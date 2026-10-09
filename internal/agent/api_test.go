package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// GET /api/: closed without a session or the token; the token is made in the
// Conta section, and a new one ends the old. A Technitium that does not
// answer leaves the internet as the other box sees it.
func TestAPISummary(t *testing.T) {
	a, _ := setup(t)
	a.creds.Store(&creds{User: "admin", PasswordHash: "x"})
	type summary struct {
		Internet struct {
			OK           bool
			Status, TNAS string
		}
		DNS      struct{ Up int }
		Services struct{ Total int }
	}
	get := func(auth string) (int, summary) {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/", nil)
		if auth != "" {
			r.Header.Set("Authorization", "Bearer "+auth)
		}
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		var m summary
		_ = json.Unmarshal(w.Body.Bytes(), &m)
		return w.Code, m
	}
	newToken := func() string {
		t.Helper()
		r := withSession(a, httptest.NewRequest(http.MethodPost, "/api/token", strings.NewReader("{}")))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		var m struct{ Token string }
		if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil || m.Token == "" {
			t.Fatalf("POST /api/token: HTTP %d %s", w.Code, w.Body)
		}
		return m.Token
	}
	if c, _ := get(""); c != http.StatusUnauthorized {
		t.Fatalf("sem token: HTTP %d", c)
	}
	old := newToken()
	tok := newToken()
	if c, _ := get(old); c != http.StatusUnauthorized {
		t.Fatalf("o token anterior ainda vale: HTTP %d", c)
	}
	a.mu.Lock()
	a.st.ServerUp, a.st.TNASDNSOK, a.st.TNASNetOK = true, false, false
	a.publish()
	a.mu.Unlock()
	c, m := get(tok)
	if c != http.StatusOK {
		t.Fatalf("com token: HTTP %d", c)
	}
	if !m.Internet.OK || m.Internet.Status != "up" || m.Internet.TNAS != "unknown" || m.DNS.Up != 1 || m.Services.Total != len(a.cfg.Services) {
		t.Fatalf("resumo: %+v", m)
	}
	if v := string(a.api.Load().raw); strings.Contains(v, tok) || strings.Contains(v, "secret") {
		t.Fatal("o resumo leva um token")
	}
}
