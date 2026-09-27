package agent

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// fake is the TNAS as the agent sees it: pings, HTTPS checks and commands.
type fake struct {
	mu      sync.Mutex
	cmds    []string        // every command except ping, in order
	gets    []string        // every URL fetched
	noPing  map[string]bool // ip → does not answer ping
	down    map[string]bool // "host@ip" → HTTPS check fails
	failCmd []string        // command prefixes that fail
	outs    map[string]string
	scans   []string // commands run through Output
}

func (f *fake) Run(name string, args ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if name == "ping" {
		if f.noPing[args[len(args)-1]] {
			return errors.New("sem resposta")
		}
		return nil
	}
	c := strings.Join(append([]string{name}, args...), " ")
	f.cmds = append(f.cmds, c)
	for _, p := range f.failCmd {
		if strings.HasPrefix(c, p) {
			return errors.New("falhou")
		}
	}
	switch {
	case name == "btrfs" && args[1] == "snapshot":
		return os.Mkdir(args[3], 0o755)
	case name == "btrfs" && args[1] == "delete":
		return os.Remove(args[2])
	}
	return nil
}

// Output answers the image scan: outs maps a command prefix to its stdout;
// a command with no entry fails, like docker image inspect of a missing image.
func (f *fake) Output(name string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := strings.Join(append([]string{name}, args...), " ")
	f.scans = append(f.scans, c)
	best := ""
	for p := range f.outs { // the longest matching prefix wins, whatever the map order
		if strings.HasPrefix(c, p) && len(p) > len(best) {
			best = p
		}
	}
	if best == "" {
		return "", errors.New("não existe")
	}
	return f.outs[best], nil
}

func (f *fake) Check(host, ip string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down[host+"@"+ip] {
		return errors.New("HTTP 502")
	}
	return nil
}

func (f *fake) Get(u, _ string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets = append(f.gets, u)
	return []byte(`{"status":"ok","ok":true}`), nil
}

// ran reports whether a command starting with prefix was run, and its position.
func (f *fake) ran(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.IndexFunc(f.cmds, func(c string) bool { return strings.HasPrefix(c, prefix) })
}

func (f *fake) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cmds, f.gets = nil, nil
}

const (
	srv  = "192.168.1.66"
	tnas = "192.168.1.249"
)

var t0 = time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

func init() { bcryptCost, loginDelay = bcrypt.MinCost, 0 }

// newTestAgent loads the example config, so the shipped file is tested too.
func newTestAgent(t *testing.T, dir string, f *fake) *Agent {
	t.Helper()
	cfg, err := LoadConfig("config/failover.yml")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Mode = "auto"
	cfg.Paths.SnapshotsDir = filepath.Join(dir, "snaps")
	cfg.Nightly.PrepullAt = ""
	cfg.DNS.Enabled = true
	cfg.DNS.TokenFile = filepath.Join(dir, "token")
	cfg.Kuma.HeartbeatToken, cfg.Kuma.NPMToken = "hb", "npmtok"
	cfg.Kuma.ServiceTokens = map[string]string{"vaultwarden": "vwtok"}
	if err := os.WriteFile(cfg.DNS.TokenFile, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := NewAgent(cfg, filepath.Join(dir, "failover.yml"), filepath.Join(dir, "state.json"), f)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func setup(t *testing.T) (*Agent, *fake) {
	f := &fake{noPing: map[string]bool{}, down: map[string]bool{}}
	return newTestAgent(t, t.TempDir(), f), f
}

// tickTo ticks once a minute from *at until t0+min, inclusive.
func tickTo(a *Agent, at *time.Time, min int) {
	for end := t0.Add(minutes(min)); !at.After(end); *at = at.Add(time.Minute) {
		a.Tick(*at)
	}
}

func state(a *Agent, svc string) string { return a.st.Services[svc].State }

func wantStates(t *testing.T, a *Agent, want map[string]string) {
	t.Helper()
	for svc, st := range want {
		if got := state(a, svc); got != st {
			t.Errorf("%s: estado %s, esperado %s (%s)", svc, got, st, a.st.Services[svc].Msg)
		}
	}
}

func hasGet(f *fake, sub string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.ContainsFunc(f.gets, func(u string) bool { return strings.Contains(u, sub) })
}

func hasEvent(a *Agent, sub string) bool {
	return slices.ContainsFunc(a.st.Events, func(e Event) bool { return strings.Contains(e.Msg, sub) })
}

// T-14 + T-20 + R1: only vaultwarden fails; failover at 5 min, return after
// 10 min stable, undone in order: DNS, down, snapshot; then the TNAS NPM.
func TestPartialFailoverAndReturn(t *testing.T) {
	a, f := setup(t)
	f.down["bitwarden.engmariz.com@"+srv] = true
	at := t0
	tickTo(a, &at, 4)
	if f.ran("") >= 0 {
		t.Fatalf("agiu antes do wait_min: %v", f.cmds)
	}
	tickTo(a, &at, 5)
	wantStates(t, a, map[string]string{"vaultwarden": Active, "homepage": Normal, "immich": Normal, "unifi": Normal})
	npmUp, vwUp := f.ran("docker compose -p failover-npm -f"), f.ran("docker compose -p failover-vaultwarden -f")
	if npmUp < 0 || vwUp < npmUp || f.ran("btrfs subvolume snapshot /Volume1/ServerBackup") > npmUp {
		t.Fatalf("arranque errado: %v", f.cmds)
	}
	if !strings.Contains(f.cmds[vwUp], "/homelab/vaultwarden/docker-compose.yml up -d") {
		t.Fatalf("compose errado: %s", f.cmds[vwUp])
	}
	if !hasGet(f, "records/add?") || !hasGet(f, "domain=bitwarden.engmariz.com") || !hasGet(f, "ttl=60") {
		t.Fatalf("DNS não mudou: %v", f.gets)
	}
	if !hasEvent(a, "DNS: bitwarden.engmariz.com → "+tnas) {
		t.Fatalf("sem evento do DNS: %v", a.st.Events)
	}
	if !hasGet(f, "/api/push/vwtok?msg=em+failover+no+TNAS&status=down") {
		t.Fatalf("Kuma não recebeu o down: %v", f.gets)
	}
	snap := a.st.Services["vaultwarden"].Snapshot
	npmSnap := a.st.TNASNPM.Snapshot

	delete(f.down, "bitwarden.engmariz.com@"+srv)
	f.reset()
	tickTo(a, &at, 14) // stable since minute 6: still < 10 min
	if state(a, "vaultwarden") != Active {
		t.Fatalf("voltou antes do stability_min")
	}
	tickTo(a, &at, 16)
	wantStates(t, a, map[string]string{"vaultwarden": Normal})
	down, del := f.ran("docker compose -p failover-vaultwarden down -v"), f.ran("btrfs subvolume delete "+snap)
	if !hasGet(f, "records/delete?") || down < 0 || del < down {
		t.Fatalf("R1 violado: %v", f.cmds)
	}
	if !hasEvent(a, "DNS: bitwarden.engmariz.com de volta ao servidor") {
		t.Fatalf("sem evento do regresso do DNS: %v", a.st.Events)
	}
	if f.ran("docker compose -p failover-npm down -v") < del || f.ran("btrfs subvolume delete "+npmSnap) < 0 {
		t.Fatalf("NPM do TNAS não parou: %v", f.cmds)
	}
	if left, _ := os.ReadDir(a.cfg.Paths.SnapshotsDir); len(left) != 0 {
		t.Fatalf("sobraram snapshots: %v", left)
	}
}

// T-15: server NPM down but the server pings: warn after 3 min, never fail over.
func TestServerNPMDownServerAlive(t *testing.T) {
	a, f := setup(t)
	f.down["nginx.engmariz.com@"+srv] = true
	at := t0
	tickTo(a, &at, 2)
	if a.st.NPMAlerted {
		t.Fatal("avisou antes de alert_after_min")
	}
	tickTo(a, &at, 60)
	if !a.st.NPMAlerted || !hasGet(f, "/api/push/npmtok?msg=NPM") {
		t.Fatal("sem aviso do NPM")
	}
	if f.ran("") >= 0 {
		t.Fatalf("agiu no caso 2.3: %v", f.cmds)
	}
	// the bars in the UI: the NPM failed, the services were not checked
	if b := a.beats["vaultwarden"]; len(b) != maxBeats || b[len(b)-1].S != "unknown" || a.beats["npm"][maxBeats-1].S != "down" {
		t.Fatalf("histórico das verificações errado: %d %+v", len(b), b[len(b)-1])
	}
}

// T-16: router unreachable: nothing happens even with everything down.
func TestRouterDown(t *testing.T) {
	a, f := setup(t)
	f.noPing["192.168.1.1"], f.noPing[srv] = true, true
	f.down["nginx.engmariz.com@"+srv] = true
	at := t0
	tickTo(a, &at, 60)
	if f.ran("") >= 0 || a.st.RouterOK {
		t.Fatalf("agiu sem router: %v", f.cmds)
	}
}

// T-19: server off: each service moves at its own wait_min, one TNAS NPM.
func TestTotalFailure(t *testing.T) {
	a, f := setup(t)
	f.noPing[srv] = true
	f.down["nginx.engmariz.com@"+srv] = true
	at := t0
	tickTo(a, &at, 5)
	wantStates(t, a, map[string]string{"vaultwarden": Active, "homepage": Normal})
	tickTo(a, &at, 10)
	wantStates(t, a, map[string]string{"homepage": Active, "immich": Normal})
	tickTo(a, &at, 15)
	wantStates(t, a, map[string]string{"immich": Active, "unifi": Active, "speedtest": Normal})
	tickTo(a, &at, 30)
	wantStates(t, a, map[string]string{"speedtest": Active})
	if a.st.ServerUp || !a.st.TNASUp {
		t.Fatalf("servidor desligado tem de aparecer em baixo e o TNAS em cima: %+v", a.st)
	}
	f.mu.Lock()
	npmStarts := 0
	for _, c := range f.cmds {
		if strings.HasPrefix(c, "docker compose -p failover-npm -f") {
			npmStarts++
		}
	}
	f.mu.Unlock()
	if npmStarts != 1 {
		t.Fatalf("NPM do TNAS arrancou %d vezes", npmStarts)
	}
	if i := f.ran("docker compose -p failover-immich"); !strings.Contains(f.cmds[i], "-f /Volume1/Docker/failover/overrides/immich.override.yml up -d") {
		t.Fatalf("immich sem override: %s", f.cmds[i])
	}
	if f.ran("arping -D -q -c 2 -w 3 -I ovs_eth0 192.168.1.92") < 0 {
		t.Fatal("unifi sem teste de IP duplicado")
	}
}

// T-11 / R3: the .92 answers: unifi does not start and nothing is left behind.
func TestUnifiIPBusy(t *testing.T) {
	a, f := setup(t)
	f.failCmd = []string{"arping"}
	f.down["unifi.engmariz.com@"+srv] = true
	at := t0
	tickTo(a, &at, 15)
	wantStates(t, a, map[string]string{"unifi": Error})
	if f.ran("btrfs subvolume snapshot") >= 0 || f.ran("docker compose -p failover-unifi -f") >= 0 {
		t.Fatalf("arrancou com o IP ocupado: %v", f.cmds)
	}
	tickTo(a, &at, 40)
	if n := strings.Count(strings.Join(f.cmds, "\n"), "arping"); n != 1 {
		t.Fatalf("repetiu o failover em ERROR (%d arpings)", n)
	}
	delete(f.down, "unifi.engmariz.com@"+srv)
	tickTo(a, &at, 41)
	wantStates(t, a, map[string]string{"unifi": Normal})
}

// The copy never gets healthy: ERROR after start_timeout_min, everything removed.
func TestStartTimeout(t *testing.T) {
	a, f := setup(t)
	f.down["bitwarden.engmariz.com@"+srv] = true
	f.down["bitwarden.engmariz.com@"+tnas] = true
	at := t0
	tickTo(a, &at, 14)
	wantStates(t, a, map[string]string{"vaultwarden": FailingOver})
	tickTo(a, &at, 15)
	wantStates(t, a, map[string]string{"vaultwarden": Error})
	if hasGet(f, "records/add") {
		t.Fatal("DNS mudado sem cópia saudável")
	}
	if a.st.Services["vaultwarden"].Snapshot != "" || a.st.TNASNPM.Snapshot != "" {
		t.Fatal("snapshots não apagados")
	}
	if left, _ := os.ReadDir(a.cfg.Paths.SnapshotsDir); len(left) != 0 {
		t.Fatalf("sobraram snapshots: %v", left)
	}
}

// T-17: maintenance blocks failovers while on; wait_min counts again after it expires.
func TestMaintenance(t *testing.T) {
	a, f := setup(t)
	a.st.MaintUntil = t0.Add(30 * time.Minute)
	a.st.Services["homepage"] = &SvcState{State: Normal, MaintUntil: t0.Add(90 * time.Minute)}
	f.down["bitwarden.engmariz.com@"+srv] = true
	f.down["homepage.engmariz.com@"+srv] = true
	at := t0
	tickTo(a, &at, 34)
	if f.ran("") >= 0 || !a.st.MaintUntil.IsZero() {
		t.Fatalf("failover durante a manutenção ou sem expirar: %v", f.cmds)
	}
	tickTo(a, &at, 35)
	wantStates(t, a, map[string]string{"vaultwarden": Active, "homepage": Normal})
	tickTo(a, &at, 99)
	wantStates(t, a, map[string]string{"homepage": Normal})
	tickTo(a, &at, 100) // its maintenance ended at 90
	wantStates(t, a, map[string]string{"homepage": Active})
}

// F2: observe mode only logs, once.
func TestObserveMode(t *testing.T) {
	a, f := setup(t)
	a.cfg.Mode = "observe"
	f.down["bitwarden.engmariz.com@"+srv] = true
	at := t0
	tickTo(a, &at, 60)
	if f.ran("") >= 0 {
		t.Fatalf("agiu em observação: %v", f.cmds)
	}
	n := 0
	for _, e := range a.st.Events {
		if strings.HasPrefix(e.Msg, "[observação]") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d eventos de observação, esperado 1", n)
	}
}

// T-21 / R7: a restarted agent picks up an active failover from disk.
func TestRestartResumes(t *testing.T) {
	dir := t.TempDir()
	f := &fake{noPing: map[string]bool{}, down: map[string]bool{"bitwarden.engmariz.com@" + srv: true}}
	a := newTestAgent(t, dir, f)
	at := t0
	tickTo(a, &at, 5)
	snap := a.st.Services["vaultwarden"].Snapshot

	b := newTestAgent(t, dir, f)
	if state(b, "vaultwarden") != Active || b.st.Services["vaultwarden"].Snapshot != snap || !b.st.Services["vaultwarden"].DNS {
		t.Fatalf("estado não recuperado: %+v", b.st.Services["vaultwarden"])
	}
	delete(f.down, "bitwarden.engmariz.com@"+srv)
	tickTo(b, &at, 20)
	wantStates(t, b, map[string]string{"vaultwarden": Normal})
	if _, err := os.Stat(snap); !os.IsNotExist(err) {
		t.Fatal("snapshot ficou depois do regresso")
	}
}

func TestDeleteSnapshotGuard(t *testing.T) {
	a, f := setup(t)
	d := a.cfg.Paths.SnapshotsDir
	for _, p := range []string{"/Volume1/ServerBackup", d, d + "/other", d + "/failover-x/../../x", "/Volume1/failover-x"} {
		if err := a.deleteSnapshot(p); err == nil {
			t.Errorf("aceitou apagar %s", p)
		}
	}
	if f.ran("btrfs") >= 0 {
		t.Fatalf("btrfs chamado: %v", f.cmds)
	}
}

// O4: the scan finds each stack's images from its compose files (overrides
// included) and reports the ones the TNAS does not have.
func TestScanImages(t *testing.T) {
	a, f := setup(t)
	f.outs = map[string]string{
		"docker compose -p failover-immich":                             "ghcr.io/immich-app/immich-server:v2\nredis:7\nredis:7\n",
		"docker compose -p failover-":                                   "vaultwarden/server:latest\n",
		"docker image inspect --format {{.Size}} {{.Created}} redis:7":  "41000000 2026-09-20T04:00:00.5Z\n",
		"docker image inspect --format {{.Size}} {{.Created}} vaultwar": "250000000 2026-09-21T04:00:00Z\n",
	}
	a.scanImages()
	im := a.st.Images["immich"].Images
	if len(im) != 2 || im[0].Present || !im[1].Present || im[1].Size != 41000000 || im[1].Created.IsZero() {
		t.Fatalf("imagens do immich: %+v", im)
	}
	if v := a.st.Images["vaultwarden"].Images; len(v) != 1 || !v[0].Present || a.st.Images["npm"].Images == nil {
		t.Fatalf("imagens: %+v", a.st.Images)
	}
	if !slices.ContainsFunc(f.scans, func(c string) bool {
		return strings.Contains(c, "immich.override.yml config --images")
	}) {
		t.Fatalf("o scan do immich ignorou o override: %v", f.scans)
	}
	if !strings.Contains(a.st.ImagesMsg, "ghcr.io/immich-app/immich-server:v2") || strings.Contains(a.st.ImagesMsg, "redis") {
		t.Fatalf("aviso errado: %q", a.st.ImagesMsg)
	}
	f.outs["docker image inspect --format {{.Size}} {{.Created}} ghcr.io"] = "1 2026-09-22T04:00:00Z"
	a.scanImages()
	if a.st.ImagesMsg != "" || a.st.Events[len(a.st.Events)-1].Msg != "todas as imagens estão no TNAS" {
		t.Fatalf("não avisou que as imagens voltaram: %q", a.st.ImagesMsg)
	}
}
