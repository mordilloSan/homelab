package agent

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// T-18: login from user.yml (admin/admin at first), JSON only, edits applied and
// persisted without a restart, and the password change.
func TestUI(t *testing.T) {
	a, _ := setup(t)
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	if v := string(*a.view.Load()); strings.Contains(v, "0001-01-01") || !strings.Contains(v, `"default_password":true`) {
		t.Fatalf("estado da interface: hora zero ou sem o aviso da password por defeito: %s", v)
	}

	do := func(user, pw, ctype, path, body string) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
		if body == "" {
			req.Method = http.MethodGet
		}
		req.Header.Set("Content-Type", ctype)
		if pw != "" {
			req.SetBasicAuth(user, pw)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	const js = "application/json"
	cfg := `{"mode":"auto","check_interval_s":30,"services":[{"name":"vaultwarden","wait_min":2,"stability_min":4}]}`

	for _, c := range []struct {
		user, pw, ctype, path, body string
		want                        int
	}{
		{"", "", "", "/api/status", "", 401},
		{"admin", "errada", "", "/api/status", "", 401},
		{"outro", "admin", "", "/api/status", "", 401},
		{"admin", "admin", "", "/api/status", "", 200},
		{"admin", "admin", "text/plain", "/api/config", cfg, 415},
		{"admin", "admin", js, "/api/config", `{"mode":"auto","check_interval_s":5}`, 400},
		{"admin", "admin", js, "/api/config", cfg, 204},
		{"admin", "admin", js, "/api/maintenance", `{"service":"immich","minutes":30}`, 204},
		{"admin", "admin", js, "/api/action", `{"service":"homepage","action":"return"}`, 409},
		{"admin", "admin", js, "/api/action", `{"service":"homepage","action":"failover"}`, 204},
		{"admin", "admin", js, "/api/password", `{"current":"errada","new":"uma-password-nova"}`, 403},
		{"admin", "admin", js, "/api/password", `{"current":"admin","new":"curta"}`, 400},
		{"admin", "admin", js, "/api/password", `{"current":"admin","new":"uma-password-nova"}`, 204},
		{"admin", "admin", "", "/api/status", "", 401},
		{"admin", "uma-password-nova", "", "/api/status", "", 200},
	} {
		if got := do(c.user, c.pw, c.ctype, c.path, c.body); got != c.want {
			t.Errorf("%s %s %s: HTTP %d, esperado %d", c.user, c.path, c.body, got, c.want)
		}
	}
	if sv, _ := a.service("vaultwarden"); sv.WaitMin != 2 || sv.StabilityMin != 4 || a.cfg.CheckIntervalS != 30 {
		t.Errorf("config não aplicada: %+v", sv)
	}
	if saved, err := LoadConfig(a.cfgPath); err != nil || saved.Services[0].WaitMin != 2 {
		t.Errorf("config não guardada: %v", err)
	}
	if a.st.Services["immich"].MaintUntil.IsZero() || state(a, "homepage") != FailingOver {
		t.Error("manutenção ou ação não aplicada")
	}
	// the new password is in no file: user.yml has only its hash, and it survives a restart
	b, _ := os.ReadFile(a.userPath)
	if strings.Contains(string(b), "uma-password-nova") || !strings.Contains(string(b), "$2a$") {
		t.Fatalf("user.yml: %s", b)
	}
	if c, err := loadUser(a.userPath); err != nil || c.Default {
		t.Fatalf("depois de mudar, a password por defeito ainda conta: %v", err)
	}
}

// First start on an empty TNAS: the shipped config is written, and never over an existing one.
func TestFirstRun(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config", "failover.yml")
	if err := FirstRun(p); err != nil {
		t.Fatal(err)
	}
	if cfg, err := LoadConfig(p); err != nil || cfg.Mode != "observe" {
		t.Fatalf("config criada errada: %v", err)
	}
	if err := os.WriteFile(p, []byte("editado"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := FirstRun(p); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "editado" {
		t.Fatal("o primeiro arranque escreveu por cima de uma config existente")
	}
}
