package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestMiddlewareMeshRoutesNotAuthed guards the bug where /api/v1/enroll (and
// peer management routes) were behind UI auth: an enrolling node has no
// session cookie and got 401 "unauthorized" before its one-time token was
// ever checked.
func TestMiddlewareMeshRoutesNotAuthed(t *testing.T) {
	am := &AuthManager{enabled: true, sessionSecret: []byte("test")}

	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})
	handler := am.Middleware(next)

	peerPaths := []string{
		"/api/v1/register",
		"/api/v1/heartbeat",
		"/api/v1/enroll",
		"/api/v1/exec",
		"/api/v1/docker/control",
		"/api/v1/hosts",
		"/api/v1/metrics/latest",
	}
	for _, p := range peerPaths {
		reached = false
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("POST", p, nil))
		if !reached {
			t.Errorf("peer route %s must pass without a session, got status %d", p, rec.Code)
		}
	}

	// UI/API routes still require auth
	for _, p := range []string{"/", "/api/v1/llm/chat", "/api/v1/debug/diagnostics", "/ui/settings"} {
		reached = false
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if reached {
			t.Errorf("%s must NOT be reachable without a session", p)
		}
		if rec.Code != http.StatusUnauthorized && p != "/" && p != "/ui/settings" {
			t.Errorf("%s: expected 401, got %d", p, rec.Code)
		}
	}
}
