package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dx111ge/homelabmon/internal/hub/llm"
	"github.com/dx111ge/homelabmon/internal/store"
)

func llmChatResponse(t *testing.T, u *UIServer) string {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/llm/chat", strings.NewReader(`{"message":"hi","session_id":"s1"}`))
	rec := httptest.NewRecorder()
	u.handleLLMChat(rec, req)
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return body["error"]
}

// TestLLMChatErrorMessages verifies the three states of the chat endpoint:
// configured-but-unreachable (truthful message), not configured, and enabled
// via the post-startup setter.
func TestLLMChatErrorMessages(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	// configured (client present) but Ollama was down at startup
	u := &UIServer{store: st, llmClient: llm.NewClient("http://192.168.178.250:11434", "m")}
	msg := llmChatResponse(t, u)
	if !strings.Contains(msg, "unreachable when the hub started") || !strings.Contains(msg, "192.168.178.250:11434") {
		t.Errorf("expected truthful unreachable message, got: %q", msg)
	}
	if strings.Contains(msg, "not configured") {
		t.Errorf("misleading 'not configured' for configured-but-down LLM: %q", msg)
	}

	// not configured at all
	u2 := &UIServer{store: st}
	msg = llmChatResponse(t, u2)
	if !strings.Contains(msg, "LLM not configured") {
		t.Errorf("expected not-configured message, got: %q", msg)
	}

	// enabled after startup via setter (Ollama came back)
	u.SetChatHandler(&llm.ChatHandler{})
	if u.getChatHandler() == nil {
		t.Fatal("SetChatHandler did not store the handler")
	}
}
