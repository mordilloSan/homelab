// failover-agent moves homelab services from the server to the TNAS when they
// fail there, and back when they recover. See README.md.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
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
	if flag.Arg(0) == "hash" { // failover-agent hash < password → bcrypt hash for ui.password_hash
		pw, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		pw = strings.TrimRight(pw, "\r\n")
		if len(pw) < 12 {
			log.Fatal("password com menos de 12 caracteres")
		}
		h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(string(h))
		return
	}

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		log.Fatal(err)
	}
	if cfg.UI.PasswordHash == "" {
		log.Fatal("ui.password_hash vazio: gera um com `failover-agent hash`")
	}
	a, err := NewAgent(cfg, *cfgPath, *statePath, realSys{})
	if err != nil {
		log.Fatal(err)
	}
	srv := &http.Server{Addr: cfg.UI.Listen, Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { log.Fatal(srv.ListenAndServe()) }()
	log.Printf("failover-agent %s em modo %s, interface em %s", version, cfg.Mode, cfg.UI.Listen)
	a.Run()
}
