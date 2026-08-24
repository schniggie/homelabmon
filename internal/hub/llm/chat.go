package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/dx111ge/homelabmon/internal/store"
	"github.com/rs/zerolog/log"
)

const systemPrompt = `You are HomelabMon's AI agent. You monitor AND manage the user's homelab: a mesh of nodes running services, Docker containers, network devices, and external integrations.

You have full access to the platform through tools:

Reading the environment:
- Hosts, devices, metrics, services, Docker containers, mesh peers, integrations, settings

Managing the homelab:
- Run shell commands on any agent node -- Linux, macOS, Windows, FreeBSD/OPNsense (run_command). This enables full devops work: package upgrades, service restarts, log inspection, config changes.
- Start, stop, or restart Docker containers on ANY node in the mesh (docker_control)
- Trigger network scans to discover new devices (trigger_network_scan)
- Send push notifications (send_notification)
- Change settings: alert thresholds, retention, scan interval, notification channels, site label (update_settings)
- Rename hosts, fix device classifications, remove stale entries (rename_host, set_device_type, delete_host)
- Test, sync, or remove external integrations like FRITZ!Box, Unifi, Home Assistant, Pi-hole, pfSense (manage_integration)
- Connect new nodes to the mesh (add_peer)
- Review what commands were already run (list_exec_history)

Persistent memory:
- Every management action you perform is recorded automatically per node.
- When you start working on a node, call recall_memory for that host to see past actions, fixes, and notes from earlier sessions.
- After completing significant work (a fix, an upgrade, a discovered quirk, a user preference), store a concise note with remember() so future sessions benefit. Record facts that are NOT derivable from the live data: what was changed and why, known issues, where configs live, user preferences.

Safety rules -- follow them strictly:
- run_command requires confirmation for EVERY command, no matter how harmless it looks. State the exact command and the target host, then wait for the user's approval. Only call with confirm=true after the user agreed to that exact command.
- Disruptive actions (stop/restart container, delete host, delete integration) likewise require explicit user confirmation.
- Never call a destructive tool with confirm=true preemptively or "just in case".
- For fleet-wide operations (e.g. "upgrade everything"), propose the plan with the exact commands per host and get approval once, then execute host by host. If any host fails, stop and report before continuing.
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
// Set high enough for fleet-wide devops workflows (e.g. one command per host
// across a dozen hosts, with reads in between).
const maxToolRounds = 32

// Action records a tool invocation for display in the chat UI.
type Action struct {
	Tool   string `json:"tool"`
	Args   string `json:"args,omitempty"`
	Result string `json:"result,omitempty"`
	Err    bool   `json:"error,omitempty"`
}

// ChatHandler manages conversations with the LLM. Sessions are persisted in
// the store with LLM-generated titles, so history survives restarts.
type ChatHandler struct {
	client   *Client
	executor *ToolExecutor
	store    *store.Store
	mu       sync.Mutex // serializes chats; store access is single-conn anyway
}

func NewChatHandler(client *Client, executor *ToolExecutor, s *store.Store) *ChatHandler {
	return &ChatHandler{
		client:   client,
		executor: executor,
		store:    s,
	}
}

// Chat sends a user message and returns the assistant's response plus a log
// of the tool actions executed along the way. It handles the tool-calling
// loop automatically and persists the exchange.
func (h *ChatHandler) Chat(ctx context.Context, sessionID, userMessage string) (string, []Action, error) {
	var actions []Action

	h.mu.Lock()
	defer h.mu.Unlock()

	// Load persisted history (system prompt + last turns)
	messages := []Message{{Role: "system", Content: systemPrompt}}
	persisted, err := h.store.GetChatMessages(ctx, sessionID, 2*20)
	if err == nil {
		for _, m := range persisted {
			if m.Role == "user" || m.Role == "assistant" {
				messages = append(messages, Message{Role: m.Role, Content: m.Content})
			}
		}
	}
	firstExchange := len(persisted) == 0

	messages = append(messages, Message{Role: "user", Content: userMessage})
	h.store.AppendChatMessage(ctx, sessionID, "user", userMessage, "[]")

	tools := ToolDefinitions()

	for round := 0; round < maxToolRounds; round++ {
		resp, err := h.client.Chat(ctx, messages, tools)
		if err != nil {
			return "", actions, fmt.Errorf("LLM error: %w", err)
		}

		messages = append(messages, resp.Message)

		// If no tool calls, we have our final answer
		if len(resp.Message.ToolCalls) == 0 {
			h.persist(ctx, sessionID, resp.Message.Content, actions)
			if firstExchange {
				h.generateTitleAsync(sessionID, userMessage, resp.Message.Content)
			}
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
	h.persist(ctx, sessionID, resp.Message.Content, actions)
	if firstExchange {
		h.generateTitleAsync(sessionID, userMessage, resp.Message.Content)
	}

	return resp.Message.Content, actions, nil
}

// persist stores the assistant reply with its action log.
func (h *ChatHandler) persist(ctx context.Context, sessionID, content string, actions []Action) {
	actionsJSON, _ := json.Marshal(actions)
	if err := h.store.AppendChatMessage(ctx, sessionID, "assistant", content, string(actionsJSON)); err != nil {
		log.Warn().Err(err).Msg("persist chat message")
	}
}

// generateTitleAsync asks the LLM for a short session title after the first
// exchange. Runs in the background so it never delays the chat response.
func (h *ChatHandler) generateTitleAsync(sessionID, userMsg, assistantReply string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()

		title := h.generateTitle(ctx, userMsg, assistantReply)
		if title == "" {
			title = truncate(strings.TrimSpace(userMsg), 60)
		}
		if err := h.store.SetSessionTitle(context.Background(), sessionID, title); err != nil {
			log.Warn().Err(err).Msg("set session title")
		}
	}()
}

func (h *ChatHandler) generateTitle(ctx context.Context, userMsg, assistantReply string) string {
	resp, err := h.client.Chat(ctx, []Message{
		{Role: "system", Content: "Generate a very short title (3-6 words) for this conversation between a user and their homelab monitoring agent. Respond with ONLY the title, no quotes, no punctuation at the end."},
		{Role: "user", Content: fmt.Sprintf("User: %s\n\nAgent reply (start): %s", truncate(userMsg, 300), truncate(assistantReply, 300))},
	}, nil)
	if err != nil {
		return ""
	}
	title := strings.TrimSpace(resp.Message.Content)
	// Take the first line only (models sometimes add explanations)
	if i := strings.IndexByte(title, '\n'); i >= 0 {
		title = title[:i]
	}
	title = strings.Trim(title, "\"'`* ")
	if len(title) > 80 {
		title = title[:80]
	}
	return title
}

// ClearSession removes a stored session and its messages.
func (h *ChatHandler) ClearSession(sessionID string) {
	h.store.DeleteChatSession(context.Background(), sessionID)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
