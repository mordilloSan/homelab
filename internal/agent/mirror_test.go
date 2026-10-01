package agent

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// No file written to the mirror (btrfs find-new) for mirror_stale_days is one
// alert; a write again, an event.
func TestMirrorStale(t *testing.T) {
	a, f := setup(t)
	f.outs = map[string]string{"btrfs subvolume find-new": "transid marker was 7\n"}
	a.Tick(t0)
	if a.st.MirrorGen != 7 || !a.st.MirrorChanged.Equal(t0) {
		t.Fatalf("geração %d desde %v", a.st.MirrorGen, a.st.MirrorChanged)
	}
	for h := 1; h <= 49; h++ {
		a.Tick(t0.Add(time.Duration(h) * time.Hour))
	}
	a.mailJobs.Wait()
	n := 0
	for _, e := range a.events {
		if strings.Contains(e.Msg, "o espelho não muda desde") {
			n++
		}
	}
	if n != 1 || !a.st.MirrorStale {
		t.Fatalf("%d avisos do espelho parado", n)
	}
	f.mu.Lock()
	f.outs["btrfs subvolume find-new"] = "inode 257 file offset 0 len 4096 disk start 0 offset 0 gen 8 flags NONE vaultwarden/db.sqlite3\ntransid marker was 8\n"
	f.mu.Unlock()
	a.Tick(t0.Add(51 * time.Hour))
	if a.st.MirrorStale || !hasEvent(a, "o espelho voltou a mudar") {
		t.Fatal("o espelho voltou a mudar e não se disse")
	}
}

// A new folder in the mirror is an alert and a hint until the page saw it; a
// protected service's compose that changed is an alert and a new scan.
func TestMirrorFolders(t *testing.T) {
	a, _ := svcSetup(t)
	root := filepath.Join(a.cfg.Paths.MirrorSubvol, a.cfg.Paths.MirrorRoot)
	a.cfg.Services = append(a.cfg.Services, Service{Name: "nextcloud", Dir: "nextcloud", Host: "cloud.engmariz.com", WaitMin: 5})
	a.Tick(t0) // the first look: nothing is new
	if len(a.st.MirrorSeen) != 3 || a.st.MirrorNew != nil {
		t.Fatalf("primeira vista: %v %v", a.st.MirrorSeen, a.st.MirrorNew)
	}
	if err := os.MkdirAll(filepath.Join(root, "jellyfin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "jellyfin", "docker-compose.yml"), []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(root, "nextcloud", "docker-compose.yml"), later, later); err != nil {
		t.Fatal(err)
	}
	a.Tick(t0.Add(30 * time.Minute)) // not an hour yet
	if a.st.MirrorNew != nil {
		t.Fatal("olhou antes de uma hora")
	}
	a.Tick(t0.Add(time.Hour))
	if len(a.st.MirrorNew) != 1 || a.st.MirrorNew[0] != "jellyfin" || !hasEvent(a, "pasta nova no espelho: jellyfin") || !hasEvent(a, "o docker-compose.yml mudou no espelho") {
		t.Fatalf("novidades: %v %v", a.st.MirrorNew, a.events)
	}
	r := withSession(a, httptest.NewRequest(http.MethodPost, "/api/mirror/seen", strings.NewReader("{}")))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusNoContent || a.st.MirrorNew != nil {
		t.Fatalf("a página viu as novidades: HTTP %d, %v", w.Code, a.st.MirrorNew)
	}
}

// The verdict lists what a failover would lack, emails a new problem once,
// and says when all is well again.
func TestVerdict(t *testing.T) {
	a, f := setup(t)
	a.st.Images = map[string]Stack{"npm": {Images: []Image{{Ref: "jc21/nginx-proxy-manager", Present: true}}}, "vaultwarden": {Images: []Image{{Ref: "vaultwarden/server"}}, Notes: []string{"a rede proxy não existe no TNAS"}}}
	a.checkReady()
	p := a.st.Verdict.Problems
	if len(p) != 2 || !strings.Contains(p[0], "vaultwarden: 1 imagem em falta") || !strings.Contains(p[1], "a rede proxy") {
		t.Fatalf("problemas: %q", p)
	}
	a.checkReady()
	n := 0
	for _, e := range a.events {
		if strings.HasPrefix(e.Msg, "problemas para um failover:") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("o mesmo problema avisado %d vezes", n)
	}
	a.st.Images = map[string]Stack{"npm": {}, "vaultwarden": {}}
	f.failCmd = nil
	a.checkReady()
	if a.st.Verdict.Problems != nil || !hasEvent(a, "pronto para failover outra vez") {
		t.Fatalf("tudo bem outra vez: %q", a.st.Verdict.Problems)
	}
}

// A folder ignored by hand (an old container's, still in the backup) leaves
// the folder list and the mirror's watch; a protected one is not ignored.
func TestMirrorIgnore(t *testing.T) {
	a, _ := svcSetup(t)
	a.cfg.Services = append(a.cfg.Services, Service{Name: "nextcloud", Dir: "nextcloud", Host: "cloud.engmariz.com", WaitMin: 5})
	if code, _ := postTo(t, a.postMirrorIgnore, `{"dir":"nextcloud","ignore":true}`); code != http.StatusConflict {
		t.Fatalf("ignorou a pasta de um serviço protegido: HTTP %d", code)
	}
	if code, _ := postTo(t, a.postMirrorIgnore, `{"dir":"../x","ignore":true}`); code != http.StatusBadRequest {
		t.Fatalf("fora do espelho: HTTP %d", code)
	}
	a.st.MirrorNew = []string{"paperless"}
	if code, body := postTo(t, a.postMirrorIgnore, `{"dir":"paperless","ignore":true}`); code != http.StatusNoContent || !slices.Equal(a.cfg.Ignored, []string{"paperless"}) || len(a.st.MirrorNew) != 0 {
		t.Fatalf("HTTP %d %s: %v %v", code, body, a.cfg.Ignored, a.st.MirrorNew)
	}
	if b, _ := os.ReadFile(a.cfgPath); !strings.Contains(string(b), "- paperless") {
		t.Fatalf("não ficou gravado:\n%s", b)
	}
	w := httptest.NewRecorder()
	a.getMirror(w, httptest.NewRequest(http.MethodGet, "/api/mirror", nil))
	if strings.Contains(w.Body.String(), "paperless") {
		t.Fatalf("a pasta ignorada continua na lista: %s", w.Body)
	}
	a.Tick(t0)
	if _, ok := a.st.MirrorSeen["paperless"]; ok || len(a.st.MirrorSeen) != 2 {
		t.Fatalf("a vigia olha para a pasta ignorada: %v", a.st.MirrorSeen)
	}
	if code, _ := postTo(t, a.postMirrorIgnore, `{"dir":"paperless","ignore":false}`); code != http.StatusNoContent || len(a.cfg.Ignored) != 0 {
		t.Fatalf("deixar de ignorar: HTTP %d %v", code, a.cfg.Ignored)
	}
}
