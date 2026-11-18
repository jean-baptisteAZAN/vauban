package config

import (
	"errors"
	"io/fs"
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
