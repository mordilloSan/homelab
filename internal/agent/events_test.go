package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func evLine(t time.Time, msg string) string {
	b, _ := json.Marshal(Event{T: t, Msg: msg})
	return string(b)
}

// Events survive a restart; a line cut short by a crash is skipped, and on
// start the file keeps only the last 30 days.
func TestEventsPersistAndTrim(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	writeLines(t, filepath.Join(dir, "events.jsonl"),
		evLine(now.Add(-40*24*time.Hour), "velho"),
		`{"t":"2026-09-2`, // cut short
		evLine(now.Add(-time.Hour), "recente"))
	a := newTestAgent(t, dir, &fake{noPing: map[string]bool{}, down: map[string]bool{}})
	if len(a.events) != 1 || a.events[0].Msg != "recente" {
		t.Fatalf("eventos lidos: %+v", a.events)
	}
	a.event("", "novo")
	b, _ := loadEvents(a.eventsPath)
	if len(b) != 2 || b[1].Msg != "novo" {
		t.Fatalf("ficheiro depois de aparar e acrescentar: %+v", b)
	}
	again := newTestAgent(t, dir, &fake{noPing: map[string]bool{}, down: map[string]bool{}})
	if len(again.events) != 2 {
		t.Fatalf("depois de reiniciar: %+v", again.events)
	}
}

// At most maxEvents stay, in memory and, after a trim, in the file.
func TestEventsCap(t *testing.T) {
	a, _ := setup(t)
	for range maxEvents + 3 {
		a.event("", "e")
	}
	if len(a.events) != maxEvents {
		t.Fatalf("em memória: %d", len(a.events))
	}
	a.trimEvents()
	if b, _ := loadEvents(a.eventsPath); len(b) != maxEvents {
		t.Fatalf("no ficheiro: %d", len(b))
	}
}

// A file that cannot be written does not stop the agent: the event stays in memory.
func TestEventsUnwritable(t *testing.T) {
	a, _ := setup(t)
	a.eventsPath = filepath.Join(t.TempDir(), "nao-existe", "events.jsonl")
	a.event("vaultwarden", "sem disco")
	a.trimEvents()
	if !hasEvent(a, "sem disco") {
		t.Fatal("o evento perdeu-se")
	}
}

// The status carries only the most recent events.
func TestStatusEvents(t *testing.T) {
	a, _ := setup(t)
	for range statusEvents + 10 {
		a.event("", "x")
	}
	a.publish()
	var v struct{ Events []Event }
	if err := json.Unmarshal(*a.view.Load(), &v); err != nil || len(v.Events) != statusEvents {
		t.Fatalf("eventos no estado: %d (%v)", len(v.Events), err)
	}
}

// A line too long to read does not stop the agent from starting: the events
// before it are kept.
func TestEventsTooLongLine(t *testing.T) {
	dir := t.TempDir()
	writeLines(t, filepath.Join(dir, "events.jsonl"),
		evLine(time.Now().Add(-time.Hour), "antes"),
		`{"t":"`+strings.Repeat("x", 2<<20)+`"}`)
	a := newTestAgent(t, dir, &fake{noPing: map[string]bool{}, down: map[string]bool{}})
	if len(a.events) != 1 || a.events[0].Msg != "antes" {
		t.Fatalf("eventos: %+v", a.events)
	}
}
