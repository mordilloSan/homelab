package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// T-18: password required, JSON only, edits applied and persisted without a restart.
func TestUI(t *testing.T) {
	a, _ := setup(t)
	h, _ := bcrypt.GenerateFromPassword([]byte("uma-password-boa"), bcrypt.MinCost)
	a.cfg.UI.PasswordHash = string(h)
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	if v := string(*a.view.Load()); strings.Contains(v, "0001-01-01") {
		t.Fatalf("hora zero no estado da interface (mostra-se como data): %s", v)
	}

	do := func(pw, ctype, path, body string) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
		if body == "" {
			req.Method = http.MethodGet
		}
		req.Header.Set("Content-Type", ctype)
		if pw != "" {
			req.SetBasicAuth("admin", pw)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	const pw, js = "uma-password-boa", "application/json"
	cfg := `{"mode":"auto","check_interval_s":30,"services":[{"name":"vaultwarden","wait_min":2,"stability_min":4}]}`

	for _, c := range []struct {
		pw, ctype, path, body string
		want                  int
	}{
		{"", "", "/api/status", "", 401},
		{"errada", "", "/api/status", "", 401},
		{pw, "", "/api/status", "", 200},
		{pw, "text/plain", "/api/config", cfg, 415},
		{pw, js, "/api/config", `{"mode":"auto","check_interval_s":5}`, 400},
		{pw, js, "/api/config", cfg, 204},
		{pw, js, "/api/maintenance", `{"service":"immich","minutes":30}`, 204},
		{pw, js, "/api/action", `{"service":"homepage","action":"return"}`, 409},
		{pw, js, "/api/action", `{"service":"homepage","action":"failover"}`, 204},
	} {
		if got := do(c.pw, c.ctype, c.path, c.body); got != c.want {
			t.Errorf("%s %s: HTTP %d, esperado %d", c.path, c.body, got, c.want)
		}
	}
	if sv, _ := a.service("vaultwarden"); sv.WaitMin != 2 || sv.StabilityMin != 4 || a.cfg.CheckIntervalS != 30 {
		t.Errorf("config não aplicada: %+v", sv)
	}
	saved, err := loadConfig(a.cfgPath)
	if err != nil || saved.Services[0].WaitMin != 2 {
		t.Errorf("config não guardada: %v", err)
	}
	if a.st.Services["immich"].MaintUntil.IsZero() || state(a, "homepage") != FailingOver {
		t.Error("manutenção ou ação não aplicada")
	}
}
