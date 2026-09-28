package agent

import (
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func hasIP(ips []net.IP, s string) bool {
	return slices.ContainsFunc(ips, func(ip net.IP) bool { return ip.Equal(net.ParseIP(s)) })
}

// The certificate is made once and kept; it is made again near its end and
// when the TNAS's address is not in it.
func TestCertCreateReuseRenew(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	ips := []net.IP{net.ParseIP("192.168.1.249")}
	_, l1, err := loadOrCreate(dir, now, ips)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(l1.DNSNames, "localhost") || !hasIP(l1.IPAddresses, "127.0.0.1") || !hasIP(l1.IPAddresses, "192.168.1.249") {
		t.Fatalf("nomes %v, IPs %v", l1.DNSNames, l1.IPAddresses)
	}
	if fi, _ := os.Stat(filepath.Join(dir, keyName)); fi.Mode().Perm() != 0o600 {
		t.Fatalf("permissões da chave: %v", fi.Mode().Perm())
	}
	if _, l2, _ := loadOrCreate(dir, now, ips); l2.SerialNumber.Cmp(l1.SerialNumber) != 0 {
		t.Fatal("gerou outro sem precisar")
	}
	if _, l3, _ := loadOrCreate(dir, l1.NotAfter.Add(-29*24*time.Hour), ips); l3.SerialNumber.Cmp(l1.SerialNumber) == 0 {
		t.Fatal("não renovou a 29 dias do fim")
	}
	_, l4, _ := loadOrCreate(dir, now, []net.IP{net.ParseIP("10.0.0.7")})
	if !hasIP(l4.IPAddresses, "10.0.0.7") {
		t.Fatalf("o IP novo do TNAS não está no certificado: %v", l4.IPAddresses)
	}
}

// A broken pair is replaced, never a reason to start without a UI.
func TestCertBroken(t *testing.T) {
	for name, files := range map[string][2]string{
		"só a chave": {"", "lixo"},
		"lixo":       {"-----BEGIN CERTIFICATE-----\nlixo\n", "lixo"},
	} {
		dir := t.TempDir()
		if files[0] != "" {
			if err := os.WriteFile(filepath.Join(dir, certName), []byte(files[0]), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, keyName), []byte(files[1]), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, leaf, err := loadOrCreate(dir, time.Now(), nil); err != nil || leaf == nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// The healthcheck trusts the certificate the agent made.
func TestHealthcheckOwnCert(t *testing.T) {
	a, _ := setup(t)
	cfg, err := a.TLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	tsrv := httptest.NewUnstartedServer(a.Handler())
	tsrv.TLS = cfg
	tsrv.StartTLS()
	defer tsrv.Close()
	_, port, _ := net.SplitHostPort(tsrv.Listener.Addr().String())
	if err := Healthcheck("0.0.0.0:"+port, CertFile(a.statePath)); err != nil {
		t.Fatal(err)
	}
	if names, until := a.CertInfo(); !slices.Contains(names, "localhost") || until.Before(time.Now().Add(300*24*time.Hour)) {
		t.Fatalf("%v %v", names, until)
	}
}
