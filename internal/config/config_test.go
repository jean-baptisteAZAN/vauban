package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func valid() *Config {
	return &Config{Project: "demo", Cloud: "scaleway", Region: "fr-par", Size: "DEV1-S", Domain: "example.com", AppPort: 3000}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := valid()
	if err := want.Save(dir); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if *got != *want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestLoadMissing(t *testing.T) {
	c, err := Load(t.TempDir())
	if c != nil || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("got %v, %v", c, err)
	}
}

func TestValidate(t *testing.T) {
	if err := valid().Validate(); err != nil {
		t.Fatal(err)
	}
	empty := valid()
	empty.Domain = ""
	if err := empty.Validate(); err != nil {
		t.Fatalf("empty domain must be valid: %v", err)
	}
	for _, c := range []string{"scaleway", "digitalocean", "hetzner"} {
		v := valid()
		v.Cloud = c
		if err := v.Validate(); err != nil {
			t.Fatalf("%s: %v", c, err)
		}
	}
	tests := []struct {
		name string
		mod  func(*Config)
		msg  string
	}{
		{"cloud", func(c *Config) { c.Cloud = "aws" }, `unsupported cloud "aws"`},
		{"port zero", func(c *Config) { c.AppPort = 0 }, "app_port"},
		{"port high", func(c *Config) { c.AppPort = 70000 }, "app_port"},
		{"project", func(c *Config) { c.Project = "" }, "project"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := valid()
			tc.mod(c)
			err := c.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.msg) {
				t.Fatalf("got %v, want containing %q", err, tc.msg)
			}
		})
	}
}

func TestValidateDomain(t *testing.T) {
	for _, d := range []string{"example.com", "app.example.co.uk", "xn--bcher-kva.example", "a-b.c1.io", "1-2-3-4.sslip.io"} {
		c := valid()
		c.Domain = d
		if err := c.Validate(); err != nil {
			t.Errorf("%q: %v", d, err)
		}
	}
	for _, d := range []string{
		"localhost", "example", "https://example.com", "example.com/", "-a.example.com", "a-.example.com",
		"a..example.com", ".example.com", "example.com.", "ex ample.com", "exa_mple.com", "*.example.com",
		"example.com:443", strings.Repeat("a", 64) + ".com", strings.Repeat("a.", 127) + "com",
	} {
		c := valid()
		c.Domain = d
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "domain") {
			t.Errorf("%q: got %v, want a domain error", d, err)
		}
	}
}

func existing() *Config {
	return &Config{
		Project: "demo", Target: TargetExisting, Domain: "app.example.com", AppPort: 3000,
		Server: &Server{Host: "myvps", User: "root", Key: "~/.ssh/id"},
	}
}

func TestValidateExisting(t *testing.T) {
	if err := existing().Validate(); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"203.0.113.7", "2001:db8::1", "vps.example.com"} {
		c := existing()
		c.Server.Host = h
		if err := c.Validate(); err != nil {
			t.Fatalf("host %s: %v", h, err)
		}
	}
	c := existing()
	c.Server.Key = "agent"
	c.Server.Port = 2222
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		mod  func(*Config)
		msg  string
	}{
		{"no server", func(c *Config) { c.Server = nil }, "server"},
		{"host", func(c *Config) { c.Server.Host = "" }, "server.host"},
		{"bad host", func(c *Config) { c.Server.Host = "bad host!" }, "server.host"},
		{"user", func(c *Config) { c.Server.User = "" }, "server.user"},
		{"key", func(c *Config) { c.Server.Key = "" }, "server.key"},
		{"port high", func(c *Config) { c.Server.Port = 70000 }, "server.port"},
		{"port negative", func(c *Config) { c.Server.Port = -1 }, "server.port"},
		{"domain", func(c *Config) { c.Domain = "" }, "domain"},
		{"bad domain", func(c *Config) { c.Domain = "nodot" }, "domain"},
		{"cloud", func(c *Config) { c.Cloud = "hetzner" }, "cloud"},
		{"region", func(c *Config) { c.Region = "fr-par" }, "region"},
		{"size", func(c *Config) { c.Size = "S" }, "size"},
		{"target", func(c *Config) { c.Target = "k8s" }, `target "k8s"`},
		{"app_port", func(c *Config) { c.AppPort = 0 }, "app_port"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := existing()
			tt.mod(c)
			err := c.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.msg) {
				t.Fatalf("got %v, want error containing %q", err, tt.msg)
			}
		})
	}
}

func TestValidateCloudUnchanged(t *testing.T) {
	c := valid()
	if c.Existing() {
		t.Fatal("no target must be cloud")
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.Target = TargetCloud
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.Target = ""
	dir := t.TempDir()
	if err := c.Save(dir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"target:", "server:"} {
		if strings.Contains(string(data), k) {
			t.Fatalf("saved cloud config contains %q:\n%s", k, data)
		}
	}
	bad := valid()
	bad.Cloud = ""
	if err := bad.Validate(); err == nil {
		t.Fatal("cloud target without cloud must fail")
	}
}

func TestExistingRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := existing()
	if err := want.Save(dir); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, FileName))
	if strings.Contains(string(data), "cloud:") || strings.Contains(string(data), "port: 0") {
		t.Fatalf("unexpected keys:\n%s", data)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Existing() || *got.Server != *want.Server {
		t.Fatalf("got %+v", got)
	}
}

func TestSSHPort(t *testing.T) {
	if p := (&Server{}).SSHPort(); p != 22 {
		t.Fatal(p)
	}
	if p := (&Server{Port: 2222}).SSHPort(); p != 2222 {
		t.Fatal(p)
	}
}

func TestKeyPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := (&Server{Key: "~/.ssh/id"}).KeyPath()
	if err != nil || got != filepath.Join(home, ".ssh/id") {
		t.Fatalf("got %q, %v", got, err)
	}
	got, err = (&Server{Key: "agent"}).KeyPath()
	if err != nil || got != "" {
		t.Fatalf("agent: got %q, %v", got, err)
	}
	got, err = (&Server{Key: "/abs/key"}).KeyPath()
	if err != nil || got != "/abs/key" {
		t.Fatalf("abs: got %q, %v", got, err)
	}
}
