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
	delay      time.Duration // artificial latency before answering
	toolFirst  bool          // first chat request answers with a run_command tool call
	chatCalls  int
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

		if f.delay > 0 {
			time.Sleep(f.delay)
		}

		f.chatCalls++
		if f.toolFirst && f.chatCalls == 1 {
			json.NewEncoder(w).Encode(ChatResponse{
				Message: Message{
					Role: "assistant",
					ToolCalls: []ToolCall{{
						Function: ToolCallFunction{
							Name:      "run_command",
							Arguments: json.RawMessage(`{"command":"uname -a"}`),
						},
					}},
				},
				Done: true,
			})
			return
		}

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

// TestChatCompletesAfterCallerCancel verifies that a chat survives the caller
// walking away: the UI aborts the HTTP request on navigation, and generation
// must still run to completion and persist for the next page load.
func TestChatCompletesAfterCallerCancel(t *testing.T) {
	h, st, fake := newTestChatHandler(t)
	fake.delay = 400 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	resp, _, err := h.Chat(ctx, "sess-orphan", "slow question")
	if err != nil {
		t.Fatalf("chat should complete despite caller cancel: %v", err)
	}
	if resp != "The answer is 42." {
		t.Errorf("unexpected response: %q", resp)
	}

	msgs, err := st.GetChatMessages(context.Background(), "sess-orphan", 10)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("expected user+assistant persisted, got %d err %v", len(msgs), err)
	}
	if msgs[0].Role != "user" || msgs[1].Role != "assistant" {
		t.Errorf("unexpected roles: %s, %s", msgs[0].Role, msgs[1].Role)
	}
}

// TestChatErrorPersistsErrorTurn verifies that a failed exchange is recorded
// as an 'error' turn so a client that left mid-request sees the failure, and
// that the error turn is not fed back to the LLM as history.
func TestChatErrorPersistsErrorTurn(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/chat", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model exploded", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	identity := &models.NodeIdentity{ID: "node-a", Hostname: "host-a", BindAddr: ":9600"}
	h := NewChatHandler(NewClient(srv.URL, "test-model"), NewToolExecutor(st, identity), st)

	if _, _, err := h.Chat(context.Background(), "sess-err", "do something"); err == nil {
		t.Fatal("expected chat to fail")
	}

	msgs, err := st.GetChatMessages(context.Background(), "sess-err", 10)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("expected user+error persisted, got %d err %v", len(msgs), err)
	}
	if msgs[0].Role != "user" {
		t.Errorf("first message role = %q, want user", msgs[0].Role)
	}
	if msgs[1].Role != "error" || !strings.Contains(msgs[1].Content, "500") {
		t.Errorf("second message = %q/%q, want error turn mentioning 500", msgs[1].Role, msgs[1].Content)
	}
}

func TestWithAutoConfirm(t *testing.T) {
	gated := json.RawMessage(`{"command":"uname -a"}`)
	got := withAutoConfirm("run_command", gated)
	if !strings.Contains(string(got), `"confirm":true`) {
		t.Errorf("confirm not injected: %s", got)
	}
	already := json.RawMessage(`{"command":"x","confirm":true}`)
	if got = withAutoConfirm("run_command", already); string(got) != string(already) {
		t.Errorf("existing confirm overwritten: %s", got)
	}
	readTool := json.RawMessage(`{"hostname":"web"} `)
	if got = withAutoConfirm("list_hosts", readTool); string(got) == string(json.RawMessage(`{"hostname":"web","confirm":true} `)) {
		t.Errorf("confirm injected into non-gated tool: %s", got)
	}
}

// TestAutoApproveOffRequiresConfirmation verifies the default: a run_command
// tool call without confirm hits the executor's confirmation gate.
func TestAutoApproveOffRequiresConfirmation(t *testing.T) {
	h, _, fake := newTestChatHandler(t)
	fake.toolFirst = true

	if _, _, err := h.Chat(context.Background(), "sess-guard", "run uname"); err != nil {
		t.Fatalf("chat: %v", err)
	}
	// The tool result fed back to the LLM must be the confirmation refusal,
	// i.e. the executor gate held without auto-approve
	if !strings.Contains(fake.lastBody, "confirmation required") {
		t.Fatalf("expected confirmation-required tool result in LLM context, got: %s", truncate(fake.lastBody, 500))
	}
	if strings.Contains(fake.lastBody, "AUTO-APPROVE MODE") {
		t.Errorf("auto-approve prompt leaked into non-auto session")
	}
}

// TestAutoApproveSessionExecutesWithoutConfirm verifies that a session with
// auto-approve enabled injects confirm=true and tells the model it may act.
func TestAutoApproveSessionExecutesWithoutConfirm(t *testing.T) {
	h, st, fake := newTestChatHandler(t)
	fake.toolFirst = true

	if err := st.SetChatAutoApprove(context.Background(), "sess-auto", true); err != nil {
		t.Fatalf("set auto approve: %v", err)
	}
	on, err := st.GetChatAutoApprove(context.Background(), "sess-auto")
	if err != nil || !on {
		t.Fatalf("flag not persisted: on=%v err=%v", on, err)
	}

	if _, _, err := h.Chat(context.Background(), "sess-auto", "run uname"); err != nil {
		t.Fatalf("chat: %v", err)
	}
	// confirm was injected, so the executor got past the gate (and stopped at
	// the missing exec router instead of asking for confirmation)
	if !strings.Contains(fake.lastBody, "not available") {
		t.Errorf("expected post-gate executor error in LLM context, got: %s", truncate(fake.lastBody, 500))
	}
	if !strings.Contains(fake.lastBody, "AUTO-APPROVE MODE") {
		t.Errorf("auto-approve system prompt missing")
	}
}

// TestSetAutoApproveCreatesSession verifies the toggle works before the first
// message exists and shows up in the session list.
func TestSetAutoApproveCreatesSession(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	if on, _ := st.GetChatAutoApprove(context.Background(), "brand-new"); on {
		t.Fatal("nonexistent session must not be auto-approved")
	}
	if err := st.SetChatAutoApprove(context.Background(), "brand-new", true); err != nil {
		t.Fatalf("set: %v", err)
	}
	sessions, _ := st.ListChatSessions(context.Background(), 10)
	if len(sessions) != 1 || !sessions[0].AutoApprove {
		t.Fatalf("session list missing flag: %+v", sessions)
	}
	if err := st.SetChatAutoApprove(context.Background(), "brand-new", false); err != nil {
		t.Fatalf("unset: %v", err)
	}
	if on, _ := st.GetChatAutoApprove(context.Background(), "brand-new"); on {
		t.Fatal("flag not cleared")
	}
}
