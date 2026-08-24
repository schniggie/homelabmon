package llm

import (
	"context"
	"fmt"
	"sync"

	"github.com/rs/zerolog/log"
)

const systemPrompt = `You are HomelabMon's AI agent. You monitor AND manage the user's homelab: a mesh of nodes running services, Docker containers, network devices, and external integrations.

You have full access to the platform through tools:

Reading the environment:
- Hosts, devices, metrics, services, Docker containers, mesh peers, integrations, settings

Managing the homelab:
- Start, stop, or restart Docker containers on ANY node in the mesh (docker_control)
- Trigger network scans to discover new devices (trigger_network_scan)
- Send push notifications (send_notification)
- Change settings: alert thresholds, retention, scan interval, notification channels, site label (update_settings)
- Rename hosts, fix device classifications, remove stale entries (rename_host, set_device_type, delete_host)
- Test, sync, or remove external integrations like FRITZ!Box, Unifi, Home Assistant, Pi-hole, pfSense (manage_integration)
- Connect new nodes to the mesh (add_peer)

Safety rules -- follow them strictly:
- Disruptive actions (stop/restart container, delete host, delete integration) require EXPLICIT user confirmation. Ask first ("Restart nginx on the NAS, sure?"). Only call the tool with confirm=true after the user agreed in this conversation.
- Never call a destructive tool with confirm=true preemptively or "just in case".
- When resolving containers or hosts, use the read tools first to get exact names.

Working style:
- Use tools to get data before answering -- don't guess
- Be concise and direct; format data clearly (lists for multiple items)
- Report both percentage and absolute values for resource usage
- After performing a management action, state clearly what was done, on which host, and the result
- If something isn't found or fails, say so clearly and suggest the next step
- You can chain multiple tool calls for complex requests
- If the user asks for something beyond your tools, say what you can do instead`

// maxToolRounds limits tool-calling loops to prevent infinite cycles.
const maxToolRounds = 8

// Action records a tool invocation for display in the chat UI.
type Action struct {
	Tool   string `json:"tool"`
	Args   string `json:"args,omitempty"`
	Result string `json:"result,omitempty"`
	Err    bool   `json:"error,omitempty"`
}

// ChatHandler manages conversations with the LLM.
type ChatHandler struct {
	client   *Client
	executor *ToolExecutor
	mu       sync.Mutex
	sessions map[string][]Message // sessionID -> conversation history
}

func NewChatHandler(client *Client, executor *ToolExecutor) *ChatHandler {
	return &ChatHandler{
		client:   client,
		executor: executor,
		sessions: make(map[string][]Message),
	}
}

// Chat sends a user message and returns the assistant's response plus a log
// of the tool actions executed along the way. It handles the tool-calling
// loop automatically.
func (h *ChatHandler) Chat(ctx context.Context, sessionID, userMessage string) (string, []Action, error) {
	var actions []Action

	h.mu.Lock()
	messages, ok := h.sessions[sessionID]
	if !ok {
		messages = []Message{
			{Role: "system", Content: systemPrompt},
		}
	}
	messages = append(messages, Message{Role: "user", Content: userMessage})
	h.mu.Unlock()

	tools := ToolDefinitions()

	for round := 0; round < maxToolRounds; round++ {
		resp, err := h.client.Chat(ctx, messages, tools)
		if err != nil {
			return "", actions, fmt.Errorf("LLM error: %w", err)
		}

		messages = append(messages, resp.Message)

		// If no tool calls, we have our final answer
		if len(resp.Message.ToolCalls) == 0 {
			h.mu.Lock()
			h.sessions[sessionID] = trimHistory(messages)
			h.mu.Unlock()
			return resp.Message.Content, actions, nil
		}

		// Execute each tool call
		for _, tc := range resp.Message.ToolCalls {
			log.Info().
				Str("tool", tc.Function.Name).
				RawJSON("args", tc.Function.Arguments).
				Msg("LLM tool call")

			result, err := h.executor.Execute(ctx, tc.Function.Name, tc.Function.Arguments)
			if err != nil {
				result = fmt.Sprintf(`{"error":"%s"}`, err.Error())
			}

			actions = append(actions, Action{
				Tool:   tc.Function.Name,
				Args:   truncate(string(tc.Function.Arguments), 120),
				Result: truncate(result, 200),
				Err:    err != nil,
			})

			messages = append(messages, Message{
				Role:    "tool",
				Content: result,
			})
		}
	}

	// Exceeded max rounds - force a final response without tools
	resp, err := h.client.Chat(ctx, messages, nil)
	if err != nil {
		return "", actions, err
	}
	messages = append(messages, resp.Message)

	h.mu.Lock()
	h.sessions[sessionID] = trimHistory(messages)
	h.mu.Unlock()

	return resp.Message.Content, actions, nil
}

// ClearSession removes conversation history for a session.
func (h *ChatHandler) ClearSession(sessionID string) {
	h.mu.Lock()
	delete(h.sessions, sessionID)
	h.mu.Unlock()
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// trimHistory keeps conversation at a reasonable size.
// Keeps system prompt + last 20 messages.
func trimHistory(messages []Message) []Message {
	const maxMessages = 21 // system + 20
	if len(messages) <= maxMessages {
		return messages
	}
	// Keep system prompt + tail
	trimmed := make([]Message, 0, maxMessages)
	trimmed = append(trimmed, messages[0]) // system prompt
	trimmed = append(trimmed, messages[len(messages)-(maxMessages-1):]...)
	return trimmed
}
