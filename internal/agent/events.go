package agent

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// Events live in events.jsonl next to state.json, one JSON line each, so a
// month of them is not rewritten with the state on every tick.
const (
	maxEvents    = 5000
	eventMaxAge  = 30 * 24 * time.Hour
	statusEvents = 50 // the most recent ones, sent with every status
)

func eventsPath(statePath string) string {
	return filepath.Join(filepath.Dir(statePath), "events.jsonl")
}

// loadEvents reads the file; a line that does not parse (a write cut short) is skipped.
func loadEvents(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var evs []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var e Event
		if json.Unmarshal(sc.Bytes(), &e) == nil && !e.T.IsZero() {
			evs = append(evs, e)
		}
	}
	return evs, sc.Err()
}

// event records msg in the log, in memory and at the end of the file. A
// failed write only goes to the log: the agent must not stop over it.
func (a *Agent) event(svc, msg string) {
	slog.Info(msg, "svc", cmp.Or(svc, "global"))
	e := Event{T: a.now, Svc: svc, Msg: msg}
	a.evMu.Lock()
	a.events = append(a.events, e)
	if n := len(a.events); n > maxEvents {
		a.events = slices.Clone(a.events[n-maxEvents:])
	}
	a.evMu.Unlock()
	b, _ := json.Marshal(e)
	f, err := os.OpenFile(a.eventsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err == nil {
		_, err = f.Write(append(b, '\n'))
		err = cmp.Or(err, f.Close())
	}
	if err != nil {
		slog.Error("guardar evento", "error", err)
	}
}

// trimEvents keeps the last 30 days, at most maxEvents, and rewrites the file
// with them. It runs at start and on the first tick of each day.
func (a *Agent) trimEvents() {
	a.trimmedOn = a.now.Format(time.DateOnly)
	cut := a.now.Add(-eventMaxAge)
	a.evMu.Lock()
	i := slices.IndexFunc(a.events, func(e Event) bool { return !e.T.Before(cut) })
	if i < 0 {
		i = len(a.events)
	}
	a.events = slices.Clone(a.events[max(i, len(a.events)-maxEvents):])
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, e := range a.events {
		_ = enc.Encode(e)
	}
	a.evMu.Unlock()
	if err := writeAtomic(a.eventsPath, buf.Bytes()); err != nil {
		slog.Error("guardar eventos", "error", err)
	}
}

// eventsSnapshot is every event in memory, newest first. It takes only evMu,
// so the page gets it even while a tick holds mu through a compose up.
func (a *Agent) eventsSnapshot() []Event {
	a.evMu.Lock()
	evs := slices.Clone(a.events)
	a.evMu.Unlock()
	slices.Reverse(evs)
	return evs
}
