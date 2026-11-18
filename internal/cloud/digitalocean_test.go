package cloud

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestDigitaloceanDefaults(t *testing.T) {
	p, err := Get("digitalocean")
	if err != nil || p.ID() != "digitalocean" {
		t.Fatalf("Get(digitalocean) = %v, %v", p, err)
	}
	if p.DefaultRegion() != "fra1" || p.DefaultSize() != "s-1vcpu-1gb" {
		t.Errorf("defaults = %s %s", p.DefaultRegion(), p.DefaultSize())
	}
}

func TestDigitaloceanTerraformGolden(t *testing.T) {
	got, err := (&digitalocean{}).Terraform(TFInput{
		Name:   "My_App.prod",
		Region: "fra1",
		Size:   "s-1vcpu-1gb",
		SSHPublicKeys: []string{
			"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDeployKeyDeployKeyDeployKeyDeployKey vauban-deploy",
			"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAuthorKeyAuthorKeyAuthorKeyAuthorKe me@laptop",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("..", "..", "testdata", "golden", "infra", "digitalocean.tf")
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
		`version = "~> 2.108"`,
		`resource "digitalocean_droplet" "vauban"`,
		`name = "my-app-prod"`,
		`image      = "debian-13-x64"`,
		`size       = "s-1vcpu-1gb"`,
		`region     = "fra1"`,
		`user_data  = file("${path.module}/cloud-init.yaml")`,
		`ipv6       = true`,
		`monitoring = true`,
		`ignore_changes = [user_data, ssh_keys]`,
		`droplet_ids = [digitalocean_droplet.vauban.id]`,
		`output "server_ip"`,
		`output "server_ipv6"`,
		`port_range       = "22"`, `port_range       = "80"`, `port_range       = "443"`,
		`protocol         = "udp"`,
		`protocol              = "icmp"`,
		`"0.0.0.0/0", "::/0"`,
		"me@laptop", "vauban-deploy",
	} {
		if !strings.Contains(s, w) {
			t.Errorf("missing %q", w)
		}
	}
}

func TestDigitaloceanTerraformErrors(t *testing.T) {
	ok := TFInput{Name: "a", Region: "fra1", Size: "s-1vcpu-1gb", SSHPublicKeys: []string{"ssh-ed25519 AAAA x"}}
	for name, mut := range map[string]func(*TFInput){
		"no region": func(in *TFInput) { in.Region = "" },
		"no size":   func(in *TFInput) { in.Size = "" },
		"no keys":   func(in *TFInput) { in.SSHPublicKeys = nil },
	} {
		in := ok
		mut(&in)
		if _, err := (&digitalocean{}).Terraform(in); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

// doctlHome points HOME at a fresh directory, clears every token variable and
// returns the doctl config directory under it.
func doctlHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("DIGITALOCEAN_TOKEN", "")
	t.Setenv("DIGITALOCEAN_ACCESS_TOKEN", "")
	dir := filepath.Join(home, ".config", "doctl")
	if runtime.GOOS == "darwin" {
		dir = filepath.Join(home, "Library", "Application Support", "doctl")
	}
	return dir
}

func writeDoctlConfig(t *testing.T, body string) {
	t.Helper()
	dir := doctlHome(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDigitaloceanCredentialsMissing(t *testing.T) {
	doctlHome(t)
	_, err := (&digitalocean{}).CredentialsEnv()
	if err == nil || !strings.Contains(err.Error(), "DIGITALOCEAN_TOKEN") || !strings.Contains(err.Error(), "doctl auth init") {
		t.Fatalf("err = %v", err)
	}
}

func TestDigitaloceanCredentialsEnv(t *testing.T) {
	doctlHome(t)
	t.Setenv("DIGITALOCEAN_TOKEN", "envtoken")
	got, err := (&digitalocean{}).CredentialsEnv()
	if err != nil || !reflect.DeepEqual(got, []string{"DIGITALOCEAN_TOKEN=envtoken"}) {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestDigitaloceanCredentialsAccessTokenEnv(t *testing.T) {
	doctlHome(t)
	t.Setenv("DIGITALOCEAN_ACCESS_TOKEN", "alttoken")
	got, err := (&digitalocean{}).CredentialsEnv()
	if err != nil || !reflect.DeepEqual(got, []string{"DIGITALOCEAN_TOKEN=alttoken"}) {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestDigitaloceanCredentialsDoctlConfig(t *testing.T) {
	writeDoctlConfig(t, "access-token: doctltoken\noutput: text\n")
	got, err := (&digitalocean{}).CredentialsEnv()
	if err != nil || !reflect.DeepEqual(got, []string{"DIGITALOCEAN_TOKEN=doctltoken"}) {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestDigitaloceanCredentialsDoctlConfigNoToken(t *testing.T) {
	writeDoctlConfig(t, "output: text\n")
	if _, err := (&digitalocean{}).CredentialsEnv(); err == nil || !strings.Contains(err.Error(), "DIGITALOCEAN_TOKEN") {
		t.Fatalf("err = %v", err)
	}
}

func TestDigitaloceanCredentialsDoctlConfigInvalid(t *testing.T) {
	writeDoctlConfig(t, "access-token: [secrettoken\n")
	_, err := (&digitalocean{}).CredentialsEnv()
	if err == nil || !strings.Contains(err.Error(), "config.yaml") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "secrettoken") {
		t.Errorf("token leaked in error: %v", err)
	}
}

// fakeDOAPI serves canned DigitalOcean API responses and requires a bearer token.
func fakeDOAPI(t *testing.T) *digitalocean {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/account", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"account":{"status":"active"}}`))
	})
	mux.HandleFunc("/v2/regions", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"regions":[
			{"slug":"nyc3","name":"New York 3","available":true},
			{"slug":"fra1","name":"Frankfurt 1","available":true},
			{"slug":"nyc2","name":"New York 2","available":false}
		],"links":{},"meta":{"total":3}}`))
	})
	mux.HandleFunc("/v2/sizes", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") != "2" {
			_, _ = w.Write([]byte(`{"sizes":[
				{"slug":"s-2vcpu-4gb","memory":4096,"vcpus":2,"price_monthly":24,"available":true,"regions":["fra1","nyc3"]},
				{"slug":"s-1vcpu-1gb","memory":1024,"vcpus":1,"price_monthly":6,"available":true,"regions":["fra1","nyc3"]},
				{"slug":"s-1vcpu-512mb-10gb","memory":512,"vcpus":1,"price_monthly":4,"available":true,"regions":["nyc3"]}
			],"links":{"pages":{"next":"http://x/v2/sizes?page=2","last":"http://x/v2/sizes?page=2"}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"sizes":[
			{"slug":"s-1vcpu-2gb","memory":2048,"vcpus":1,"price_monthly":12.5,"available":true,"regions":["fra1"]},
			{"slug":"s-4vcpu-8gb","memory":8192,"vcpus":4,"price_monthly":48,"available":false,"regions":["fra1"]}
		],"links":{"pages":{"first":"http://x/v2/sizes?page=1","prev":"http://x/v2/sizes?page=1"}}}`))
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer goodtoken" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"id":"unauthorized","message":"Unable to authenticate you"}`))
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	doctlHome(t)
	t.Setenv("DIGITALOCEAN_TOKEN", "goodtoken")
	return &digitalocean{endpoint: srv.URL}
}

func TestDigitaloceanCheckAuth(t *testing.T) {
	d := fakeDOAPI(t)
	if err := d.CheckAuth(context.Background()); err != nil {
		t.Fatalf("good token: %v", err)
	}
	t.Setenv("DIGITALOCEAN_TOKEN", "badtoken")
	err := d.CheckAuth(context.Background())
	if err == nil || !strings.Contains(err.Error(), "digitalocean") {
		t.Fatalf("bad token err = %v", err)
	}
	if strings.Contains(err.Error(), "badtoken") {
		t.Errorf("token leaked in error: %v", err)
	}
	t.Setenv("DIGITALOCEAN_TOKEN", "")
	if err := d.CheckAuth(context.Background()); err == nil || !strings.Contains(err.Error(), "DIGITALOCEAN_TOKEN") {
		t.Fatalf("no token err = %v", err)
	}
}

func TestDigitaloceanRegions(t *testing.T) {
	got, err := fakeDOAPI(t).Regions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Option{
		{ID: "fra1", Label: "fra1 · Frankfurt 1"},
		{ID: "nyc3", Label: "nyc3 · New York 3"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Regions = %#v", got)
	}
}

func TestDigitaloceanSizes(t *testing.T) {
	got, err := fakeDOAPI(t).Sizes(context.Background(), "fra1")
	if err != nil {
		t.Fatal(err)
	}
	want := []Option{
		{ID: "s-1vcpu-1gb", Label: "s-1vcpu-1gb · 1 vCPU · 1 GB · $6/mo"},
		{ID: "s-1vcpu-2gb", Label: "s-1vcpu-2gb · 1 vCPU · 2 GB · $12.50/mo"},
		{ID: "s-2vcpu-4gb", Label: "s-2vcpu-4gb · 2 vCPU · 4 GB · $24/mo"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Sizes = %#v", got)
	}
}

func TestBearerClientScopedToAPIHost(t *testing.T) {
	var got []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, "api:"+r.Header.Get("Authorization"))
	}))
	defer api.Close()
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, "other:"+r.Header.Get("Authorization"))
	}))
	defer other.Close()
	c := bearerClient("tok", api.URL)
	for _, u := range []string{api.URL, other.URL} {
		resp, err := c.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	want := []string{"api:Bearer tok", "other:"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// fakeDOQuota serves /v2/account and a droplet count for Quota.
func fakeDOQuota(t *testing.T, limit, droplets int) *digitalocean {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/account", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{"account":{"droplet_limit":%d,"email":"me@example.com","status":"active"}}`, limit)
	})
	mux.HandleFunc("/v2/droplets", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{"droplets":[],"links":{},"meta":{"total":%d}}`, droplets)
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer goodtoken" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	doctlHome(t)
	t.Setenv("DIGITALOCEAN_TOKEN", "goodtoken")
	return &digitalocean{endpoint: srv.URL}
}

func TestDigitaloceanQuota(t *testing.T) {
	got, err := fakeDOQuota(t, 3, 2).Quota(context.Background(), "fra1", "s-1vcpu-1gb", false)
	if err != nil || !got.OK {
		t.Fatalf("2 of 3: %+v, %v", got, err)
	}
	got, err = fakeDOQuota(t, 3, 3).Quota(context.Background(), "fra1", "s-1vcpu-1gb", false)
	if err != nil {
		t.Fatal(err)
	}
	want := "digitalocean: droplet limit reached (3 of 3 in use) — delete a droplet or request a higher limit at https://cloud.digitalocean.com/account/team (Droplet limit → Increase)"
	if got.OK || got.Reason != want {
		t.Errorf("3 of 3: %+v", got)
	}
	if strings.Contains(got.Reason, "goodtoken") {
		t.Error("token leaked")
	}
}

// A resize or replacement adds no droplet: N of N is fine, over the limit is not.
func TestDigitaloceanQuotaReplacing(t *testing.T) {
	got, err := fakeDOQuota(t, 3, 3).Quota(context.Background(), "fra1", "s-2vcpu-4gb", true)
	if err != nil || !got.OK {
		t.Errorf("replacing at 3 of 3: %+v, %v", got, err)
	}
	got, err = fakeDOQuota(t, 3, 4).Quota(context.Background(), "fra1", "s-2vcpu-4gb", true)
	if err != nil || got.OK || !strings.Contains(got.Reason, "4 of 3 in use") {
		t.Errorf("replacing at 4 of 3: %+v, %v", got, err)
	}
}
