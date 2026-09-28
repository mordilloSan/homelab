// failover-agent moves homelab services from the server to the TNAS when they
// fail there, and back when they recover. See README.md.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mordilloSan/homelab/internal/agent"
)

// version is set at build time (-X main.version=v1.2.3) by the release workflow.
var version = "dev"

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// run returns the exit code: 0 ok, 1 failed, 2 bad usage.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	cmd := "run" // no command: the image's ENTRYPOINT starts the agent
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("failover-agent "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		writeUsage(stderr)
		fs.PrintDefaults()
	}
	cfgPath := fs.String("config", "/config/failover.yml", "configuration file")
	statePath := fs.String("state", "/state/state.json", "persistent state file")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "failover-agent: argumento a mais: %q\n", fs.Arg(0))
		return 2
	}

	var err error
	switch cmd {
	case "run":
		err = serve(ctx, *cfgPath, *statePath)
	case "healthcheck":
		err = healthcheck(*cfgPath)
	case "version":
		fmt.Fprintln(stdout, version)
	case "help":
		writeUsage(stdout)
	default:
		fmt.Fprintf(stderr, "failover-agent: comando desconhecido %q\n", cmd)
		writeUsage(stderr)
		return 2
	}
	if err != nil {
		fmt.Fprintf(stderr, "failover-agent: %s: %v\n", cmd, err)
		return 1
	}
	return 0
}

func writeUsage(w io.Writer) {
	fmt.Fprintln(w, `Uso: failover-agent [run|healthcheck|version|help] [-config FICHEIRO] [-state FICHEIRO]

  run          arranca o agente (sem comando, é este)
  healthcheck  pergunta à interface por /healthz (HEALTHCHECK da imagem)
  version      mostra a versão`)
}

func healthcheck(cfgPath string) error {
	cfg, err := agent.LoadConfig(cfgPath)
	if err != nil {
		return err
	}
	return agent.Healthcheck(cfg.UI.Listen, cfg.UI.TLSCert)
}

// serve runs the agent until ctx ends. A signal stops it between ticks, never
// in the middle of a failover or a return.
func serve(ctx context.Context, cfgPath, statePath string) error {
	if err := agent.FirstRun(cfgPath); err != nil {
		return fmt.Errorf("criar a configuração: %w", err)
	}
	cfg, err := agent.LoadConfig(cfgPath)
	if err != nil {
		return err
	}
	agent.WriteOverrides(&cfg)
	if err = os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		return fmt.Errorf("pasta do estado: %w", err)
	}
	agent.Version = version
	a, err := agent.NewAgent(cfg, cfgPath, statePath, agent.RealSys{})
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", cfg.UI.Listen)
	if err != nil {
		return fmt.Errorf("interface: %w", err)
	}
	https := cfg.UI.TLSCert != ""
	srv := &http.Server{Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	if https {
		if srv.TLSConfig, err = agent.UITLS(cfg.UI.TLSCert, cfg.UI.TLSKey); err != nil {
			_ = ln.Close()
			return err
		}
		ln = tls.NewListener(ln, srv.TLSConfig)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	serveErr := make(chan error, 1)
	go func() {
		if serr := srv.Serve(ln); !errors.Is(serr, http.ErrServerClosed) {
			serveErr <- serr
			cancel() // without the UI, stop the agent too
		}
	}()

	ui, err := agent.UIURL(cfg.UI.Listen, cfg.TNASIP, https)
	if err != nil {
		ui = cfg.UI.Listen
	}
	slog.Info("failover-agent a correr", "version", version, "mode", cfg.Mode, "ui", ui)
	a.Preflight()
	a.Run(ctx)

	shutdownCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("parar a interface", "error", err)
	}
	select {
	case err := <-serveErr:
		return fmt.Errorf("interface: %w", err)
	default:
		slog.Info("failover-agent parado")
		return nil
	}
}
