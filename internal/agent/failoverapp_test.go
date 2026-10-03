package agent

import (
	"slices"
	"strings"
	"testing"
)

const techAPI = "http://127.0.0.1:5380/api/"

// a cluster of the TNAS's Technitium and the server's, which state says
func cluster(state string) []byte {
	return []byte(`{"status":"ok","response":{"clusterInitialized":true,"nodes":[
		{"name":"dns-tnas.home.arpa","ipAddress":"192.168.1.249","state":"Self"},
		{"name":"dns-debian.home.arpa","ipAddress":"192.168.1.66","state":"` + state + `"}]}}`)
}

const (
	noApps       = `{"status":"ok","response":{"apps":[]}}`
	wildcardURL  = techAPI + "zones/records/get?domain=%2A.engmariz.com&zone=engmariz.com"
	wildcardA    = `{"type":"A","ttl":60,"rData":{"ipAddress":"192.168.1.66"}}`
	pingWildcard = `{"type":"APP","ttl":60,"rData":{"appName":"Failover","classPath":"Failover.Address","data":"{\"primary\":[\"192.168.1.66\"],\"secondary\":[\"192.168.1.249\"],\"serverDown\":[\"192.168.1.249\"],\"healthCheck\":\"ping\"}"}}`
)

func records(rs ...string) []byte {
	return []byte(`{"status":"ok","response":{"records":[` + strings.Join(rs, ",") + `]}}`)
}

// The readiness says what keeps the zone's * from following the server: the
// app missing on a Technitium that answers (one down is not checked), a
// native A on * (it wins over the app), an APP record that is not the
// agent's, or none.
func TestFailoverProblems(t *testing.T) {
	for _, c := range []struct {
		name   string
		bodies map[string][]byte
		want   []string
	}{
		{"como o agente o grava", nil, nil},
		{"sem a app, sem cluster", map[string][]byte{techAPI + "apps/list?": []byte(noApps)},
			[]string{"a app Failover não está instalada no Technitium; corrige em Definições → DNS"}},
		{"sem a app no servidor, que responde", map[string][]byte{techAPI + "admin/cluster/state?": cluster("Connected"),
			techAPI + "apps/list?node=dns-debian.home.arpa": []byte(noApps)},
			[]string{"a app Failover não está instalada no Technitium dns-debian.home.arpa (192.168.1.66)"}},
		{"o servidor em baixo não conta", map[string][]byte{techAPI + "admin/cluster/state?": cluster("Unreachable"),
			techAPI + "apps/list?node=dns-debian.home.arpa": []byte(noApps)}, nil},
		{"um A no * ganha à app", map[string][]byte{wildcardURL: records(wildcardA, strings.TrimSuffix(strings.TrimPrefix(goodWildcard, `{"status":"ok","response":{"records":[`), `]}}`))},
			[]string{"o *.engmariz.com tem um registo A 192.168.1.66, que ganha à app Failover"}},
		{"o teste não é o tcp443", map[string][]byte{wildcardURL: records(pingWildcard)},
			[]string{"o registo APP do *.engmariz.com não é o do failover (primário 192.168.1.66, secundário 192.168.1.249, teste tcp443)"}},
		{"só o A antigo", map[string][]byte{wildcardURL: records(wildcardA)},
			[]string{"tem um registo A 192.168.1.66", "o *.engmariz.com não é um registo da app Failover"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			a, f := setup(t)
			f.bodies = c.bodies
			got := failoverProblems(f, a.cfg, "secret")
			if len(got) != len(c.want) {
				t.Fatalf("problemas %q, esperados %q", got, c.want)
			}
			for i, w := range c.want {
				if !strings.Contains(got[i], w) {
					t.Errorf("problema %d: %q não tem %q", i, got[i], w)
				}
			}
		})
	}
}

// Definições → DNS → Configurar: the app installed from the store on each
// Technitium that lacks it, then the APP record on *, and only then the old
// A removed, so * always answers.
func TestApplyFailover(t *testing.T) {
	a, f := setup(t)
	store := []byte(`{"status":"ok","response":{"storeApps":[{"name":"Failover","url":"https://download.technitium.com/dns/apps/FailoverApp.zip"}]}}`)
	f.bodies = map[string][]byte{
		techAPI + "admin/cluster/state?":                         cluster("Connected"),
		techAPI + "apps/list?":                                   []byte(noApps),
		techAPI + "apps/list?node=dns-debian.home.arpa":          []byte(noApps),
		techAPI + "apps/listStoreApps?":                          store,
		techAPI + "apps/listStoreApps?node=dns-debian.home.arpa": store,
		wildcardURL: records(wildcardA),
	}
	if code, body := postTo(t, a.postFailoverApp, `{}`); code != 204 {
		t.Fatalf("HTTP %d %s", code, body)
	}
	f.mu.Lock()
	gets := slices.Clone(f.gets)
	f.mu.Unlock()
	at := func(sub string) int {
		return slices.IndexFunc(gets, func(u string) bool { return strings.Contains(u, sub) })
	}
	add, del := at("zones/records/add?"), at("zones/records/delete?")
	for _, want := range []string{"apps/downloadAndInstall?name=Failover&url=https", "apps/downloadAndInstall?name=Failover&node=dns-debian.home.arpa&url=https"} {
		if at(want) < 0 {
			t.Errorf("sem %s: %v", want, gets)
		}
	}
	if add < 0 || del < add {
		t.Fatalf("o APP tem de entrar antes de o A sair: add %d, delete %d", add, del)
	}
	for _, want := range []string{"appName=Failover", "classPath=Failover.Address", "overwrite=true", "ttl=60", "type=APP", "%22healthCheck%22%3A+%22tcp443%22"} {
		if !strings.Contains(gets[add], want) {
			t.Errorf("o registo gravado não tem %s: %s", want, gets[add])
		}
	}
	if !strings.Contains(gets[del], "ipAddress=192.168.1.66") || !strings.Contains(gets[del], "type=A") {
		t.Errorf("apagou outra coisa: %s", gets[del])
	}
	if !hasEvent(a, "app Failover instalada no Technitium dns-debian.home.arpa") || !hasEvent(a, "registo A 192.168.1.66 do *.engmariz.com apagado") {
		t.Errorf("eventos: %v", a.events)
	}
}
