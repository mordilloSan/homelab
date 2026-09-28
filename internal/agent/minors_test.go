package agent

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// With no events, /api/events is an empty list, not null.
func TestEventsAPIEmpty(t *testing.T) {
	a, _ := setup(t)
	a.events = nil
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, withSession(a, httptest.NewRequest(http.MethodGet, "/api/events", nil)))
	if strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("%q", w.Body.String())
	}
}

// A line cut short (a crash or a full disk mid-write) does not swallow the
// next event.
func TestEventAfterCutLine(t *testing.T) {
	a, _ := setup(t)
	if err := os.WriteFile(a.eventsPath, []byte(`{"t":"2026-09-28T10:00:00Z","msg":"cor`), 0o600); err != nil {
		t.Fatal(err)
	}
	a.event("", "depois")
	evs, _ := loadEvents(a.eventsPath)
	if len(evs) != 1 || evs[0].Msg != "depois" {
		t.Fatalf("%+v", evs)
	}
}

// The first tick of a new day trims the file.
func TestEventsDailyTrim(t *testing.T) {
	a, _ := setup(t)
	a.Tick(t0)
	a.events = append([]Event{{T: t0.Add(-31 * 24 * time.Hour), Msg: "velho"}}, a.events...)
	a.Tick(t0.Add(time.Minute)) // same day: nothing
	if !hasEvent(a, "velho") {
		t.Fatal("aparou no mesmo dia")
	}
	a.Tick(t0.Add(24 * time.Hour))
	if hasEvent(a, "velho") {
		t.Fatal("não aparou no dia seguinte")
	}
}

// A restart before the first save still has the events in state.json: they
// are not moved twice.
func TestEventsMigrateOnce(t *testing.T) {
	dir := t.TempDir()
	writeLines(t, filepath.Join(dir, "state.json"), `{"router_ok":true,"services":{},"events":[`+evLine(time.Now().Add(-time.Hour), "antigo")+`]}`)
	f := &fake{noPing: map[string]bool{}, down: map[string]bool{}}
	newTestAgent(t, dir, f)
	again := newTestAgent(t, dir, f)
	if n := strings.Count(strings.Join(func() []string {
		var s []string
		for _, e := range again.events {
			s = append(s, e.Msg)
		}
		return s
	}(), " "), "antigo"); n != 1 {
		t.Fatalf("o evento migrado aparece %d vezes", n)
	}
}

func TestConfigPartialInterval(t *testing.T) {
	a, _ := setup(t)
	mode := a.cfg.Mode
	if code, _ := postTo(t, a.postConfig, `{"check_interval_s":30}`); code != 204 || a.cfg.CheckIntervalS != 30 || a.cfg.Mode != mode {
		t.Fatalf("%d %d %s", code, a.cfg.CheckIntervalS, a.cfg.Mode)
	}
}

// A request in absolute form (GET http://h/x) is redirected to https://h/x.
func TestRedirectAbsoluteForm(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	go redirect(c2)
	_, _ = c1.Write([]byte("GET http://tnas.lan:8099/x?y=1 HTTP/1.1\r\nHost: tnas.lan:8099\r\n\r\n"))
	resp, err := http.ReadResponse(bufio.NewReader(c1), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.Header.Get("Location"); got != "https://tnas.lan:8099/x?y=1" {
		t.Fatalf("Location %q", got)
	}
}

// The healthcheck asks the address the UI is on, not a new one saved and
// waiting for a restart.
func TestRunningListen(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state.json")
	if got := RunningListen(state, "0.0.0.0:8099"); got != "0.0.0.0:8099" {
		t.Fatal(got)
	}
	if err := SetRunningListen(state, "127.0.0.1:18099"); err != nil {
		t.Fatal(err)
	}
	if got := RunningListen(state, "0.0.0.0:9000"); got != "127.0.0.1:18099" {
		t.Fatal(got)
	}
}

// A leftover .tmp with loose permissions does not make the file loose.
func TestWriteAtomicMode(t *testing.T) {
	p := filepath.Join(t.TempDir(), "chave")
	if err := os.WriteFile(p+".tmp", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(p, []byte("segredo")); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatalf("%v", fi.Mode().Perm())
	}
}

// A scan asked for while one runs is done once that one ends, with the new config.
func TestScanAgainWhenAsked(t *testing.T) {
	a, f := setup(t)
	a.scanning.Store(true) // one is running
	a.scanImages()         // asked meanwhile: remembered
	a.scanning.Store(false)
	a.scanImages()
	n := 0
	for _, c := range f.scans {
		if strings.HasPrefix(c, "docker compose -p failover-npm") && strings.Contains(c, "config --images") {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("o NPM foi verificado %d vezes: %v", n, f.scans)
	}
}

// A service added while a check was in flight is not called failed by it.
func TestServiceAddedMidCheck(t *testing.T) {
	a, _ := setup(t)
	p := a.probe()
	a.cfg.Services = append(a.cfg.Services, Service{Name: "novo", Dir: "novo", Host: "novo.engmariz.com", WaitMin: 1})
	a.mu.Lock()
	a.now = t0
	a.evaluate(p)
	a.mu.Unlock()
	if hasEvent(a, "falha no servidor") || len(a.beats["novo"]) != 0 {
		t.Fatalf("eventos %v, barras %v", a.events, a.beats["novo"])
	}
}

// A settings save refused by a service's rule says which service.
func TestSectionServiceRule(t *testing.T) {
	a, _ := setup(t)
	code, body := postTo(t, a.postSection, `{"section":"rede","values":{"lan_iface":""}}`)
	if code != 400 || strings.Contains(body, `"field"`) || !strings.Contains(body, "unifi") {
		t.Fatalf("HTTP %d %s", code, body)
	}
}

func TestDotFolderRefused(t *testing.T) {
	base, _ := LoadConfig("config/failover.yml")
	base.NPM.Dir = "."
	var fe *FieldError
	if err := base.validate(); !errors.As(err, &fe) || fe.Field != "npm.dir" {
		t.Fatalf("%v", err)
	}
}
