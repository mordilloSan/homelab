package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A new install (no state yet) asks for the first-start guide until it is
// finished; an existing one, upgraded, never does.
func TestSetupGuide(t *testing.T) {
	a, _ := setup(t)
	if v := string(a.view.Load().raw); !strings.Contains(v, `"setup_pending":true`) {
		t.Fatal("uma instalação nova não pede o assistente")
	}
	if code, _ := postTo(t, a.postSetupDone, `{}`); code != 204 {
		t.Fatalf("HTTP %d", code)
	}
	if v := string(a.view.Load().raw); !strings.Contains(v, `"setup_pending":false`) {
		t.Fatal("concluído, mas continua pendente")
	}
	if b, _ := os.ReadFile(a.statePath); strings.Contains(string(b), "setup_pending") {
		t.Fatalf("não ficou gravado: %s", b)
	}

	dir := t.TempDir()
	writeLines(t, filepath.Join(dir, "state.json"), `{"router_ok":true,"services":{}}`)
	old := newTestAgent(t, dir, &fake{noPing: map[string]bool{}, down: map[string]bool{}})
	if v := string(old.view.Load().raw); !strings.Contains(v, `"setup_pending":false`) {
		t.Fatal("uma instalação que já existia pede o assistente")
	}
}

// Left for later and the agent restarted: the guide is still pending.
func TestSetupGuideSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	f := &fake{noPing: map[string]bool{}, down: map[string]bool{}}
	a := newTestAgent(t, dir, f)
	a.save()
	again := newTestAgent(t, dir, f)
	if v := string(again.view.Load().raw); !strings.Contains(v, `"setup_pending":true`) {
		t.Fatal("depois de reiniciar, o assistente desapareceu")
	}
}
