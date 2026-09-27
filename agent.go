package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.yaml.in/yaml/v3"
)

type Service struct {
	Name          string `yaml:"name" json:"name"`
	Dir           string `yaml:"dir" json:"dir"`
	Host          string `yaml:"host" json:"host"`
	WaitMin       int    `yaml:"wait_min" json:"wait_min"`
	StabilityMin  int    `yaml:"stability_min" json:"stability_min"`
	Override      string `yaml:"override,omitempty" json:"override,omitempty"`
	RequireFreeIP string `yaml:"require_free_ip,omitempty" json:"require_free_ip,omitempty"`
}

type Config struct {
	Mode   string `yaml:"mode"` // observe: only logs what it would do; auto: acts
	Server struct {
		IP           string `yaml:"ip"`
		NPMCheckHost string `yaml:"npm_check_host"`
	} `yaml:"server"`
	TNASIP          string `yaml:"tnas_ip"`
	RouterIP        string `yaml:"router_ip"`
	LANIface        string `yaml:"lan_iface"`
	CheckIntervalS  int    `yaml:"check_interval_s"`
	StartTimeoutMin int    `yaml:"start_timeout_min"`
	Paths           struct {
		MirrorSubvol string `yaml:"mirror_subvol"`
		MirrorRoot   string `yaml:"mirror_root"`
		SnapshotsDir string `yaml:"snapshots_dir"`
		OverridesDir string `yaml:"overrides_dir"`
	} `yaml:"paths"`
	NPM struct {
		Dir           string `yaml:"dir"`
		AlertAfterMin int    `yaml:"alert_after_min"`
	} `yaml:"npm"`
	Services    []Service `yaml:"services"`
	Maintenance struct {
		DefaultExpiryMin int `yaml:"default_expiry_min"`
	} `yaml:"maintenance"`
	DNS struct {
		Enabled   bool   `yaml:"enabled"`
		APIURL    string `yaml:"api_url"`
		TokenFile string `yaml:"token_file"`
		Zone      string `yaml:"zone"`
		TTL       int    `yaml:"ttl"`
	} `yaml:"dns"`
	Kuma struct {
		BaseURL        string            `yaml:"base_url"`
		HeartbeatToken string            `yaml:"heartbeat_token"`
		NPMToken       string            `yaml:"npm_token"`
		ServiceTokens  map[string]string `yaml:"service_tokens"`
	} `yaml:"kuma"`
	Nightly struct {
		PrepullAt string `yaml:"prepull_at"`
	} `yaml:"nightly"`
	UI struct {
		Listen       string `yaml:"listen"`
		PasswordHash string `yaml:"password_hash"`
	} `yaml:"ui"`
}

// Service names end up in compose project names and snapshot paths.
var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

func (c *Config) validate() error {
	switch {
	case c.Mode != "observe" && c.Mode != "auto":
		return errors.New("mode tem de ser observe ou auto")
	case c.CheckIntervalS < 10:
		return errors.New("check_interval_s tem de ser >= 10")
	case c.StartTimeoutMin < 1 || c.NPM.AlertAfterMin < 0:
		return errors.New("start_timeout_min tem de ser >= 1 e npm.alert_after_min >= 0")
	case c.Server.IP == "" || c.Server.NPMCheckHost == "" || c.TNASIP == "" || c.RouterIP == "":
		return errors.New("server.ip, server.npm_check_host, tnas_ip e router_ip são obrigatórios")
	case c.Paths.MirrorSubvol == "" || c.Paths.SnapshotsDir == "" || c.NPM.Dir == "":
		return errors.New("paths.mirror_subvol, paths.snapshots_dir e npm.dir são obrigatórios")
	case c.DNS.Enabled && (c.DNS.APIURL == "" || c.DNS.TokenFile == "" || c.DNS.Zone == ""):
		return errors.New("dns.api_url, dns.token_file e dns.zone são obrigatórios com dns.enabled")
	}
	if at := c.Nightly.PrepullAt; at != "" {
		if _, err := time.Parse("15:04", at); err != nil || len(at) != 5 {
			return errors.New("nightly.prepull_at tem de ser HH:MM")
		}
	}
	seen := map[string]bool{"npm": true}
	for _, s := range c.Services {
		if !validName.MatchString(s.Name) || seen[s.Name] || s.Dir == "" || s.Host == "" {
			return fmt.Errorf("serviço inválido ou repetido: %q", s.Name)
		}
		if s.WaitMin < 1 || s.StabilityMin < 0 {
			return fmt.Errorf("%s: wait_min tem de ser >= 1 e stability_min >= 0", s.Name)
		}
		if s.RequireFreeIP != "" && c.LANIface == "" {
			return fmt.Errorf("%s: require_free_ip precisa de lan_iface", s.Name)
		}
		seen[s.Name] = true
	}
	return nil
}

func loadConfig(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	d := yaml.NewDecoder(bytes.NewReader(b))
	d.KnownFields(true) // a typo in the config must not be silently ignored
	if err := d.Decode(&c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	return c, c.validate()
}

func saveConfig(path string, c *Config) error {
	var buf bytes.Buffer
	e := yaml.NewEncoder(&buf)
	e.SetIndent(2)
	if err := e.Encode(c); err != nil {
		return err
	}
	return writeAtomic(path, buf.Bytes())
}

func writeAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

const (
	Normal      = "NORMAL"
	FailingOver = "FAILING_OVER"
	Active      = "ACTIVE"
	Returning   = "RETURNING"
	Error       = "ERROR"
)

type SvcState struct {
	State      string    `json:"state"`
	Since      time.Time `json:"since,omitzero"`
	ServerOK   bool      `json:"server_ok"`
	FailSince  time.Time `json:"fail_since,omitzero"`
	OKSince    time.Time `json:"ok_since,omitzero"`
	Snapshot   string    `json:"snapshot,omitempty"`
	DNS        bool      `json:"dns,omitempty"` // A record added, must be removed on return
	Msg        string    `json:"msg,omitempty"`
	MaintUntil time.Time `json:"maint_until,omitzero"`
	Observed   bool      `json:"observed,omitempty"`
}

type Event struct {
	T   time.Time `json:"t"`
	Svc string    `json:"svc,omitempty"`
	Msg string    `json:"msg"`
}

type State struct {
	RouterOK     bool      `json:"router_ok"`
	TNASUp       bool      `json:"tnas_up"`   // its LAN IP answers; with RouterOK, it is on the LAN
	ServerUp     bool      `json:"server_up"` // NPM or ping answered; meaningless without the router
	ServerNPMOK  bool      `json:"server_npm_ok"`
	NPMFailSince time.Time `json:"npm_fail_since,omitzero"`
	NPMAlerted   bool      `json:"npm_alerted,omitempty"`
	TNASNPM      struct {
		Snapshot string    `json:"snapshot,omitempty"`
		Since    time.Time `json:"since,omitzero"`
		OK       bool      `json:"ok"`
		Msg      string    `json:"msg,omitempty"`
	} `json:"tnas_npm"`
	MaintUntil time.Time            `json:"maint_until,omitzero"`
	LastPull   string               `json:"last_pull,omitempty"`
	Images     map[string]Stack     `json:"images,omitempty"` // by service; "npm" is the TNAS NPM
	ImagesAt   time.Time            `json:"images_at,omitzero"`
	ImagesMsg  string               `json:"images_msg,omitempty"`
	Services   map[string]*SvcState `json:"services"`
	Events     []Event              `json:"events"`
}

const maxEvents = 200

// System is every side effect the agent has, so tests can replace it.
type System interface {
	Run(name string, args ...string) error              // docker, btrfs, arping, ping
	Output(name string, args ...string) (string, error) // same, when stdout matters (image scan)
	Check(host, ip string) error                        // https://host with the connection sent to ip (curl --resolve)
	Get(url, bearer string) ([]byte, error)
}

type Agent struct {
	// One lock for config and state. The network probes of a tick run
	// outside it; the actions (compose up/down, btrfs) run inside, so a UI
	// write waits only while a failover or a return is being carried out.
	// Reads use view and never wait.
	mu        sync.Mutex
	cfg       Config
	st        State
	sys       System
	now       time.Time // time of the operation in progress
	cfgPath   string
	statePath string
	wake      chan struct{}
	view      atomic.Pointer[[]byte]
	pulling   atomic.Bool
	scanning  atomic.Bool
	pushErr   string
	beats     map[string][]Beat
	tnasSeen  bool // last TNAS ping, so a change is logged once
}

func NewAgent(cfg Config, cfgPath, statePath string, sys System) (*Agent, error) {
	a := &Agent{cfg: cfg, cfgPath: cfgPath, statePath: statePath, sys: sys, wake: make(chan struct{}, 1), now: time.Now(), beats: map[string][]Beat{}, tnasSeen: true}
	a.st.RouterOK = true
	b, err := os.ReadFile(statePath)
	switch {
	case err == nil:
		// A corrupt state would forget running failovers: refuse to start.
		if err := json.Unmarshal(b, &a.st); err != nil {
			return nil, fmt.Errorf("estado %s corrompido: %w", statePath, err)
		}
	case !errors.Is(err, fs.ErrNotExist):
		return nil, err
	}
	if a.st.Services == nil {
		a.st.Services = map[string]*SvcState{}
	}
	a.publish()
	return a, nil
}

func (a *Agent) Run() {
	for {
		a.mu.Lock()
		stale := time.Since(a.st.ImagesAt) > time.Hour
		a.mu.Unlock()
		if stale {
			go a.scanImages()
		}
		a.Tick(time.Now())
		a.mu.Lock()
		d := time.Duration(a.cfg.CheckIntervalS) * time.Second
		a.mu.Unlock()
		select {
		case <-time.After(d):
		case <-a.wake:
		}
	}
}

func (a *Agent) poke() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

func (a *Agent) Tick(now time.Time) {
	p := a.probe()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.now = now
	a.evaluate(p)
	a.stopIdleNPM()
	a.report()
	a.nightly()
	a.save()
}

func minutes(n int) time.Duration { return time.Duration(n) * time.Minute }

func (a *Agent) svc(name string) *SvcState {
	s := a.st.Services[name]
	if s == nil {
		s = &SvcState{State: Normal}
		a.st.Services[name] = s
	}
	return s
}

func (a *Agent) service(name string) (Service, bool) {
	i := slices.IndexFunc(a.cfg.Services, func(s Service) bool { return s.Name == name })
	if i < 0 {
		return Service{}, false
	}
	return a.cfg.Services[i], true
}

func (a *Agent) event(svc, msg string) {
	log.Printf("[%s] %s", cmp.Or(svc, "global"), msg)
	a.st.Events = append(a.st.Events, Event{T: a.now, Svc: svc, Msg: msg})
	if n := len(a.st.Events); n > maxEvents {
		a.st.Events = slices.Clone(a.st.Events[n-maxEvents:])
	}
}

// warn records a repeating problem once, not every tick.
func (a *Agent) warn(msg *string, svc, m string) {
	if *msg != m {
		*msg = m
		a.event(svc, m)
	}
}

func (a *Agent) ping(ip string) bool {
	return a.sys.Run("ping", "-c", "1", "-W", "2", ip) == nil
}

func (a *Agent) inMaint(s *SvcState) bool {
	return !a.st.MaintUntil.IsZero() || !s.MaintUntil.IsZero()
}

// probe is everything a tick asks the network. It runs without the lock:
// with the server down it spends seconds on timeouts.
type probe struct {
	routerOK, npmOK, serverPing, tnasNPMOK bool
	tnasPing                               bool // its own LAN IP is up (answered locally)
	npmMs                                  int
	svcOK                                  map[string]bool
	svcMs                                  map[string]int
}

func (a *Agent) probe() probe {
	a.mu.Lock()
	c := a.cfg // the UI replaces a.cfg and never edits it in place, so this copy is safe to read
	tnasNPM := a.st.TNASNPM.Snapshot != ""
	a.mu.Unlock()

	timed := func(host, ip string) (bool, int) {
		t := time.Now()
		err := a.sys.Check(host, ip)
		return err == nil, int(time.Since(t).Milliseconds())
	}
	p := probe{routerOK: a.ping(c.RouterIP), tnasPing: a.ping(c.TNASIP), svcOK: map[string]bool{}, svcMs: map[string]int{}}
	if !p.routerOK {
		return p
	}
	if p.npmOK, p.npmMs = timed(c.Server.NPMCheckHost, c.Server.IP); !p.npmOK {
		p.serverPing = a.ping(c.Server.IP)
	}
	ok, ms := make([]bool, len(c.Services)), make([]int, len(c.Services))
	var wg sync.WaitGroup
	if p.npmOK { // with the NPM down every service is down
		for i, sv := range c.Services {
			wg.Go(func() { ok[i], ms[i] = timed(sv.Host, c.Server.IP) })
		}
	}
	if tnasNPM {
		wg.Go(func() { p.tnasNPMOK = a.sys.Check(c.Server.NPMCheckHost, c.TNASIP) == nil })
	}
	wg.Wait()
	for i, sv := range c.Services {
		p.svcOK[sv.Name], p.svcMs[sv.Name] = ok[i], ms[i]
	}
	return p
}

// Beat is one check of the server's NPM ("npm") or of a service, for the
// heartbeat bars in the UI. Kept in memory only.
type Beat struct {
	T  time.Time `json:"t"`
	S  string    `json:"s"` // up, down, unknown (not checked)
	Ms int       `json:"ms,omitempty"`
}

const maxBeats = 40

func (a *Agent) record(p probe) {
	add := func(name, s string, ms int) {
		b := append(a.beats[name], Beat{T: a.now, S: s, Ms: ms})
		a.beats[name] = b[max(0, len(b)-maxBeats):]
	}
	status := map[bool]string{true: "up", false: "down"}
	if !p.routerOK {
		add("npm", "unknown", 0)
		for _, sv := range a.cfg.Services {
			add(sv.Name, "unknown", 0)
		}
		return
	}
	add("npm", status[p.npmOK], p.npmMs)
	for _, sv := range a.cfg.Services {
		switch {
		case p.npmOK:
			add(sv.Name, status[p.svcOK[sv.Name]], p.svcMs[sv.Name])
		case p.serverPing: // case 2.3: the services were not checked
			add(sv.Name, "unknown", 0)
		default:
			add(sv.Name, "down", 0)
		}
	}
}

func (a *Agent) evaluate(p probe) {
	c, st := &a.cfg, &a.st
	a.record(p)
	if !st.MaintUntil.IsZero() && !a.now.Before(st.MaintUntil) {
		st.MaintUntil = time.Time{}
		a.event("", "manutenção global terminou")
	}
	for _, sv := range c.Services {
		if s := a.svc(sv.Name); !s.MaintUntil.IsZero() && !a.now.Before(s.MaintUntil) {
			s.MaintUntil = time.Time{}
			a.event(sv.Name, "manutenção terminou")
		}
	}

	// R6: without the router we cannot tell who failed, so do nothing.
	st.TNASUp = p.tnasPing
	if p.tnasPing != a.tnasSeen {
		a.tnasSeen = p.tnasPing
		a.event("", map[bool]string{true: "o TNAS responde no IP da LAN", false: "o TNAS não responde no próprio IP da LAN"}[p.tnasPing])
	}
	if p.routerOK != st.RouterOK {
		st.RouterOK = p.routerOK
		a.event("", map[bool]string{true: "router acessível", false: "router inacessível: sem ações"}[p.routerOK])
	}
	if !st.RouterOK {
		return
	}

	// R2: the server's NPM first. Down with the server alive is case 2.3:
	// warn only and leave the services alone (known=false).
	known := true
	st.ServerNPMOK, st.ServerUp = p.npmOK, p.npmOK || p.serverPing
	if st.ServerNPMOK {
		if st.NPMAlerted {
			a.event("", "NPM do servidor voltou")
		}
		st.NPMFailSince, st.NPMAlerted = time.Time{}, false
	} else {
		if st.NPMFailSince.IsZero() {
			st.NPMFailSince = a.now
		}
		if p.serverPing {
			known = false
			if !st.NPMAlerted && a.now.Sub(st.NPMFailSince) >= minutes(c.NPM.AlertAfterMin) {
				st.NPMAlerted = true
				a.event("", "NPM do servidor em falha com o servidor vivo: só aviso, sem failover")
			}
		}
	}

	for _, sv := range c.Services {
		a.step(sv, a.svc(sv.Name), known, p.svcOK[sv.Name])
	}
	if st.TNASNPM.Snapshot != "" {
		st.TNASNPM.OK = p.tnasNPMOK
	}
}

func (a *Agent) set(s *SvcState, state string) {
	s.State, s.Since = state, a.now
	s.FailSince, s.OKSince, s.Observed, s.Msg = time.Time{}, time.Time{}, false, ""
}

// step advances one service; known=false means its server side could not be checked.
func (a *Agent) step(sv Service, s *SvcState, known, ok bool) {
	auto := a.cfg.Mode == "auto"
	if known {
		s.ServerOK = ok
	}
	switch s.State {
	case Normal:
		if !known {
			return
		}
		if ok || a.inMaint(s) {
			if ok && !s.FailSince.IsZero() {
				a.event(sv.Name, "recuperou no servidor antes do failover")
			}
			s.FailSince, s.Observed = time.Time{}, false
			return
		}
		if s.FailSince.IsZero() {
			s.FailSince = a.now
			a.event(sv.Name, "falha no servidor")
		}
		if a.now.Sub(s.FailSince) < minutes(sv.WaitMin) {
			return
		}
		if !auto {
			if !s.Observed {
				s.Observed = true
				a.event(sv.Name, "[observação] o failover começaria agora")
			}
			return
		}
		a.set(s, FailingOver)
		a.failover(sv, s)
	case FailingOver:
		a.failover(sv, s)
	case Active:
		if !known {
			return
		}
		if !ok {
			s.OKSince = time.Time{}
			return
		}
		if s.OKSince.IsZero() {
			s.OKSince = a.now
		}
		if a.now.Sub(s.OKSince) < minutes(sv.StabilityMin) {
			return
		}
		if !auto {
			if !s.Observed {
				s.Observed = true
				a.event(sv.Name, "[observação] o regresso começaria agora")
			}
			return
		}
		a.set(s, Returning)
		a.giveBack(sv, s)
	case Returning:
		a.giveBack(sv, s)
	case Error:
		// Healthy on the server again: clean up whatever is left and reset.
		if known && ok {
			a.set(s, Returning)
			a.giveBack(sv, s)
		}
	}
}

func (a *Agent) failover(sv Service, s *SvcState) {
	c := &a.cfg
	if s.Snapshot == "" {
		if ip := sv.RequireFreeIP; ip != "" { // R3
			if err := a.sys.Run("arping", "-D", "-q", "-c", "2", "-w", "3", "-I", c.LANIface, ip); err != nil {
				a.fail(sv, s, "IP "+ip+" ocupado, o failover não arranca: "+err.Error())
				return
			}
		}
		if err := a.ensureNPM(); err != nil {
			a.fail(sv, s, "NPM do TNAS: "+err.Error())
			return
		}
		snap, err := a.snapshot(sv.Name)
		if err != nil {
			a.fail(sv, s, "snapshot: "+err.Error())
			return
		}
		s.Snapshot = snap
		a.event(sv.Name, "failover iniciado a partir de "+snap)
		if err := a.compose(sv.Name, a.cfg.files(snap, sv.Dir, sv.Override), "up", "-d"); err != nil {
			a.fail(sv, s, "compose up: "+err.Error())
			return
		}
	}
	// R5: the copy is checked through the TNAS NPM; DNS only after it is healthy.
	err := a.sys.Check(sv.Host, c.TNASIP)
	if err == nil && c.DNS.Enabled && !s.DNS {
		if err = a.dns("add", sv.Host); err == nil {
			s.DNS = true
		} else {
			err = fmt.Errorf("DNS: %w", err)
		}
	}
	if err != nil {
		if a.now.Sub(s.Since) >= minutes(c.StartTimeoutMin) {
			a.fail(sv, s, "a cópia não ficou pronta: "+err.Error())
		} else {
			s.Msg = "à espera da cópia: " + err.Error()
		}
		return
	}
	a.set(s, Active)
	a.event(sv.Name, "em failover no TNAS")
}

func (a *Agent) fail(sv Service, s *SvcState, msg string) {
	a.set(s, Error)
	if err := a.teardown(sv, s); err != nil {
		msg += "; limpeza falhou: " + err.Error()
	}
	s.Msg = msg
	a.event(sv.Name, "ERRO: "+msg)
}

// teardown undoes a failover in the order R1 requires: DNS, containers
// (down, never just stop), and only then the snapshot under them.
func (a *Agent) teardown(sv Service, s *SvcState) error {
	if s.DNS {
		if err := a.dns("delete", sv.Host); err != nil {
			return fmt.Errorf("DNS: %w", err)
		}
		s.DNS = false
	}
	if err := a.compose(sv.Name, nil, "down", "-v"); err != nil {
		return err
	}
	if s.Snapshot != "" {
		if err := a.deleteSnapshot(s.Snapshot); err != nil {
			return err
		}
		s.Snapshot = ""
	}
	return nil
}

func (a *Agent) giveBack(sv Service, s *SvcState) {
	if err := a.teardown(sv, s); err != nil {
		a.warn(&s.Msg, sv.Name, "regresso por concluir: "+err.Error())
		return
	}
	a.set(s, Normal)
	a.event(sv.Name, "de volta ao servidor")
}

func (a *Agent) ensureNPM() error {
	n := &a.st.TNASNPM
	if n.Snapshot != "" {
		return nil
	}
	snap, err := a.snapshot("npm")
	if err != nil {
		return err
	}
	n.Snapshot, n.Since, n.OK, n.Msg = snap, a.now, false, ""
	// On failure stopIdleNPM cleans it up at the end of the tick.
	if err := a.compose("npm", a.cfg.files(snap, a.cfg.NPM.Dir, ""), "up", "-d"); err != nil {
		return err
	}
	a.event("", "NPM do TNAS arrancou a partir de "+snap)
	return nil
}

func (a *Agent) stopIdleNPM() {
	n := &a.st.TNASNPM
	if n.Snapshot == "" {
		return
	}
	for _, s := range a.st.Services {
		if s.State == FailingOver || s.State == Active || s.State == Returning {
			return
		}
	}
	err := a.compose("npm", nil, "down", "-v")
	if err == nil {
		err = a.deleteSnapshot(n.Snapshot)
	}
	if err != nil {
		a.warn(&n.Msg, "", "NPM do TNAS por parar: "+err.Error())
		return
	}
	n.Snapshot, n.Since, n.OK, n.Msg = "", time.Time{}, false, ""
	a.event("", "NPM do TNAS parado")
}

func (a *Agent) snapshot(name string) (string, error) {
	p := a.cfg.Paths
	dst := filepath.Join(p.SnapshotsDir, "failover-"+name+"-"+a.now.Format("20060102-150405"))
	_ = os.MkdirAll(p.SnapshotsDir, 0o755) // if this fails, btrfs says so
	if err := a.sys.Run("btrfs", "subvolume", "snapshot", p.MirrorSubvol, dst); err != nil {
		return "", err
	}
	return dst, nil
}

// deleteSnapshot only touches failover-* entries directly inside snapshots_dir:
// btrfs subvolume delete does not ask and must never reach the mirror.
func (a *Agent) deleteSnapshot(p string) error {
	dir := filepath.Clean(a.cfg.Paths.SnapshotsDir)
	if p != filepath.Clean(p) || filepath.Dir(p) != dir || !strings.HasPrefix(filepath.Base(p), "failover-") ||
		p == filepath.Clean(a.cfg.Paths.MirrorSubvol) {
		return fmt.Errorf("recusado apagar %q: não é um snapshot de failover em %s", p, dir)
	}
	if _, err := os.Stat(p); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return a.sys.Run("btrfs", "subvolume", "delete", p)
}

func (c *Config) files(root, dir, override string) []string {
	f := []string{"-f", filepath.Join(root, c.Paths.MirrorRoot, dir, "docker-compose.yml")}
	if override != "" {
		f = append(f, "-f", filepath.Join(c.Paths.OverridesDir, override))
	}
	return f
}

// compose without files works by project name (used for down).
func (a *Agent) compose(project string, files []string, args ...string) error {
	return a.sys.Run("docker", slices.Concat([]string{"compose", "-p", "failover-" + project}, files, args)...)
}

func (a *Agent) dns(op, host string) error {
	d := a.cfg.DNS
	tok, err := os.ReadFile(d.TokenFile)
	if err != nil {
		return err
	}
	q := url.Values{"domain": {host}, "zone": {d.Zone}, "type": {"A"}, "ipAddress": {a.cfg.TNASIP}}
	if op == "add" {
		q.Set("ttl", strconv.Itoa(d.TTL))
		q.Set("overwrite", "true")
	}
	body, err := a.sys.Get(strings.TrimRight(d.APIURL, "/")+"/api/zones/records/"+op+"?"+q.Encode(), strings.TrimSpace(string(tok)))
	if err != nil {
		return err
	}
	var r struct {
		Status       string `json:"status"`
		ErrorMessage string `json:"errorMessage"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return fmt.Errorf("resposta inválida do Technitium: %w", err)
	}
	if r.Status == "ok" || op == "delete" && strings.Contains(r.ErrorMessage, "no such record exists") {
		return nil
	}
	return fmt.Errorf("technitium %s: %s", r.Status, r.ErrorMessage)
}

var stateMsg = map[string]string{
	Normal: "no servidor", FailingOver: "failover em curso", Active: "em failover no TNAS",
	Returning: "a regressar ao servidor", Error: "erro",
}

// report pushes to the Kuma Push monitors every tick; a monitor that stops
// receiving pushes goes down by itself, which is how a dead agent is noticed.
func (a *Agent) report() {
	k := &a.cfg.Kuma
	a.push(k.HeartbeatToken, true, map[bool]string{true: "ok", false: "router inacessível"}[a.st.RouterOK])
	a.push(k.NPMToken, !a.st.NPMAlerted, map[bool]string{true: "ok", false: "NPM do servidor em falha com o servidor vivo"}[!a.st.NPMAlerted])
	for _, sv := range a.cfg.Services {
		s := a.svc(sv.Name)
		msg := stateMsg[s.State]
		if s.Msg != "" {
			msg += ": " + s.Msg
		}
		if a.inMaint(s) {
			msg += " (manutenção)"
		}
		a.push(k.ServiceTokens[sv.Name], s.State == Normal, msg)
	}
}

func (a *Agent) push(token string, up bool, msg string) {
	if token == "" || a.cfg.Kuma.BaseURL == "" {
		return
	}
	q := url.Values{"status": {map[bool]string{true: "up", false: "down"}[up]}, "msg": {msg}}
	_, err := a.sys.Get(strings.TrimRight(a.cfg.Kuma.BaseURL, "/")+"/api/push/"+url.PathEscape(token)+"?"+q.Encode(), "")
	e := ""
	if err != nil {
		e = err.Error()
	}
	if e != a.pushErr {
		a.pushErr = e
		if e != "" {
			log.Printf("kuma: %s", e)
		}
	}
}

type Image struct {
	Ref     string    `json:"ref"`
	Present bool      `json:"present"`
	Size    int64     `json:"size,omitempty"`
	Created time.Time `json:"created,omitzero"`
}

type Stack struct {
	Images []Image `json:"images"`
	Err    string  `json:"err,omitempty"`
}

// scanImages lists, for every stack a failover can start, the images its
// compose files ask for and whether the TNAS already has them (O4). It runs
// outside the lock: a few dozen docker calls.
func (a *Agent) scanImages() {
	if !a.scanning.CompareAndSwap(false, true) {
		return
	}
	defer a.scanning.Store(false)
	a.mu.Lock()
	c := a.cfg
	a.publish() // shows the scan in progress
	a.mu.Unlock()

	stacks := map[string]Stack{}
	var missing []string
	scan := func(name, dir, override string) {
		args := slices.Concat([]string{"compose", "-p", "failover-" + name}, c.files(c.Paths.MirrorSubvol, dir, override), []string{"config", "--images"})
		out, err := a.sys.Output("docker", args...)
		if err != nil {
			stacks[name] = Stack{Err: err.Error()}
			missing = append(missing, name+" (compose ilegível)")
			return
		}
		refs := strings.Fields(out)
		slices.Sort(refs)
		var st Stack
		for _, ref := range slices.Compact(refs) {
			img := Image{Ref: ref}
			if out, err := a.sys.Output("docker", "image", "inspect", "--format", "{{.Size}} {{.Created}}", ref); err == nil {
				img.Present = true
				if f := strings.Fields(out); len(f) == 2 {
					img.Size, _ = strconv.ParseInt(f[0], 10, 64)
					img.Created, _ = time.Parse(time.RFC3339Nano, f[1])
				}
			} else {
				missing = append(missing, ref)
			}
			st.Images = append(st.Images, img)
		}
		stacks[name] = st
	}
	scan("npm", c.NPM.Dir, "")
	for _, sv := range c.Services {
		scan(sv.Name, sv.Dir, sv.Override)
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	a.now = time.Now()
	a.st.Images, a.st.ImagesAt = stacks, a.now
	switch {
	case missing != nil:
		a.warn(&a.st.ImagesMsg, "", "imagens em falta no TNAS, o failover precisaria da internet: "+strings.Join(missing, ", "))
	case a.st.ImagesMsg != "":
		a.st.ImagesMsg = ""
		a.event("", "todas as imagens estão no TNAS")
	}
	a.save()
}

// nightly pulls every image once a day after prepull_at, from the compose
// files in the mirror, so a failover never needs the internet (O4).
func (a *Agent) nightly() {
	at, day := a.cfg.Nightly.PrepullAt, a.now.Format("2006-01-02")
	if at == "" || a.st.LastPull == day || a.now.Format("15:04") < at || !a.pulling.CompareAndSwap(false, true) {
		return
	}
	a.st.LastPull = day
	root := a.cfg.Paths.MirrorSubvol
	jobs := [][]string{slices.Concat([]string{"compose", "-p", "failover-npm"}, a.cfg.files(root, a.cfg.NPM.Dir, ""), []string{"pull", "-q"})}
	for _, sv := range a.cfg.Services {
		jobs = append(jobs, slices.Concat([]string{"compose", "-p", "failover-" + sv.Name}, a.cfg.files(root, sv.Dir, sv.Override), []string{"pull", "-q"}))
	}
	go func() {
		defer a.pulling.Store(false)
		var failed []string
		for _, j := range jobs {
			if err := a.sys.Run("docker", j...); err != nil {
				failed = append(failed, j[2]+": "+err.Error())
			}
		}
		_ = a.sys.Run("docker", "image", "prune", "-f")
		a.scanImages()
		a.mu.Lock()
		defer a.mu.Unlock()
		a.now = time.Now()
		if failed != nil {
			a.event("", "falha ao descarregar imagens: "+strings.Join(failed, "; "))
		} else {
			a.event("", "imagens descarregadas")
		}
		a.save()
	}()
}

func (a *Agent) save() {
	a.publish()
	b, _ := json.MarshalIndent(&a.st, "", "  ")
	if err := writeAtomic(a.statePath, b); err != nil {
		log.Printf("guardar estado: %v", err)
	}
}

// publish renders the UI status once, under the lock, so readers never wait.
func (a *Agent) publish() {
	type svcView struct {
		Service
		*SvcState
	}
	svcs := make([]svcView, 0, len(a.cfg.Services))
	for _, sv := range a.cfg.Services {
		svcs = append(svcs, svcView{sv, a.svc(sv.Name)})
	}
	// A struct, not a map: omitzero only works on fields, and a zero time
	// sent as "0001-01-01" reads as a date in the page.
	b, _ := json.Marshal(struct {
		Now              time.Time `json:"now"`
		Version          string    `json:"version"`
		Mode             string    `json:"mode"`
		CheckIntervalS   int       `json:"check_interval_s"`
		StartTimeoutMin  int       `json:"start_timeout_min"`
		DefaultExpiryMin int       `json:"default_expiry_min"`
		DNSEnabled       bool      `json:"dns_enabled"`
		ServerIP         string    `json:"server_ip"`
		TNASIP           string    `json:"tnas_ip"`
		RouterIP         string    `json:"router_ip"`
		RouterOK         bool      `json:"router_ok"`
		TNASUp           bool      `json:"tnas_up"`
		ServerUp         bool      `json:"server_up"`
		ServerNPMOK      bool      `json:"server_npm_ok"`
		NPMFailSince     time.Time `json:"npm_fail_since,omitzero"`
		NPMAlerted       bool      `json:"npm_alerted"`
		TNASNPM          any       `json:"tnas_npm"`
		MaintUntil       time.Time `json:"maint_until,omitzero"`
		LastPull         string    `json:"last_pull,omitempty"`
		PrepullAt        string    `json:"prepull_at,omitempty"`
		NPMCheckHost     string    `json:"npm_check_host"`
		Images           any       `json:"images"`
		ImagesAt         time.Time `json:"images_at,omitzero"`
		ImagesScanning   bool      `json:"images_scanning"`
		DNSZone          string    `json:"dns_zone"`
		Beats            any       `json:"beats"`
		Services         []svcView `json:"services"`
		Events           []Event   `json:"events"`
	}{
		a.now, version, a.cfg.Mode, a.cfg.CheckIntervalS, a.cfg.StartTimeoutMin, a.cfg.Maintenance.DefaultExpiryMin, a.cfg.DNS.Enabled,
		a.cfg.Server.IP, a.cfg.TNASIP, a.cfg.RouterIP, a.st.RouterOK, a.st.TNASUp, a.st.ServerUp, a.st.ServerNPMOK, a.st.NPMFailSince, a.st.NPMAlerted,
		a.st.TNASNPM, a.st.MaintUntil, a.st.LastPull, a.cfg.Nightly.PrepullAt, a.cfg.Server.NPMCheckHost,
		a.st.Images, a.st.ImagesAt, a.scanning.Load(), a.cfg.DNS.Zone, a.beats, svcs, a.st.Events,
	})
	a.view.Store(&b)
}
