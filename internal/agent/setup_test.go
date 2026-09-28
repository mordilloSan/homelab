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
	if v := string(*a.view.Load()); !strings.Contains(v, `"setup_pending":true`) {
		t.Fatal("uma instalação nova não pede o assistente")
	}
	if code, _ := postTo(t, a.postSetupDone, `{}`); code != 204 {
		t.Fatalf("HTTP %d", code)
	}
	if v := string(*a.view.Load()); !strings.Contains(v, `"setup_pending":false`) {
		t.Fatal("concluído, mas continua pendente")
	}
	if b, _ := os.ReadFile(a.statePath); !strings.Contains(string(b), `"setup_done": true`) {
		t.Fatalf("não ficou gravado: %s", b)
	}

	dir := t.TempDir()
	writeLines(t, filepath.Join(dir, "state.json"), `{"router_ok":true,"services":{}}`)
	old := newTestAgent(t, dir, &fake{noPing: map[string]bool{}, down: map[string]bool{}})
	if v := string(*old.view.Load()); !strings.Contains(v, `"setup_pending":false`) {
		t.Fatal("uma instalação que já existia pede o assistente")
	}
}
