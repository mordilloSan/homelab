package agent

import (
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const procRoute = `Iface	Destination	Gateway 	Flags	RefCnt	Use	Metric	Mask		MTU	Window	IRTT
docker0	000011AC	00000000	0001	0	0	0	0000FFFF	0	0	0
ovs_eth0	00000000	0101A8C0	0003	0	0	10	00000000	0	0	0
ovs_eth0	0001A8C0	00000000	0001	0	0	0	00FFFFFF	0	0	0
`

func TestDefaultRoute(t *testing.T) {
	if got := defaultRoute([]byte(procRoute)); got != "192.168.1.1" {
		t.Fatal(got)
	}
	if got := defaultRoute([]byte("Iface\tDestination\n")); got != "" {
		t.Fatalf("sem rota por defeito: %q", got)
	}
}

func TestLanOf(t *testing.T) {
	_, lan, _ := net.ParseCIDR("192.168.1.249/24")
	_, dock, _ := net.ParseCIDR("172.17.0.1/16")
	ifaces := []ifaceAddr{
		{"docker0", []*net.IPNet{{IP: net.ParseIP("172.17.0.1"), Mask: dock.Mask}}},
		{"ovs_eth0", []*net.IPNet{{IP: net.ParseIP("192.168.1.249"), Mask: lan.Mask}}},
	}
	ip, iface := lanOf(ifaces, net.ParseIP("192.168.1.1"))
	if ip != "192.168.1.249" || iface != "ovs_eth0" {
		t.Fatal(ip, iface)
	}
	if ip, _ := lanOf(ifaces, nil); ip != "" {
		t.Fatal("sem router não há LAN")
	}
}

// The NPM writes one file per proxy host; other files and odd lines are ignored.
func TestReadProxyHosts(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "data", "nginx", "proxy_host")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"1.conf": `# ------------------------------------------------------------
# bitwarden.engmariz.com, vault.engmariz.com
# ------------------------------------------------------------
server {
  set $forward_scheme http;
  set $server         "192.168.1.66";
  set $port           8080;
  listen 443 ssl;
  server_name bitwarden.engmariz.com vault.engmariz.com;
}`,
		"2.conf":  "server {\n  set $server immich_server;\n  set $port 2283;\n  server_name immich.engmariz.com;\n}\n",
		"3.conf":  "server {\n  server_name semdestino.engmariz.com;\n}\n", // no $server: skipped
		"x.txt":   "server_name nao.engmariz.com;",
		"4.conf~": "server_name backup.engmariz.com;",
	}
	for n, c := range files {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := findProxyHostDir(root); got != dir {
		t.Fatalf("pasta: %q", got)
	}
	hosts := readProxyHosts(dir)
	if len(hosts) != 2 {
		t.Fatalf("%+v", hosts)
	}
	bw := hosts[slices.IndexFunc(hosts, func(h proxyHost) bool { return h.Port == 8080 })]
	if bw.Server != "192.168.1.66" || !slices.Equal(bw.Domains, []string{"bitwarden.engmariz.com", "vault.engmariz.com"}) {
		t.Fatalf("%+v", bw)
	}
	if findProxyHostDir(t.TempDir()) != "" {
		t.Fatal("inventou uma pasta")
	}
}

// docker compose config --format json of unifi (macvlan, fixed IP) and of
// immich (a GPU container beside the one the NPM serves).
const unifiJSON = `{"name":"unifi","services":{"unifi":{"container_name":"unifi","image":"x",
  "networks":{"lan":{"ipv4_address":"192.168.1.92"}},"ports":[{"mode":"ingress","target":8443,"published":"8443","protocol":"tcp"}]}},
  "networks":{"lan":{"name":"lan","driver":"macvlan","driver_opts":{"parent":"enp2s0"}}}}`

const immichJSON = `{"name":"immich","services":{
  "immich-server":{"container_name":"immich_server","image":"x","ports":[{"target":2283,"published":2283}]},
  "immich-machine-learning":{"image":"y","deploy":{"resources":{"reservations":{"devices":[{"capabilities":["gpu"]}]}}}},
  "redis":{"image":"z"}}}`

func TestAnalyzeCompose(t *testing.T) {
	u, err := analyzeCompose([]byte(unifiJSON), "ovs_eth0")
	if err != nil || u.FreeIP != "192.168.1.92" || !strings.Contains(u.Override, "parent: ovs_eth0") || !u.Names["unifi"] || u.Ports[8443] == "" {
		t.Fatalf("unifi: %v %+v", err, u)
	}
	im, err := analyzeCompose([]byte(immichJSON), "ovs_eth0")
	if err != nil || !im.Names["immich_server"] || !im.Names["immich-server"] || im.Ports[2283] != "immich-server" {
		t.Fatalf("immich: %v %+v", err, im)
	}
	if _, err := analyzeCompose([]byte("não é json"), ""); err == nil {
		t.Fatal("aceitou lixo")
	}
}

func TestMatchHost(t *testing.T) {
	hosts := []proxyHost{
		{Domains: []string{"bitwarden.engmariz.com"}, Server: "192.168.1.66", Port: 8080},
		{Domains: []string{"immich.engmariz.com"}, Server: "immich_server", Port: 2283},
		{Domains: []string{"unifi.engmariz.com"}, Server: "192.168.1.92", Port: 8443},
	}
	vw := composeInfo{Names: map[string]bool{"vaultwarden": true}, Ports: map[int]string{8080: "vaultwarden"}}
	im, _ := analyzeCompose([]byte(immichJSON), "")
	u, _ := analyzeCompose([]byte(unifiJSON), "")
	for want, info := range map[string]composeInfo{"bitwarden.engmariz.com": vw, "immich.engmariz.com": im, "unifi.engmariz.com": u} {
		if got, _ := matchHost(hosts, info, "192.168.1.66"); got != want {
			t.Errorf("queria %s, deu %q", want, got)
		}
	}
	other := composeInfo{Names: map[string]bool{"x": true}, Ports: map[int]string{9999: "x"}}
	if got, _ := matchHost(hosts, other, "192.168.1.66"); got != "" {
		t.Fatalf("inventou %q", got)
	}
}

// The override disables the GPU container the NPM does not serve, and moves a
// macvlan to the TNAS's LAN interface.
func TestSuggestedOverride(t *testing.T) {
	im, _ := analyzeCompose([]byte(immichJSON), "ovs_eth0")
	ov := im.overrideFor("immich-server")
	if !strings.Contains(ov, "immich-machine-learning:") || !strings.Contains(ov, `profiles: ["disabled"]`) || strings.Contains(ov, "immich-server:") {
		t.Fatalf("%s", ov)
	}
	if len(im.Notes) == 0 {
		t.Fatal("sem nota sobre a GPU")
	}
}

func TestSlugName(t *testing.T) {
	for in, want := range map[string]string{"speedtest-tracker": "speedtest-tracker", "Home Assistant": "home-assistant", "_x_": "x_", "Ünifi!": "nifi"} {
		if got := slugName(in); got != want {
			t.Errorf("%q: %q, queria %q", in, got, want)
		}
	}
}

// discoverSetup: a mirror with vaultwarden, unifi, immich, the NPM (with its
// proxy hosts), a compose that does not resolve and a folder without one; a
// route table and interfaces like the TNAS's.
func discoverSetup(t *testing.T) (*Agent, *fake) {
	t.Helper()
	a, f := svcSetup(t)
	root := filepath.Join(a.cfg.Paths.MirrorSubvol, a.cfg.Paths.MirrorRoot)
	for _, d := range []string{"vaultwarden", "unifi", "immich", "npm", "partido"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, d, "docker-compose.yml"), []byte("services: {}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ph := filepath.Join(root, "npm", "data", "nginx", "proxy_host")
	if err := os.MkdirAll(ph, 0o755); err != nil {
		t.Fatal(err)
	}
	for n, c := range map[string]string{
		"1": "server {\n set $server \"192.168.1.66\";\n set $port 8080;\n server_name bitwarden.engmariz.com;\n}\n",
		"2": "server {\n set $server immich_server;\n set $port 2283;\n server_name immich.engmariz.com;\n}\n",
		"3": "server {\n set $server 192.168.1.92;\n set $port 8443;\n server_name unifi.engmariz.com;\n}\n",
		"4": "server {\n set $server 192.168.1.66;\n set $port 81;\n server_name nginx.engmariz.com;\n}\n",
	} {
		if err := os.WriteFile(filepath.Join(ph, n+".conf"), []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	route := filepath.Join(t.TempDir(), "route")
	if err := os.WriteFile(route, []byte(procRoute), 0o600); err != nil {
		t.Fatal(err)
	}
	a.procRoute = route
	_, lan, _ := net.ParseCIDR("192.168.1.0/24")
	a.ifaces = func() []ifaceAddr {
		return []ifaceAddr{{"ovs_eth0", []*net.IPNet{{IP: net.ParseIP("192.168.1.249"), Mask: lan.Mask}}}}
	}
	vwJSON := `{"name":"vaultwarden","services":{"vaultwarden":{"container_name":"vaultwarden","ports":[{"published":"8080","target":80}]}}}`
	npmJSON := `{"name":"npm","services":{"app":{"container_name":"npm","ports":[{"published":"81"},{"published":"443"}]}}}`
	f.outs = map[string]string{
		"docker compose -p discover-vaultwarden": vwJSON, "docker compose -p discover-unifi": unifiJSON,
		"docker compose -p discover-immich": immichJSON, "docker compose -p discover-npm": npmJSON,
	}
	api := strings.TrimRight(a.cfg.DNS.APIURL, "/")
	f.bodies = map[string][]byte{
		api + "/api/zones/list?": []byte(`{"status":"ok","response":{"zones":[{"name":"engmariz.com","type":"Primary","internal":false},{"name":"0.in-addr.arpa","type":"Primary","internal":true}]}}`),
		api + "/api/zones/records/get?domain=%2A.engmariz.com&zone=engmariz.com": []byte(`{"status":"ok","response":{"records":[{"name":"*.engmariz.com","type":"A","rData":{"ipAddress":"192.168.1.66"}}]}}`),
	}
	return a, f
}

func TestDiscover(t *testing.T) {
	a, _ := discoverSetup(t)
	d := a.discover()
	n := d.Network
	if n.RouterIP != "192.168.1.1" || n.TNASIP != "192.168.1.249" || n.LANIface != "ovs_eth0" || n.ServerIP != "192.168.1.66" || n.NPMCheckHost != "nginx.engmariz.com" {
		t.Fatalf("rede: %+v", n)
	}
	if d.DNS.Zone != "engmariz.com" || !d.NPM.Found || d.NPM.ProxyHosts != 4 || !d.Mirror.Found {
		t.Fatalf("dns %+v npm %+v espelho %+v", d.DNS, d.NPM, d.Mirror)
	}
	by := map[string]discoveredService{}
	for _, s := range d.Services {
		by[s.Dir] = s
	}
	if by["npm"].Dir != "" || by["vaultwarden"].Dir == "" || by["unifi"].Dir == "" || by["immich"].Dir == "" || by["partido"].Dir == "" {
		t.Fatalf("serviços: %+v", d.Services)
	}
	if s := by["vaultwarden"]; s.Host != "bitwarden.engmariz.com" || !s.Configured || s.Name != "vaultwarden" {
		t.Errorf("vaultwarden: %+v", s)
	}
	if s := by["unifi"]; s.Host != "unifi.engmariz.com" || s.RequireFreeIP != "192.168.1.92" || !strings.Contains(s.OverrideYAML, "parent: ovs_eth0") {
		t.Errorf("unifi: %+v", s)
	}
	if s := by["immich"]; s.Host != "immich.engmariz.com" || !strings.Contains(s.OverrideYAML, "immich-machine-learning") || len(s.Notes) == 0 {
		t.Errorf("immich: %+v", s)
	}
	if s := by["partido"]; s.Error == "" || s.Host != "" {
		t.Errorf("uma pasta que não resolve devia dar erro sem estragar as outras: %+v", s)
	}
}

// No route (no network yet) and no NPM: empty values, not errors.
func TestDiscoverNothing(t *testing.T) {
	a, _ := svcSetup(t)
	a.procRoute = filepath.Join(t.TempDir(), "nao-existe")
	a.ifaces = func() []ifaceAddr { return nil }
	d := a.discover()
	if d.Network.RouterIP != "" || d.NPM.Found || d.Services == nil {
		t.Fatalf("%+v", d)
	}
}

// The Technitium login makes a token and keeps it; the password is never kept.
func TestTechnitiumLogin(t *testing.T) {
	a, f := setup(t)
	api := strings.TrimRight(a.cfg.DNS.APIURL, "/")
	f.bodies = map[string][]byte{
		api + "/api/user/createToken?pass=certa&tokenName=failover-agent&user=admin":  []byte(`{"status":"ok","username":"admin","tokenName":"failover-agent","token":"novo-token"}`),
		api + "/api/user/createToken?pass=errada&tokenName=failover-agent&user=admin": []byte(`{"status":"error","errorMessage":"Invalid username or password."}`),
	}
	if code, body := postTo(t, a.postTechnitiumLogin, `{"user":"admin","pass":"errada"}`); code != 400 || !strings.Contains(body, "Invalid username") {
		t.Fatalf("password errada: HTTP %d %s", code, body)
	}
	if b, _ := os.ReadFile(a.cfg.DNS.TokenFile); strings.TrimSpace(string(b)) != "secret" {
		t.Fatal("uma recusa mudou o token")
	}
	if code, body := postTo(t, a.postTechnitiumLogin, `{"user":"admin","pass":"certa"}`); code != 204 {
		t.Fatalf("HTTP %d %s", code, body)
	}
	if b, _ := os.ReadFile(a.cfg.DNS.TokenFile); strings.TrimSpace(string(b)) != "novo-token" {
		t.Fatalf("token: %q", b)
	}
	for _, p := range []string{a.cfgPath, a.statePath, a.cfg.DNS.TokenFile} {
		if b, _ := os.ReadFile(p); strings.Contains(string(b), "certa") {
			t.Fatalf("a password ficou em %s", p)
		}
	}
}
