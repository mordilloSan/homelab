package agent

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Discovery: what the TNAS can find out by itself, so the settings are
// proposed and not typed. It only reads; saving goes through the usual
// requests. Everything here is pure, over what discover() gathers.

// defaultRoute is the router's address in /proc/net/route: the default
// route (destination 0) with the lowest metric; "" without one.
func defaultRoute(procRoute []byte) string {
	best, metric := "", -1
	sc := bufio.NewScanner(bytes.NewReader(procRoute))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 7 || f[1] != "00000000" {
			continue
		}
		flags, err1 := strconv.ParseUint(f[3], 16, 32)
		m, err2 := strconv.Atoi(f[6])
		gw, err3 := hex.DecodeString(f[2])
		if err1 != nil || err2 != nil || err3 != nil || len(gw) != 4 || flags&0x2 == 0 { // RTF_GATEWAY
			continue
		}
		if metric < 0 || m < metric {
			best, metric = net.IPv4(gw[3], gw[2], gw[1], gw[0]).String(), m // little endian
		}
	}
	return best
}

type ifaceAddr struct {
	name  string
	addrs []*net.IPNet
}

// lanOf is the TNAS's address and interface on the router's network.
func lanOf(ifaces []ifaceAddr, gw net.IP) (ip, iface string) {
	if gw == nil {
		return "", ""
	}
	for _, i := range ifaces {
		for _, n := range i.addrs {
			if n.Contains(gw) {
				return n.IP.String(), i.name
			}
		}
	}
	return "", ""
}

func systemIfaces() []ifaceAddr {
	ifs, _ := net.Interfaces()
	var out []ifaceAddr
	for _, i := range ifs {
		addrs, _ := i.Addrs()
		var nets []*net.IPNet
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil {
				nets = append(nets, n)
			}
		}
		out = append(out, ifaceAddr{i.Name, nets})
	}
	return out
}

// proxyHost is one proxy host of the server's NPM, as its generated config says.
type proxyHost struct {
	Domains []string `json:"domains"`
	Server  string   `json:"server"` // the forward host: an IP or a container's name
	Port    int      `json:"port"`
}

// findProxyHostDir looks for nginx/proxy_host under the NPM's folder in the
// mirror (the data volume's place varies), a few levels down at most.
func findProxyHostDir(npmRoot string) string {
	want := filepath.Join("nginx", "proxy_host")
	found := ""
	_ = filepath.WalkDir(npmRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil || found != "" {
			return filepath.SkipDir
		}
		if !d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(npmRoot, p)
		if strings.Count(rel, string(filepath.Separator)) >= 4 {
			return filepath.SkipDir
		}
		if strings.HasSuffix(p, want) {
			found = p
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

var (
	reServerName = regexp.MustCompile(`^\s*server_name\s+([^;]+);`)
	reServer     = regexp.MustCompile(`^\s*set\s+\$server\s+"?([^";\s]+)"?\s*;`)
	rePort       = regexp.MustCompile(`^\s*set\s+\$port\s+(\d+)\s*;`)
	reDashes     = regexp.MustCompile(`-+`)
)

// readProxyHosts reads the NPM's proxy_host/*.conf; a file without a
// forward host or a domain is not a proxy host and is left out.
func readProxyHosts(dir string) []proxyHost {
	files, _ := filepath.Glob(filepath.Join(dir, "*.conf"))
	var out []proxyHost
	for _, f := range files {
		if b, err := os.ReadFile(f); err == nil {
			if h := parseProxyHost(b); h.Server != "" && len(h.Domains) > 0 {
				out = append(out, h)
			}
		}
	}
	return out
}

func parseProxyHost(b []byte) proxyHost {
	var h proxyHost
	for line := range strings.SplitSeq(string(b), "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		switch {
		case reServerName.MatchString(line):
			for _, d := range strings.Fields(reServerName.FindStringSubmatch(line)[1]) {
				if !slices.Contains(h.Domains, d) && isName(d) {
					h.Domains = append(h.Domains, d)
				}
			}
		case reServer.MatchString(line) && h.Server == "":
			h.Server = reServer.FindStringSubmatch(line)[1]
		case rePort.MatchString(line) && h.Port == 0:
			h.Port, _ = strconv.Atoi(rePort.FindStringSubmatch(line)[1])
		}
	}
	return h
}

// composeInfo is what matters of a resolved compose for a failover.
type composeInfo struct {
	Names    map[string]bool   // service keys and container names, as a proxy host may name them
	keyOf    map[string]string // any of Names → its service key
	Ports    map[int]string    // published port → service key
	FixedIPs map[string]string // fixed IP on a macvlan → service key
	FreeIP   string            // the IP a copy must find free (a macvlan's)
	Override string            // the override for when the NPM serves no GPU container
	Notes    []string
	lan      string
	macvlans []string // networks to move to the TNAS's LAN interface
	heavy    []string // services with a GPU or devices
}

// analyzeCompose reads `docker compose config --format json`. lanIface is the
// TNAS's LAN interface, for a macvlan's parent.
func analyzeCompose(js []byte, lanIface string) (composeInfo, error) {
	var c struct {
		Name     string                    `json:"name"`
		Services map[string]composeService `json:"services"`
		Networks map[string]struct {
			Driver string `json:"driver"`
		} `json:"networks"`
	}
	if err := json.Unmarshal(js, &c); err != nil {
		return composeInfo{}, fmt.Errorf("docker compose config não deu JSON: %w", err)
	}
	info := composeInfo{Names: map[string]bool{}, keyOf: map[string]string{}, Ports: map[int]string{}, FixedIPs: map[string]string{}, lan: lanIface}
	for n, net := range c.Networks {
		if net.Driver == "macvlan" || net.Driver == "ipvlan" {
			info.macvlans = append(info.macvlans, n)
		}
	}
	slices.Sort(info.macvlans)
	keys := make([]string, 0, len(c.Services))
	for k := range c.Services {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		info.addService(c.Name, k, c.Services[k])
	}
	info.Override = info.overrideFor("")
	return info, nil
}

// composeService is what analyzeCompose reads of one service.
type composeService struct {
	ContainerName string `json:"container_name"`
	Ports         []struct {
		Published any `json:"published"`
	} `json:"ports"`
	Networks map[string]*struct {
		IPv4 string `json:"ipv4_address"`
	} `json:"networks"`
	Devices     json.RawMessage `json:"devices"`
	Gpus        json.RawMessage `json:"gpus"`
	Runtime     string          `json:"runtime"`
	NetworkMode string          `json:"network_mode"`
	Deploy      struct {
		Resources struct {
			Reservations struct {
				Devices json.RawMessage `json:"devices"`
			} `json:"reservations"`
		} `json:"resources"`
	} `json:"deploy"`
}

// hardware: the service asks for a GPU or devices of the machine it runs on.
func (s composeService) hardware() bool {
	has := func(r json.RawMessage) bool {
		v := strings.TrimSpace(string(r))
		return v != "" && v != "null" && v != "[]"
	}
	return has(s.Devices) || has(s.Gpus) || has(s.Deploy.Resources.Reservations.Devices) || s.Runtime == "nvidia"
}

func (info *composeInfo) addService(project, k string, s composeService) {
	for _, n := range []string{k, s.ContainerName} {
		if n != "" {
			info.Names[n], info.keyOf[n] = true, k
		}
	}
	if project != "" { // compose's own container names
		for _, n := range []string{project + "-" + k + "-1", project + "_" + k + "_1"} {
			info.Names[n], info.keyOf[n] = true, k
		}
	}
	for _, p := range s.Ports {
		if port, err := strconv.Atoi(fmt.Sprint(p.Published)); err == nil && port > 0 {
			info.Ports[port] = k
		}
	}
	for n, sn := range s.Networks {
		if sn != nil && sn.IPv4 != "" && slices.Contains(info.macvlans, n) {
			info.FixedIPs[sn.IPv4] = k
			if info.FreeIP == "" {
				info.FreeIP = sn.IPv4
			}
		}
	}
	if s.hardware() {
		info.heavy = append(info.heavy, k)
	}
	if s.NetworkMode == "host" {
		info.Notes = append(info.Notes, k+" usa a rede do anfitrião: no TNAS fica com as portas do TNAS")
	}
}

// overrideFor is the override for a failover of the compose, served is the
// service the NPM points to: a GPU or device container other than it is
// left off on the TNAS, and a macvlan moves to the TNAS's LAN interface.
func (c *composeInfo) overrideFor(served string) string {
	var b strings.Builder
	c.Notes = slices.DeleteFunc(c.Notes, func(n string) bool { return strings.Contains(n, "GPU") })
	var off []string
	for _, k := range c.heavy {
		if k == served {
			c.Notes = append(c.Notes, k+" usa GPU ou dispositivos do servidor: no TNAS pode não arrancar")
			continue
		}
		off = append(off, k)
		c.Notes = append(c.Notes, k+" usa GPU ou dispositivos do servidor: fica desligado no TNAS")
	}
	if len(off) > 0 {
		b.WriteString("services:\n")
		for _, k := range off {
			fmt.Fprintf(&b, "  %s:\n    profiles: [\"disabled\"]\n", k)
		}
	}
	if len(c.macvlans) > 0 && c.lan != "" {
		b.WriteString("networks:\n")
		for _, n := range c.macvlans {
			fmt.Fprintf(&b, "  %s:\n    driver_opts:\n      parent: %s\n", n, c.lan)
		}
	}
	return b.String()
}

// match finds the proxy host that serves this compose: by a service's or
// container's name, by a fixed macvlan IP, or by the server's IP and a port
// the compose publishes. It returns the first domain, all of them, and the
// service key served.
func match(hosts []proxyHost, info composeInfo, serverIP string) (host string, all []string, key string) {
	for _, h := range hosts {
		var k string
		switch {
		case info.Names[h.Server]:
			k = info.keyOf[h.Server]
		case info.FixedIPs[h.Server] != "":
			k = info.FixedIPs[h.Server]
		case isIP(h.Server) && (serverIP == "" || h.Server == serverIP) && info.Ports[h.Port] != "":
			k = info.Ports[h.Port]
		default:
			continue
		}
		if host == "" {
			host, key = h.Domains[0], k
		}
		all = append(all, h.Domains...)
	}
	return host, all, key
}

func matchHost(hosts []proxyHost, info composeInfo, serverIP string) (string, []string) {
	h, all, _ := match(hosts, info, serverIP)
	return h, all
}

// slugName is a service name from a folder's: lowercase, [a-z0-9_-] only,
// starting with a letter or a number.
func slugName(dir string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(dir) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	s := reDashes.ReplaceAllString(b.String(), "-")
	s = strings.TrimLeft(s, "-_")
	return strings.TrimRight(s, "-")
}

// discovery is what GET /api/discover answers: proposals, never saved by it.
type discovery struct {
	Network struct {
		RouterIP     string `json:"router_ip"`
		TNASIP       string `json:"tnas_ip"`
		LANIface     string `json:"lan_iface"`
		ServerIP     string `json:"server_ip"`
		NPMCheckHost string `json:"npm_check_host"`
	} `json:"network"`
	DNS struct {
		Zone  string   `json:"zone"`
		Zones []string `json:"zones"`
	} `json:"dns"`
	NPM struct {
		Found      bool `json:"found"`
		ProxyHosts int  `json:"proxy_hosts"`
	} `json:"npm"`
	Mirror struct {
		Found bool   `json:"found"`
		Root  string `json:"root"`
	} `json:"mirror"`
	Services []discoveredService `json:"services"`
}

type discoveredService struct {
	Dir           string   `json:"dir"`
	Name          string   `json:"name"`
	Host          string   `json:"host"`
	Hosts         []string `json:"hosts,omitempty"`
	OverrideYAML  string   `json:"override_yaml,omitempty"`
	RequireFreeIP string   `json:"require_free_ip,omitempty"`
	Notes         []string `json:"notes,omitempty"`
	Configured    bool     `json:"configured"`
	Error         string   `json:"error,omitempty"`
}

// mirrorDirs are the folders of the mirror with a docker-compose.yml, but the NPM's.
func mirrorDirs(root, npm string) []string {
	var out []string
	ents, _ := os.ReadDir(root) // sorted; unreadable is none
	for _, e := range ents {
		if e.IsDir() && e.Name() != npm && fileExists(filepath.Join(root, e.Name(), "docker-compose.yml")) {
			out = append(out, e.Name())
		}
	}
	return out
}

// resolveCompose is `docker compose config --format json` of a folder of the
// mirror, read by analyzeCompose.
func (a *Agent) resolveCompose(root, dir, lan string) (composeInfo, error) {
	name := cmp.Or(slugName(dir), "x")
	out, err := a.sys.Output("docker", "compose", "-p", "discover-"+name, "-f", filepath.Join(root, dir, "docker-compose.yml"), "config", "--format", "json")
	if err != nil {
		return composeInfo{}, err
	}
	return analyzeCompose([]byte(out), lan)
}

// discover gathers what the TNAS can see: its network, the NPM's proxy hosts
// and the composes in the mirror, the Technitium's zones. Outside mu: it runs
// docker compose once per folder.
func (a *Agent) discover() discovery {
	a.mu.Lock()
	c := a.cfg
	a.mu.Unlock()
	var d discovery
	route, _ := os.ReadFile(a.procRoute)
	d.Network.RouterIP = defaultRoute(route)
	d.Network.TNASIP, d.Network.LANIface = lanOf(a.ifaces(), net.ParseIP(d.Network.RouterIP))

	root := filepath.Join(c.Paths.MirrorSubvol, c.Paths.MirrorRoot)
	d.Mirror.Root = root
	_, err := os.Stat(root)
	d.Mirror.Found = err == nil
	hosts := readProxyHosts(findProxyHostDir(filepath.Join(root, c.NPM.Dir)))
	d.NPM.Found, d.NPM.ProxyHosts = len(hosts) > 0, len(hosts)

	dirs := mirrorDirs(root, c.NPM.Dir)
	infos, errs := make([]composeInfo, len(dirs)), make([]error, len(dirs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4) // a few composes at a time
	for i, dir := range dirs {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			infos[i], errs[i] = a.resolveCompose(root, dir, d.Network.LANIface)
		})
	}
	wg.Wait()

	d.Network.ServerIP = serverFrom(hosts, infos)
	a.discoverDNS(c, hosts, &d)
	if npm, err := a.resolveCompose(root, c.NPM.Dir, ""); err == nil {
		for _, h := range hosts {
			if h.Port == 81 || npm.Names[h.Server] { // the NPM's own admin
				d.Network.NPMCheckHost = h.Domains[0]
				break
			}
		}
	}

	d.Services = []discoveredService{}
	for i, dir := range dirs {
		s := discoveredService{Dir: dir, Name: slugName(dir)}
		if j := slices.IndexFunc(c.Services, func(sv Service) bool { return sv.Dir == dir }); j >= 0 {
			s.Name, s.Configured = c.Services[j].Name, true
		}
		if errs[i] != nil {
			s.Error = "o docker compose não leu o compose: " + errs[i].Error()
			d.Services = append(d.Services, s)
			continue
		}
		info := infos[i]
		var served string
		s.Host, s.Hosts, served = match(hosts, info, d.Network.ServerIP)
		s.OverrideYAML, s.RequireFreeIP = info.overrideFor(served), info.FreeIP
		s.Notes = info.Notes
		if s.Host == "" && len(hosts) > 0 {
			s.Notes = append(s.Notes, "nenhum proxy host do NPM aponta para este compose")
		}
		d.Services = append(d.Services, s)
	}
	return d
}

// serverFrom is the server's address: the IP the proxy hosts point to most,
// leaving out the fixed IPs of macvlans (those are the containers').
func serverFrom(hosts []proxyHost, infos []composeInfo) string {
	count := map[string]int{}
	for _, h := range hosts {
		fixed := slices.ContainsFunc(infos, func(i composeInfo) bool { return i.FixedIPs[h.Server] != "" })
		if isIP(h.Server) && !fixed {
			count[h.Server]++
		}
	}
	best := ""
	for ip, n := range count {
		if n > count[best] || n == count[best] && ip < best {
			best = ip
		}
	}
	return best
}

// discoverDNS asks the Technitium, with the token there is, for its zones:
// the one that holds the proxy hosts' domains, and its wildcard (the
// server's address, as the clients see it).
func (a *Agent) discoverDNS(c Config, hosts []proxyHost, d *discovery) {
	b, _ := os.ReadFile(c.DNS.TokenFile)
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return
	}
	var zl struct {
		Response struct {
			Zones []struct {
				Name     string `json:"name"`
				Type     string `json:"type"`
				Internal bool   `json:"internal"`
			} `json:"zones"`
		} `json:"response"`
	}
	if technitiumJSON(a.sys, c.DNS.APIURL, "zones/list", nil, tok, &zl) != nil {
		return
	}
	best, most := "", -1
	for _, z := range zl.Response.Zones {
		if z.Internal || z.Type != "Primary" {
			continue
		}
		d.DNS.Zones = append(d.DNS.Zones, z.Name)
		n := 0
		for _, h := range hosts {
			n += len(slices.DeleteFunc(slices.Clone(h.Domains), func(x string) bool { return !strings.HasSuffix(x, "."+z.Name) }))
		}
		if n > most {
			best, most = z.Name, n
		}
	}
	d.DNS.Zone = best
	if best == "" {
		return
	}
	var rec struct {
		Response struct {
			Records []struct {
				Type  string `json:"type"`
				RData struct {
					IPAddress string `json:"ipAddress"`
				} `json:"rData"`
			} `json:"records"`
		} `json:"response"`
	}
	if technitiumJSON(a.sys, c.DNS.APIURL, "zones/records/get", url.Values{"domain": {"*." + best}, "zone": {best}}, tok, &rec) == nil {
		for _, r := range rec.Response.Records {
			if r.Type == "A" && isIP(r.RData.IPAddress) {
				d.Network.ServerIP = r.RData.IPAddress // what the clients are sent to wins
				break
			}
		}
	}
}

// technitiumJSON calls /api/<path> of the Technitium and reads its answer into
// out; a status other than ok is the error, with its message.
func technitiumJSON(sys System, apiURL, path string, q url.Values, token string, out any) error {
	body, err := sys.Get(strings.TrimRight(apiURL, "/")+"/api/"+path+"?"+q.Encode(), token)
	if err != nil {
		return err
	}
	var st struct {
		Status       string `json:"status"`
		ErrorMessage string `json:"errorMessage"`
	}
	if err := json.Unmarshal(body, &st); err != nil {
		return fmt.Errorf("resposta inválida do Technitium: %w", err)
	}
	if st.Status != "ok" {
		return fmt.Errorf("technitium %s: %s", st.Status, st.ErrorMessage)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

func (a *Agent) getDiscover(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, a.discover())
}

// postTechnitiumLogin makes the agent's API token with the Technitium's
// login and keeps it; the password is only passed on, never kept.
func (a *Agent) postTechnitiumLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		User string `json:"user"`
		Pass string `json:"pass"`
	}
	if !decode(w, r, &req) {
		return
	}
	a.mu.Lock()
	d := a.cfg.DNS
	a.mu.Unlock()
	var out struct {
		Token    string `json:"token"`
		Response struct {
			Token string `json:"token"`
		} `json:"response"`
	}
	err := technitiumJSON(a.sys, d.APIURL, "user/createToken", url.Values{"user": {req.User}, "pass": {req.Pass}, "tokenName": {"failover-agent"}}, "", &out)
	tok := cmp.Or(out.Token, out.Response.Token)
	if err == nil && tok == "" {
		err = errors.New("o Technitium não deu um token")
	}
	if err != nil {
		http.Error(w, "o Technitium recusou: "+err.Error(), http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := writeAtomic(d.TokenFile, []byte(tok+"\n")); err != nil {
		http.Error(w, "o token foi criado no Technitium mas não consegui guardá-lo: "+err.Error(), http.StatusInternalServerError)
		return
	}
	a.now = time.Now()
	a.done(w, "", "token do Technitium criado com o utilizador "+req.User)
}
