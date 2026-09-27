package agent

import (
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// T-18: the login form (admin/admin at first), sessions, JSON only, edits
// applied and persisted without a restart, and the password change.
func TestUI(t *testing.T) {
	a, _ := setup(t)
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	if v := string(*a.view.Load()); strings.Contains(v, "0001-01-01") || !strings.Contains(v, `"default_password":true`) {
		t.Fatalf("estado da interface: hora zero ou sem o aviso da password por defeito: %s", v)
	}

	// a browser: keeps the cookie, does not follow redirects so they can be checked
	browser := func() *http.Client {
		jar, _ := cookiejar.New(nil)
		return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	login := func(c *http.Client, user, pw string) string {
		t.Helper()
		resp, err := c.PostForm(srv.URL+"/login", url.Values{"username": {user}, "password": {pw}})
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.Header.Get("Location")
	}
	do := func(c *http.Client, ctype, path, body string) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
		if body == "" {
			req.Method = http.MethodGet
		}
		req.Header.Set("Content-Type", ctype)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	anon, me, other := browser(), browser(), browser()
	if do(anon, "", "/", "") != http.StatusSeeOther || do(anon, "", "/api/status", "") != 401 || do(anon, "", "/login", "") != 200 {
		t.Fatal("sem sessão: a página tem de ir para o login e a API responder 401")
	}
	for _, bad := range [][2]string{{"admin", "errada"}, {"outro", "admin"}} {
		if got := login(anon, bad[0], bad[1]); got != "/login?erro=1" {
			t.Fatalf("login %v: foi para %q", bad, got)
		}
	}
	if login(me, "admin", "admin") != "/" || login(other, "admin", "admin") != "/" {
		t.Fatal("admin/admin não entra")
	}
	if do(me, "", "/login", "") != http.StatusSeeOther {
		t.Fatal("com sessão, o login devia mandar para a página")
	}

	const js = "application/json"
	cfg := `{"mode":"auto","check_interval_s":30,"services":[{"name":"vaultwarden","wait_min":2,"stability_min":4}]}`
	for _, c := range []struct {
		ctype, path, body string
		want              int
	}{
		{"", "/api/status", "", 200},
		{"text/plain", "/api/config", cfg, 415},
		{js, "/api/config", `{"mode":"auto","check_interval_s":5}`, 400},
		{js, "/api/config", cfg, 204},
		{js, "/api/maintenance", `{"service":"immich","minutes":30}`, 204},
		{js, "/api/action", `{"service":"homepage","action":"return"}`, 409},
		{js, "/api/action", `{"service":"homepage","action":"failover"}`, 204},
		{js, "/api/password", `{"current":"errada","new":"uma-password-nova"}`, 403},
		{js, "/api/password", `{"current":"admin","new":"curta"}`, 400},
		{js, "/api/password", `{"current":"admin","new":"uma-password-nova"}`, 204},
		{"", "/api/status", "", 200}, // the session that changed it goes on
	} {
		if got := do(me, c.ctype, c.path, c.body); got != c.want {
			t.Errorf("%s %s: HTTP %d, esperado %d", c.path, c.body, got, c.want)
		}
	}
	if do(other, "", "/api/status", "") != 401 {
		t.Error("a outra sessão continua aberta depois de mudar a password")
	}
	if login(browser(), "admin", "admin") != "/login?erro=1" || login(browser(), "admin", "uma-password-nova") != "/" {
		t.Error("depois de mudar, a password antiga ainda entra ou a nova não")
	}
	if do(me, js, "/api/logout", "{}") != 204 || do(me, "", "/api/status", "") != 401 {
		t.Error("sair não terminou a sessão")
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

// The image's HEALTHCHECK: open (no session), and 0.0.0.0 is asked on loopback.
func TestHealthcheck(t *testing.T) {
	a, _ := setup(t)
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	if err := Healthcheck("0.0.0.0:" + port); err != nil {
		t.Fatal(err)
	}
	srv.Close()
	if Healthcheck("0.0.0.0:"+port) == nil {
		t.Fatal("healthcheck ok com a interface em baixo")
	}
}

// The address in the startup log: the TNAS IP instead of 0.0.0.0.
func TestUIURL(t *testing.T) {
	for listen, want := range map[string]string{
		"0.0.0.0:8099":      "http://192.168.1.249:8099",
		":8099":             "http://192.168.1.249:8099",
		"[::]:8099":         "http://192.168.1.249:8099",
		"192.168.1.10:8099": "http://192.168.1.10:8099",
	} {
		if got, _ := UIURL(listen, "192.168.1.249"); got != want {
			t.Errorf("%s: %s, queria %s", listen, got, want)
		}
	}
}
