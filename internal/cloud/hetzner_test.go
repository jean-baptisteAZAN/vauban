package cloud

import (
	"context"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "regenerate golden files")

func TestRegistry(t *testing.T) {
	if got := IDs(); !reflect.DeepEqual(got, []string{"hetzner"}) {
		t.Errorf("IDs() = %v", got)
	}
	p, err := Get("hetzner")
	if err != nil || p.ID() != "hetzner" {
		t.Fatalf("Get(hetzner) = %v, %v", p, err)
	}
	if p.DefaultRegion() != "nbg1" || p.DefaultSize() != "cx23" {
		t.Errorf("defaults = %s %s", p.DefaultRegion(), p.DefaultSize())
	}
	if _, err := Get("aws"); err == nil || err.Error() != `unknown cloud "aws"` {
		t.Errorf("Get(aws) err = %v", err)
	}
}

func TestHetznerTerraformGolden(t *testing.T) {
	got, err := (&hetzner{}).Terraform(TFInput{
		Name:   "My_App.prod",
		Region: "nbg1",
		Size:   "cx23",
		SSHPublicKeys: []string{
			"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDeployKeyDeployKeyDeployKeyDeployKey vauban-deploy",
			"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAuthorKeyAuthorKeyAuthorKeyAuthorKe me@laptop",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("..", "..", "testdata", "golden", "infra", "hetzner.tf")
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("mismatch with %s:\n%s", path, got)
	}
	s := string(got)
	for _, w := range []string{
		`version = "~> 1.70"`,
		`resource "hcloud_server" "vauban"`,
		`name = "my-app-prod"`,
		`image        = "debian-13"`,
		`user_data    = file("${path.module}/cloud-init.yaml")`,
		`firewall_ids = [hcloud_firewall.vauban.id]`,
		`output "server_ip"`,
		`output "server_ipv6"`,
		`port        = "22"`, `port        = "80"`, `port        = "443"`,
		`protocol    = "udp"`,
		`"0.0.0.0/0", "::/0"`,
		"me@laptop", "vauban-deploy",
	} {
		if !strings.Contains(s, w) {
			t.Errorf("missing %q", w)
		}
	}
}

func TestHetznerTerraformErrors(t *testing.T) {
	ok := TFInput{Name: "a", Region: "nbg1", Size: "cx23", SSHPublicKeys: []string{"ssh-ed25519 AAAA x"}}
	for name, mut := range map[string]func(*TFInput){
		"no region": func(in *TFInput) { in.Region = "" },
		"no size":   func(in *TFInput) { in.Size = "" },
		"no keys":   func(in *TFInput) { in.SSHPublicKeys = nil },
	} {
		in := ok
		mut(&in)
		if _, err := (&hetzner{}).Terraform(in); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestHCLStringEscapes(t *testing.T) {
	got := hclString("a\"b\\c ${x} %{y}\n")
	if want := `"a\"b\\c $${x} %%{y}\n"`; got != want {
		t.Errorf("hclString = %s, want %s", got, want)
	}
}

func TestSanitizeName(t *testing.T) {
	for in, want := range map[string]string{
		"My_App.prod": "my-app-prod",
		"--x--y--":    "x-y",
		"":            "vauban",
		"!!!":         "vauban",
		"ok-name-1":   "ok-name-1",
	} {
		if got := sanitizeName(in); got != want {
			t.Errorf("sanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHetznerCredentialsMissing(t *testing.T) {
	t.Setenv("HCLOUD_TOKEN", "")
	t.Setenv("HOME", t.TempDir())
	_, err := (&hetzner{}).CredentialsEnv()
	if err == nil || !strings.Contains(err.Error(), "HCLOUD_TOKEN") || !strings.Contains(err.Error(), "hcloud context create") {
		t.Fatalf("err = %v", err)
	}
}

func TestHetznerCredentialsEnv(t *testing.T) {
	t.Setenv("HCLOUD_TOKEN", "envtoken")
	t.Setenv("HOME", t.TempDir())
	got, err := (&hetzner{}).CredentialsEnv()
	if err != nil || !reflect.DeepEqual(got, []string{"HCLOUD_TOKEN=envtoken"}) {
		t.Fatalf("got %v, %v", got, err)
	}
}

func writeCLIConfig(t *testing.T, body string) {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "hcloud")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cli.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("HCLOUD_TOKEN", "")
}

func TestHetznerCredentialsCLIConfig(t *testing.T) {
	writeCLIConfig(t, `active_context = "work"

[[contexts]]
  name = "perso"
  token = "persotoken"

[[contexts]]
  name = "work"
  token = "worktoken"
`)
	got, err := (&hetzner{}).CredentialsEnv()
	if err != nil || !reflect.DeepEqual(got, []string{"HCLOUD_TOKEN=worktoken"}) {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestHetznerCredentialsCLIConfigNoActive(t *testing.T) {
	writeCLIConfig(t, `[[contexts]]
  name = "perso"
  token = "persotoken"
`)
	if _, err := (&hetzner{}).CredentialsEnv(); err == nil || !strings.Contains(err.Error(), "HCLOUD_TOKEN") {
		t.Fatalf("err = %v", err)
	}
}

func TestHetznerCredentialsCLIConfigInvalid(t *testing.T) {
	writeCLIConfig(t, "active_context = [")
	if _, err := (&hetzner{}).CredentialsEnv(); err == nil || !strings.Contains(err.Error(), "cli.toml") {
		t.Fatalf("err = %v", err)
	}
}

// fakeAPI serves canned hcloud API responses and requires a bearer token.
func fakeAPI(t *testing.T) *hetzner {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/locations", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"locations":[
			{"id":2,"name":"nbg1","description":"Nuremberg DC Park 1","country":"DE","city":"Nuremberg"},
			{"id":1,"name":"fsn1","description":"Falkenstein DC Park 1","country":"DE","city":"Falkenstein"}
		]}`))
	})
	mux.HandleFunc("/server_types", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"server_types":[
			{"id":115,"name":"cx33","cores":4,"memory":8,"disk":80,"cpu_type":"shared","architecture":"x86",
			 "prices":[{"location":"nbg1","price_monthly":{"net":"5.4900000000","gross":"6.5331000000"}}],
			 "locations":[{"id":2,"name":"nbg1","available":true}]},
			{"id":114,"name":"cx23","cores":2,"memory":4,"disk":40,"cpu_type":"shared","architecture":"x86",
			 "prices":[{"location":"nbg1","price_monthly":{"net":"3.4900000000","gross":"4.1531000000"}}],
			 "locations":[{"id":2,"name":"nbg1","available":true}]},
			{"id":200,"name":"cax11","cores":2,"memory":4,"disk":40,"cpu_type":"shared","architecture":"arm",
			 "prices":[{"location":"nbg1","price_monthly":{"net":"3.2900000000","gross":"3.9100000000"}}],
			 "locations":[{"id":2,"name":"nbg1","available":true}]},
			{"id":96,"name":"ccx13","cores":2,"memory":8,"disk":80,"cpu_type":"dedicated","architecture":"x86",
			 "prices":[{"location":"nbg1","price_monthly":{"net":"12.4900000000","gross":"14.8600000000"}}],
			 "locations":[{"id":2,"name":"nbg1","available":true}]},
			{"id":104,"name":"cx22","cores":2,"memory":4,"disk":40,"cpu_type":"shared","architecture":"x86",
			 "prices":[{"location":"nbg1","price_monthly":{"net":"3.7900000000","gross":"4.5100000000"}}],
			 "locations":[{"id":2,"name":"nbg1","available":true,
			   "deprecation":{"announced":"2025-10-16T00:00:00Z","unavailable_after":"2026-01-01T00:00:00Z"}}]},
			{"id":109,"name":"cpx22","cores":2,"memory":4,"disk":80,"cpu_type":"shared","architecture":"x86",
			 "prices":[{"location":"fsn1","price_monthly":{"net":"5.9900000000","gross":"7.1300000000"}}],
			 "locations":[{"id":1,"name":"fsn1","available":true},{"id":2,"name":"nbg1","available":false}]},
			{"id":110,"name":"cpx32","cores":4,"memory":8,"disk":160,"cpu_type":"shared","architecture":"x86",
			 "prices":[],
			 "locations":[{"id":2,"name":"nbg1","available":true}]}
		]}`))
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer goodtoken" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":{"code":"unauthorized","message":"unable to authenticate"}}`))
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("HCLOUD_TOKEN", "goodtoken")
	t.Setenv("HOME", t.TempDir())
	return &hetzner{endpoint: srv.URL}
}

func TestHetznerCheckAuth(t *testing.T) {
	h := fakeAPI(t)
	if err := h.CheckAuth(context.Background()); err != nil {
		t.Fatalf("good token: %v", err)
	}
	t.Setenv("HCLOUD_TOKEN", "badtoken")
	err := h.CheckAuth(context.Background())
	if err == nil || !strings.Contains(err.Error(), "hetzner") {
		t.Fatalf("bad token err = %v", err)
	}
	t.Setenv("HCLOUD_TOKEN", "")
	if err := h.CheckAuth(context.Background()); err == nil || !strings.Contains(err.Error(), "HCLOUD_TOKEN") {
		t.Fatalf("no token err = %v", err)
	}
}

func TestHetznerRegions(t *testing.T) {
	got, err := fakeAPI(t).Regions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Option{
		{ID: "fsn1", Label: "fsn1 · Falkenstein, DE"},
		{ID: "nbg1", Label: "nbg1 · Nuremberg, DE"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Regions = %#v", got)
	}
}

func TestHetznerSizes(t *testing.T) {
	got, err := fakeAPI(t).Sizes(context.Background(), "nbg1")
	if err != nil {
		t.Fatal(err)
	}
	want := []Option{
		{ID: "cx23", Label: "cx23 · 2 vCPU · 4 GB · €3.49/mo"},
		{ID: "cx33", Label: "cx33 · 4 vCPU · 8 GB · €5.49/mo"},
		{ID: "cpx32", Label: "cpx32 · 4 vCPU · 8 GB"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Sizes = %#v", got)
	}
}
