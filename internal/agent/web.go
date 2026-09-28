package agent

import (
	"crypto/tls"
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

//go:embed web/index.html
var indexHTML []byte

// Inter, the LinuxIO typeface (SIL OFL 1.1), embedded so the page needs no internet.
//
//go:embed web/inter.woff2
var interFont []byte

func (a *Agent) Handler() http.Handler {
	mux := http.NewServeMux() // everything here needs a session
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(indexHTML)
	})
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(*a.view.Load())
	})
	mux.HandleFunc("POST /api/config", a.postConfig)
	mux.HandleFunc("POST /api/maintenance", a.postMaintenance)
	mux.HandleFunc("POST /api/action", a.postAction)
	mux.HandleFunc("POST /api/password", a.postPassword)
	mux.HandleFunc("POST /api/dns", a.postDNS)
	mux.HandleFunc("POST /api/images", func(w http.ResponseWriter, _ *http.Request) {
		go a.scanImages()
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("POST /api/logout", a.postLogout)

	root := http.NewServeMux() // open: the login and what it shows
	root.HandleFunc("GET /login", a.getLogin)
	root.HandleFunc("POST /login", a.postLogin)
	root.HandleFunc("GET /inter.woff2", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "font/woff2")
		w.Header().Set("Cache-Control", "private, max-age=604800")
		_, _ = w.Write(interFont)
	})
	root.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	root.Handle("/", a.auth(mux))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		root.ServeHTTP(w, r)
	})
}

// UIURL is the address to open the UI at: listen (ui.listen) with host in
// place of an unspecified address like 0.0.0.0, https when the UI has TLS.
func UIURL(listen, host string, https bool) (string, error) {
	h, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", err
	}
	if ip := net.ParseIP(h); h == "" || ip != nil && ip.IsUnspecified() {
		h = host
	}
	scheme := "http://"
	if https {
		scheme = "https://"
	}
	return scheme + net.JoinHostPort(h, port), nil
}

// UITLS serves the certificate in certFile/keyFile, read again on every
// handshake so a renewed certificate is picked up without a restart.
// ponytail: two file reads per handshake, fine for one admin's browser; cache by mtime if that ever shows.
func UITLS(certFile, keyFile string) (*tls.Config, error) {
	if _, err := tls.LoadX509KeyPair(certFile, keyFile); err != nil {
		return nil, fmt.Errorf("certificado da interface: %w", err)
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		c, err := tls.LoadX509KeyPair(certFile, keyFile)
		return &c, err
	}}, nil
}

// Healthcheck asks the UI at listen for /healthz, on loopback when it listens
// everywhere; the image's HEALTHCHECK runs it as "failover-agent healthcheck".
func Healthcheck(listen string, https bool) error {
	u, err := UIURL(listen, "127.0.0.1", https)
	if err != nil {
		return err
	}
	c := http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		// Loopback: the certificate names the UI's hostname, not 127.0.0.1.
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // G402: loopback only
	}}
	resp, err := c.Get(u + "/healthz")
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		http.Error(w, "pedido inválido: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

// done persists a UI change and wakes the loop so it applies without waiting.
func (a *Agent) done(w http.ResponseWriter, svc, msg string) {
	a.event(svc, msg+" (interface)")
	a.save()
	a.poke()
	w.WriteHeader(http.StatusNoContent)
}

func (a *Agent) postConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode           string `json:"mode"`
		CheckIntervalS int    `json:"check_interval_s"`
		Services       []struct {
			Name         string `json:"name"`
			WaitMin      int    `json:"wait_min"`
			StabilityMin int    `json:"stability_min"`
		} `json:"services"`
	}
	if !decode(w, r, &req) {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	next := a.cfg
	next.Services = slices.Clone(a.cfg.Services)
	next.Mode, next.CheckIntervalS = req.Mode, req.CheckIntervalS
	for _, rs := range req.Services {
		i := slices.IndexFunc(next.Services, func(s Service) bool { return s.Name == rs.Name })
		if i < 0 {
			http.Error(w, "serviço desconhecido: "+rs.Name, http.StatusBadRequest)
			return
		}
		next.Services[i].WaitMin, next.Services[i].StabilityMin = rs.WaitMin, rs.StabilityMin
	}
	if err := next.validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := saveConfig(a.cfgPath, &next); err != nil {
		http.Error(w, "guardar configuração: "+err.Error(), http.StatusInternalServerError)
		return
	}
	a.cfg = next
	a.now = time.Now()
	a.done(w, "", "configuração alterada")
}

func (a *Agent) postMaintenance(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Service string `json:"service"` // empty = global
		Minutes int    `json:"minutes"` // 0 = off
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Minutes < 0 || req.Minutes > 7*24*60 {
		http.Error(w, "minutos entre 0 e 10080", http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.now = time.Now()
	until := &a.st.MaintUntil
	if req.Service != "" {
		if _, ok := a.service(req.Service); !ok {
			http.Error(w, "serviço desconhecido", http.StatusNotFound)
			return
		}
		until = &a.svc(req.Service).MaintUntil
	}
	*until = time.Time{}
	msg := "manutenção desligada"
	if req.Minutes > 0 {
		*until = a.now.Add(minutes(req.Minutes))
		msg = "manutenção ligada até " + until.Local().Format("02/01 15:04")
	}
	a.done(w, req.Service, msg)
}

func (a *Agent) postAction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Service string `json:"service"`
		Action  string `json:"action"` // failover | return
	}
	if !decode(w, r, &req) {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.service(req.Service); !ok {
		http.Error(w, "serviço desconhecido", http.StatusNotFound)
		return
	}
	a.now = time.Now()
	s := a.svc(req.Service)
	switch {
	// From ERROR only once the cleanup finished, otherwise return first.
	case req.Action == "failover" && (s.State == Normal || s.State == Error && s.Snapshot == "" && !s.DNS):
		a.set(s, FailingOver)
		a.done(w, req.Service, "failover forçado")
	case req.Action == "return" && (s.State == FailingOver || s.State == Active || s.State == Error):
		a.set(s, Returning)
		a.done(w, req.Service, "regresso forçado")
	default:
		http.Error(w, "ação impossível no estado "+s.State, http.StatusConflict)
	}
}

func (a *Agent) postPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !decode(w, r, &req) {
		return
	}
	c := a.creds.Load()
	if bcrypt.CompareHashAndPassword([]byte(c.PasswordHash), []byte(req.Current)) != nil {
		http.Error(w, "a password atual está errada", http.StatusForbidden)
		return
	}
	if len(req.New) < 8 || len(req.New) > 72 { // bcrypt ignores anything past 72 bytes
		http.Error(w, "a nova password tem de ter entre 8 e 72 caracteres (os acentos contam a dobrar)", http.StatusBadRequest)
		return
	}
	n, err := newCreds(c.User, req.New)
	if err == nil {
		err = saveUser(a.userPath, n)
	}
	if err != nil {
		http.Error(w, "guardar a password: "+err.Error(), http.StatusInternalServerError)
		return
	}
	a.creds.Store(n)
	a.sessions.keepOnly(token(r)) // whoever knew the old password is logged out
	a.mu.Lock()
	defer a.mu.Unlock()
	a.now = time.Now()
	a.done(w, "", "password da interface alterada")
}

// postDNS replaces the Technitium token. It is tested first, so a wrong one
// is found here and not in the middle of a failover.
func (a *Agent) postDNS(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if !decode(w, r, &req) {
		return
	}
	tok := strings.TrimSpace(req.Token)
	if tok == "" {
		http.Error(w, "falta o token do Technitium", http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	d := a.cfg.DNS
	a.mu.Unlock()
	// outside the lock: the Technitium may take seconds to answer
	if err := testToken(a.sys, d.APIURL, d.Zone, tok); err != nil {
		http.Error(w, "o Technitium recusou o token: "+err.Error(), http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := writeAtomic(d.TokenFile, []byte(tok+"\n")); err != nil {
		http.Error(w, "guardar o token: "+err.Error(), http.StatusInternalServerError)
		return
	}
	a.now = time.Now()
	a.done(w, "", "token do Technitium alterado")
}
