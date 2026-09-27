// failover-agent moves homelab services from the server to the TNAS when they
// fail there, and back when they recover. See README.md.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// version is set at build time (-X main.version=v1.2.3) by the release workflow.
var version = "dev"

func main() {
	cfgPath := flag.String("config", "/config/failover.yml", "configuration file")
	statePath := flag.String("state", "/state/state.json", "persistent state file")
	flag.Parse()

	if flag.Arg(0) == "version" {
		fmt.Println(version)
		return
	}

	if err := firstRun(*cfgPath); err != nil {
		log.Fatal(err)
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		log.Fatal(err)
	}
	writeOverrides(&cfg)
	_ = os.MkdirAll(filepath.Dir(*statePath), 0o755)
	a, err := NewAgent(cfg, *cfgPath, *statePath, realSys{})
	if err != nil {
		log.Fatal(err)
	}
	srv := &http.Server{Addr: cfg.UI.Listen, Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { log.Fatal(srv.ListenAndServe()) }()
	log.Printf("failover-agent %s em modo %s, interface em %s", version, cfg.Mode, cfg.UI.Listen)
	a.Run()
}
