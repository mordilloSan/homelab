package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Check must behave like curl --resolve: connect to the given IP but send
// the public name, so NPM picks the right proxy host.
func TestCheckResolve(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.Split(r.Host, ":")[0] {
		case "ok.engmariz.com":
			w.WriteHeader(http.StatusOK)
		case "redirect.engmariz.com":
			http.Redirect(w, r, "https://elsewhere.invalid/", http.StatusFound)
		default:
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	defer ts.Close()
	_, port, _ := net.SplitHostPort(ts.Listener.Addr().String())
	var s realSys
	if err := s.Check("ok.engmariz.com:"+port, "127.0.0.1"); err != nil {
		t.Errorf("ok: %v", err)
	}
	if err := s.Check("redirect.engmariz.com:"+port, "127.0.0.1"); err != nil {
		t.Errorf("3xx tem de contar como vivo: %v", err)
	}
	if err := s.Check("bitwarden.engmariz.com:"+port, "127.0.0.1"); err == nil || err.Error() != "HTTP 502" {
		t.Errorf("502 tem de falhar: %v", err)
	}
}

// R1 relies on "compose -p <project> down" working without the compose files
// (the snapshot may already be gone). Needs a local Docker.
func TestComposeDownByProjectName(t *testing.T) {
	if testing.Short() || exec.Command("docker", "info").Run() != nil {
		t.Skip("sem Docker")
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "docker-compose.yml")
	yml := "services:\n  s:\n    image: docker:cli\n    command: sleep 600\n    volumes: [data:/data]\nvolumes:\n  data: {}\n"
	if err := os.WriteFile(file, []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	var s realSys
	count := func(args ...string) int {
		out, _ := exec.Command("docker", append(args, "-q", "--filter", "label=com.docker.compose.project=failover-itest")...).Output()
		return len(strings.Fields(string(out)))
	}
	if err := s.Run("docker", "compose", "-p", "failover-itest", "-f", file, "up", "-d"); err != nil {
		t.Fatal(err)
	}
	if count("ps") != 1 {
		t.Fatal("container não arrancou")
	}
	if err := s.Run("docker", "compose", "-p", "failover-itest", "down", "-v"); err != nil {
		t.Fatal(err)
	}
	if c, v := count("ps", "-a"), count("volume", "ls"); c != 0 || v != 0 {
		t.Fatalf("sobraram %d containers e %d volumes", c, v)
	}
}
