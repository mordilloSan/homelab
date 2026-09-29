package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Each rule names the field it rejects, so the UI can show the error next to it.
func TestValidateFields(t *testing.T) {
	base, err := LoadConfig("testdata/failover.yml")
	if err != nil {
		t.Fatal(err)
	}
	if err := base.validate(); err != nil {
		t.Fatalf("a config por defeito não valida: %v", err)
	}
	for _, c := range []struct {
		field string
		set   func(*Config)
	}{
		{"server.ip", func(c *Config) { c.Server.IP = "192.168.1" }},
		{"tnas_ip", func(c *Config) { c.TNASIP = "tnas" }},
		{"router_ip", func(c *Config) { c.RouterIP = "" }},
		{"server.npm_check_host", func(c *Config) { c.Server.NPMCheckHost = "https://nginx.engmariz.com" }},
		{"lan_iface", func(c *Config) { c.LANIface = "ovs eth0" }},
		{"dns.api_url", func(c *Config) { c.DNS.APIURL = "127.0.0.1:5380" }},
		{"dns.zone", func(c *Config) { c.DNS.Zone = "eng mariz" }},
		{"dns.ttl", func(c *Config) { c.DNS.TTL = 0 }},
		{"paths.mirror_subvol", func(c *Config) { c.Paths.MirrorSubvol = "Volume1/ServerBackup" }},
		{"paths.mirror_root", func(c *Config) { c.Paths.MirrorRoot = "../x" }},
		{"paths.snapshots_dir", func(c *Config) { c.Paths.SnapshotsDir = "snaps" }},
		{"paths.overrides_dir", func(c *Config) { c.Paths.OverridesDir = "o" }},
		{"npm.dir", func(c *Config) { c.NPM.Dir = "/npm" }},
		{"maintenance.default_expiry_min", func(c *Config) { c.Maintenance.DefaultExpiryMin = 20000 }},
		{"ui.listen", func(c *Config) { c.UI.Listen = "8099" }},
		{"nightly.prepull_at", func(c *Config) { c.Nightly.PrepullAt = "25:00" }},
		{"start_timeout_min", func(c *Config) { c.StartTimeoutMin = 0 }},
		{"check_interval_s", func(c *Config) { c.CheckIntervalS = 5 }},
		{"mode", func(c *Config) { c.Mode = "x" }},
	} {
		next := base
		c.set(&next)
		var fe *FieldError
		if err := next.validate(); !errors.As(err, &fe) || fe.Field != c.field {
			t.Errorf("%s: %v", c.field, err)
		}
	}
}

// A file without maintenance.default_expiry_min reads it as 60.
func TestLoadZeroExpiry(t *testing.T) {
	b, err := os.ReadFile("testdata/failover.yml")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "f.yml")
	if err = os.WriteFile(p, []byte(strings.Replace(string(b), "default_expiry_min: 60", "default_expiry_min: 0", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, err := LoadConfig(p); err != nil || c.Maintenance.DefaultExpiryMin != 60 {
		t.Fatalf("%v %d", err, c.Maintenance.DefaultExpiryMin)
	}
}

// The shipped config loads as it is, with no services: the discovery proposes them.
func TestShippedConfig(t *testing.T) {
	c, err := LoadConfig("config/failover.yml")
	if err != nil || len(c.Services) != 0 {
		t.Fatalf("%v %+v", err, c.Services)
	}
}
