package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestWatchdog(t *testing.T) {
	a, f := setup(t)
	code := -1
	a.exit = func(c int) { code = c }
	ok := func() error { return nil }
	fails, now := 0, time.Since(a.started)

	if why := a.watch(ok, &fails, now); why != "" {
		t.Fatalf("parou com o agente bem: %s", why)
	}
	down := func() error { return errors.New("recusada") }
	for i := range healthFails - 1 {
		if why := a.watch(down, &fails, now); why != "" {
			t.Fatalf("parou à falha %d do /healthz: %s", i+1, why)
		}
	}
	if a.watch(ok, &fails, now); fails != 0 {
		t.Fatal("uma resposta boa não repôs as falhas")
	}
	for range healthFails - 1 {
		a.watch(down, &fails, now)
	}
	if why := a.watch(down, &fails, now); why == "" {
		t.Fatal("o /healthz falhou 3 vezes e o watchdog não parou")
	}

	fails = 0
	if why := a.watch(ok, &fails, now+3*a.watchCfg.Load().interval+16*time.Minute); why == "" {
		t.Fatal("verificação parada e o watchdog não parou")
	} else {
		a.die(why)
	}
	if code != 1 || !mailed(f, "O agente vai reiniciar") {
		t.Fatalf("saída %d, emails %+v", code, f.mails)
	}
}

// Run marks the state while it runs; a start that finds the mark alerts.
func TestCrashedAlerts(t *testing.T) {
	dir := t.TempDir()
	f := &fake{noPing: map[string]bool{}, down: map[string]bool{}}
	a := newTestAgent(t, dir, f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a.Run(ctx)
	if a.st.Running {
		t.Fatal("Run parado a pedido deixou a marca")
	}
	if b := newTestAgent(t, dir, f); len(b.alerts) != 0 {
		t.Fatalf("aviso depois de uma paragem pedida: %+v", b.alerts)
	}

	a.mu.Lock()
	a.st.Running = true
	a.save()
	a.mu.Unlock()
	b := newTestAgent(t, dir, f)
	if len(b.alerts) != 1 || b.alerts[0].Msg[:len("o agente reiniciou")] != "o agente reiniciou" {
		t.Fatalf("sem aviso depois de uma falha: %+v", b.alerts)
	}
	b.Tick(time.Now())
	b.mailJobs.Wait()
	if !mailed(f, "o agente reiniciou") {
		t.Fatalf("sem email: %+v", f.mails)
	}
	b.mu.Lock()
	b.st.Running = true
	b.save()
	b.mu.Unlock()
	if c := newTestAgent(t, dir, f); strings.Count(c.alerts[0].Msg, "o agente reiniciou") != 1 {
		t.Fatalf("dois reinícios seguidos encaixaram a mensagem: %s", c.alerts[0].Msg)
	}
}
