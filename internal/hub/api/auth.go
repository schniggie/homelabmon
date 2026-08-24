package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// authToken is the plaintext token loaded from disk.
// sessionSecret is derived from the token for signing cookies.
type AuthManager struct {
	token         string
	sessionSecret []byte
	enabled       bool
}

// NewAuthManager loads or generates the auth token from the data directory.
// If disabled, all requests pass through without checks.
func NewAuthManager(dataDir string, enabled bool) *AuthManager {
	am := &AuthManager{enabled: enabled}
	if !enabled {
		return am
	}

	tokenPath := filepath.Join(dataDir, "auth-token")
	if data, err := os.ReadFile(tokenPath); err == nil {
		am.token = strings.TrimSpace(string(data))
	}

	if am.token == "" {
		// Generate a random 24-char hex token
		b := make([]byte, 12)
		rand.Read(b)
		am.token = hex.EncodeToString(b)
		os.MkdirAll(dataDir, 0700)
		os.WriteFile(tokenPath, []byte(am.token+"\n"), 0600)
	}

	// Derive session signing key from token
	h := sha256.Sum256([]byte("homelabmon-session:" + am.token))
	am.sessionSecret = h[:]

	return am
}

// Token returns the auth token (for display during setup).
func (am *AuthManager) Token() string {
	return am.token
}

// Enabled returns whether auth is active.
func (am *AuthManager) Enabled() bool {
	return am.enabled
}

// Middleware returns an http.Handler that checks authentication.
// Unauthenticated requests to UI/API routes are redirected to the login page.
// Mesh peer routes (/api/v1/register, /api/v1/heartbeat, /api/v1/status, /api/v1/peers,
// /api/v1/hosts, /api/v1/metrics/*, /api/v1/services/*) are NOT protected.
func (am *AuthManager) Middleware(next http.Handler) http.Handler {
	if !am.enabled {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Always allow: login page, static files, mesh peer API
		if path == "/ui/login" || path == "/ui/login/submit" ||
			strings.HasPrefix(path, "/static/") ||
			isMeshRoute(path) {
			next.ServeHTTP(w, r)
			return
		}

		// Check session cookie
		if am.validSession(r) {
			next.ServeHTTP(w, r)
			return
		}

		// API routes return 401, page routes redirect to login
		if strings.HasPrefix(path, "/api/") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}

		http.Redirect(w, r, "/ui/login", http.StatusFound)
	})
}

// isMeshRoute returns true for peer-to-peer API routes that should not
// require UI auth. Data routes (heartbeat etc.) are trusted like the
// heartbeat itself; enrollment carries its own one-time token; exec and
// docker control follow the --exec trust model (documented: anyone who can
// reach the port -- use mTLS on untrusted networks).
func isMeshRoute(path string) bool {
	meshRoutes := []string{
		"/api/v1/register",
		"/api/v1/heartbeat",
		"/api/v1/status",
		"/api/v1/peers",
		"/api/v1/hosts",
		"/api/v1/metrics/",
		"/api/v1/services",
		"/api/v1/enroll",
		"/api/v1/exec",
		"/api/v1/docker/control",
	}
	for _, r := range meshRoutes {
		if path == r || strings.HasPrefix(path, r) {
			return true
		}
	}
	return false
}

// validSession checks the session cookie.
func (am *AuthManager) validSession(r *http.Request) bool {
	cookie, err := r.Cookie("hlm_session")
	if err != nil {
		return false
	}
	return am.verifySessionValue(cookie.Value)
}

// CheckToken validates a token string against the stored token.
func (am *AuthManager) CheckToken(input string) bool {
	return strings.TrimSpace(input) == am.token
}

// SetSessionCookie sets a signed session cookie on the response.
func (am *AuthManager) SetSessionCookie(w http.ResponseWriter) {
	value := am.createSessionValue()
	http.SetCookie(w, &http.Cookie{
		Name:     "hlm_session",
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   7 * 24 * 60 * 60, // 7 days
	})
}

// ClearSessionCookie removes the session cookie.
func (am *AuthManager) ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     "hlm_session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
}

// createSessionValue creates an HMAC-signed session value.
// Format: "timestamp:signature"
func (am *AuthManager) createSessionValue() string {
	ts := fmt.Sprintf("%d", time.Now().Unix())
	sig := am.sign(ts)
	return ts + ":" + sig
}

// verifySessionValue validates the HMAC signature and checks expiry (7 days).
func (am *AuthManager) verifySessionValue(value string) bool {
	parts := strings.SplitN(value, ":", 2)
	if len(parts) != 2 {
		return false
	}
	ts, sig := parts[0], parts[1]

	// Verify signature
	expected := am.sign(ts)
	if !hmac.Equal([]byte(sig), []byte(expected)) {
		return false
	}

	// Check expiry (7 days)
	var epoch int64
	fmt.Sscanf(ts, "%d", &epoch)
	if time.Now().Unix()-epoch > 7*24*60*60 {
		return false
	}

	return true
}

func (am *AuthManager) sign(data string) string {
	mac := hmac.New(sha256.New, am.sessionSecret)
	mac.Write([]byte(data))
	return hex.EncodeToString(mac.Sum(nil))
}
