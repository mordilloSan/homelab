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
