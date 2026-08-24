package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dx111ge/homelabmon/internal/models"
	"github.com/dx111ge/homelabmon/internal/store"
)

// fakeOllama mimics the Ollama /api/chat endpoint: calls with tools get a
// plain answer, calls without tools (title generation) get a fixed title.
// It records the last request so tests can verify context handling.
type fakeOllama struct {
	server     *httptest.Server
	lastBody   string
	titleCalls int
}

func newFakeOllama(t *testing.T) *fakeOllama {
	t.Helper()
	f := &fakeOllama{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/chat", func(w http.ResponseWriter, r *http.Request) {
		var req ChatRequest
		json.NewDecoder(r.Body).Decode(&req)
		b, _ := json.Marshal(req)
		f.lastBody = string(b)

		content := "The answer is 42."
		if req.Tools == nil {
			f.titleCalls++
			content = "Homelab Disk Check"
		}
		json.NewEncoder(w).Encode(ChatResponse{
			Message: Message{Role: "assistant", Content: content},
			Done:    true,
		})
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func newTestChatHandler(t *testing.T) (*ChatHandler, *store.Store, *fakeOllama) {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	fake := newFakeOllama(t)
	identity := &models.NodeIdentity{ID: "node-a", Hostname: "host-a", BindAddr: ":9600"}
	executor := NewToolExecutor(st, identity)
	handler := NewChatHandler(NewClient(fake.server.URL, "test-model"), executor, st)
	return handler, st, fake
}

func TestChatSessionPersistedAndTitled(t *testing.T) {
	h, st, fake := newTestChatHandler(t)

	resp, actions, err := h.Chat(context.Background(), "sess-1", "check my disks")
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if resp != "The answer is 42." || actions != nil {
		t.Errorf("unexpected response: %q actions=%v", resp, actions)
	}

	// both turns persisted
	msgs, err := st.GetChatMessages(context.Background(), "sess-1", 10)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("expected 2 persisted messages, got %d err %v", len(msgs), err)
	}
	if msgs[0].Role != "user" || msgs[0].Content != "check my disks" {
		t.Errorf("user message wrong: %+v", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "The answer is 42." {
		t.Errorf("assistant message wrong: %+v", msgs[1])
	}

	// title generated asynchronously by the LLM
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		sessions, _ := st.ListChatSessions(context.Background(), 10)
		if len(sessions) == 1 && sessions[0].Title == "Homelab Disk Check" {
			if sessions[0].MessageCount != 2 {
				t.Errorf("message count wrong: %+v", sessions[0])
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("title not generated (calls=%d)", fake.titleCalls)
}

func TestChatHistoryLoadedFromStore(t *testing.T) {
	h, st, fake := newTestChatHandler(t)

	if _, _, err := h.Chat(context.Background(), "sess-2", "first question"); err != nil {
		t.Fatalf("chat 1: %v", err)
	}
	// wait for async title to settle so the second request body is stable
	time.Sleep(200 * time.Millisecond)

	if _, _, err := h.Chat(context.Background(), "sess-2", "second question"); err != nil {
		t.Fatalf("chat 2: %v", err)
	}

	// second call must include the first exchange as context
	if !strings.Contains(fake.lastBody, "first question") {
		t.Errorf("prior history not sent to LLM: %s", truncate(fake.lastBody, 300))
	}
	if !strings.Contains(fake.lastBody, "second question") {
		t.Errorf("new message missing: %s", truncate(fake.lastBody, 300))
	}

	// delete session removes messages
	h.ClearSession("sess-2")
	msgs, _ := st.GetChatMessages(context.Background(), "sess-2", 10)
	if len(msgs) != 0 {
		t.Errorf("session not deleted: %d messages remain", len(msgs))
	}
}
