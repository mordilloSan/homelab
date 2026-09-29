package agent

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// The watchdog runs apart from the checks and never takes mu: a check stuck
// for too long, or the page not answering, is emailed and ends the process,
// so Docker starts it again. A start after an end that was not asked for is
// emailed too (State.Running).

// watchCfg is what the watchdog needs, published under mu (publish).
type watchCfg struct {
	email    EmailConfig
	interval time.Duration
	ui       string
}

const healthFails = 3

// progress counts each command that ends as progress: a check running slow
// commands one after another is moving, and each has its own timeout.
type progress struct {
	System
	a *Agent
}

func (p progress) Run(name string, args ...string) error {
	defer p.a.beat()
	return p.System.Run(name, args...)
}

func (p progress) Output(name string, args ...string) (string, error) {
	defer p.a.beat()
	return p.System.Output(name, args...)
}

func (a *Agent) beat() { a.lastBeat.Store(int64(time.Since(a.started))) }

// Watchdog checks every minute until ctx ends; health asks the page for /healthz.
func (a *Agent) Watchdog(ctx context.Context, health func() error) {
	fails := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}
		if why := a.watch(health, &fails, time.Since(a.started)); why != "" {
			a.die(why)
			return
		}
	}
}

// watch says why the agent should end, or "". up is the time since started:
// monotonic, so a clock set by NTP after the start is no stall.
func (a *Agent) watch(health func() error, fails *int, up time.Duration) string {
	w := a.watchCfg.Load()
	if idle := up - time.Duration(a.lastBeat.Load()); idle > 3*w.interval+15*time.Minute {
		return fmt.Sprintf("a verificação está parada há %d min", int(idle.Minutes()))
	}
	if err := health(); err != nil {
		if *fails++; *fails >= healthFails {
			return fmt.Sprintf("a interface não responde (%d vezes): %v", *fails, err)
		}
		return ""
	}
	*fails = 0
	return ""
}

func (a *Agent) die(why string) {
	w := a.watchCfg.Load()
	slog.Error("o agente vai terminar para o Docker o arrancar de novo", "motivo", why)
	if w.email.on() {
		body := why + "\n\nO agente termina para o Docker o arrancar de novo.\n"
		if w.ui != "" {
			body += "\nInterface: " + w.ui + "\n"
		}
		if err := a.sys.SendMail(w.email.mail("[Failover] O agente vai reiniciar: "+why, body)); err != nil {
			slog.Error("enviar o email do watchdog", "error", err)
		}
	}
	a.exit(1)
}

// crashed alerts, at the start, that the last run ended without being asked to.
func (a *Agent) crashed() {
	msg := "o agente reiniciou depois de parar sem ser pedido"
	a.evMu.Lock()
	if n := len(a.events); n > 0 {
		e := a.events[n-1]
		msg += fmt.Sprintf("; último evento, %s: %s", e.T.Local().Format("02/01 15:04"), e.Msg)
	}
	a.evMu.Unlock()
	a.alert("", msg)
}
