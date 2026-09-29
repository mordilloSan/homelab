package agent

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
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
	has := func(r json.RawMessage) bool {
		v := strings.TrimSpace(string(r))
		return v != "" && v != "null" && v != "[]"
	}
	if has(s.Devices) || has(s.Gpus) || has(s.Deploy.Resources.Reservations.Devices) || s.Runtime == "nvidia" {
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
