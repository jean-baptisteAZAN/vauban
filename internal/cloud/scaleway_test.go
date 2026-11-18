package cloud

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const (
	testSCWAccess  = "SCWXXXXXXXXXXXXXXXXX"
	testSCWSecret  = "11111111-2222-3333-4444-555555555555"
	testSCWProject = "66666666-7777-8888-9999-000000000000"
)

func TestScalewayDefaults(t *testing.T) {
	p, err := Get("scaleway")
	if err != nil || p.ID() != "scaleway" {
		t.Fatalf("Get(scaleway) = %v, %v", p, err)
	}
	if p.DefaultRegion() != "fr-par-1" || p.DefaultSize() != "PLAY2-PICO" {
		t.Errorf("defaults = %s %s", p.DefaultRegion(), p.DefaultSize())
	}
}

func TestScalewayTerraformGolden(t *testing.T) {
	got, err := (&scaleway{}).Terraform(TFInput{
		Name:   "My_App.prod",
		Region: "fr-par-1",
		Size:   "PLAY2-PICO",
		SSHPublicKeys: []string{
			"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDeployKeyDeployKeyDeployKeyDeployKey vauban-deploy",
			"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAuthorKeyAuthorKeyAuthorKeyAuthorKe me@laptop",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("..", "..", "testdata", "golden", "infra", "scaleway.tf")
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
		`version = "~> 2.92"`,
		`zone   = "fr-par-1"`,
		`region = "fr-par"`,
		`resource "scaleway_instance_server" "vauban"`,
		`name = "my-app-prod"`,
		`type              = "PLAY2-PICO"`,
		`image             = "debian_trixie"`,
		`resource "scaleway_iam_ssh_key" "vauban"`,
		`type = "routed_ipv4"`, `type = "routed_ipv6"`,
		`ip_ids            = [scaleway_instance_ip.ipv4.id, scaleway_instance_ip.ipv6.id]`,
		`security_group_id = scaleway_instance_security_group.vauban.id`,
		`user_data = {`,
		`cloud-init = file("${path.module}/cloud-init.yaml")`,
		`ignore_changes = [user_data]`,
		`inbound_default_policy  = "drop"`,
		`outbound_default_policy = "accept"`,
		`ip_range = "0.0.0.0/0"`, `ip_range = "::/0"`,
		`protocol = "UDP"`,
		`output "server_ip"`, `output "server_ipv6"`,
		"me@laptop", "vauban-deploy",
	} {
		if !strings.Contains(s, w) {
			t.Errorf("missing %q", w)
		}
	}
	for p, n := range map[string]int{`port     = 22`: 2, `port     = 80`: 2, `port     = 443`: 4} { // IPv4+IPv6 (+UDP for 443)
		if strings.Count(s, p) != n {
			t.Errorf("want %q %d times", p, n)
		}
	}
}

func TestScalewayTerraformRegionFromZone(t *testing.T) {
	got, err := (&scaleway{}).Terraform(TFInput{Name: "a", Region: "nl-ams-2", Size: "PLAY2-PICO", SSHPublicKeys: []string{"ssh-ed25519 AAAA x"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `zone   = "nl-ams-2"`) || !strings.Contains(string(got), `region = "nl-ams"`) {
		t.Errorf("zone/region wrong:\n%s", got)
	}
}

func TestScalewayTerraformErrors(t *testing.T) {
	ok := TFInput{Name: "a", Region: "fr-par-1", Size: "PLAY2-PICO", SSHPublicKeys: []string{"ssh-ed25519 AAAA x"}}
	for name, mut := range map[string]func(*TFInput){
		"no region":  func(in *TFInput) { in.Region = "" },
		"bad region": func(in *TFInput) { in.Region = "paris" },
		"no size":    func(in *TFInput) { in.Size = "" },
		"no keys":    func(in *TFInput) { in.SSHPublicKeys = nil },
	} {
		in := ok
		mut(&in)
		if _, err := (&scaleway{}).Terraform(in); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

// scwHome isolates the scw config: SCW_CONFIG_PATH points at a (missing)
// file in a temp dir and every SCW_* variable is cleared. It returns that path.
func scwHome(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("SCW_CONFIG_PATH", path)
	for _, k := range []string{"SCW_ACCESS_KEY", "SCW_SECRET_KEY", "SCW_DEFAULT_PROJECT_ID", "SCW_DEFAULT_ZONE", "SCW_PROFILE"} {
		t.Setenv(k, "")
	}
	return path
}

func writeScwConfig(t *testing.T, body string) {
	t.Helper()
	path := scwHome(t)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestScalewayCredentialsMissing(t *testing.T) {
	scwHome(t)
	_, err := (&scaleway{}).CredentialsEnv()
	if err == nil || !strings.Contains(err.Error(), "SCW_ACCESS_KEY") || !strings.Contains(err.Error(), "scw init") {
		t.Fatalf("err = %v", err)
	}
}

func TestScalewayCredentialsEnv(t *testing.T) {
	scwHome(t)
	t.Setenv("SCW_ACCESS_KEY", testSCWAccess)
	t.Setenv("SCW_SECRET_KEY", testSCWSecret)
	t.Setenv("SCW_DEFAULT_PROJECT_ID", testSCWProject)
	got, err := (&scaleway{}).CredentialsEnv()
	want := []string{"SCW_ACCESS_KEY=" + testSCWAccess, "SCW_SECRET_KEY=" + testSCWSecret, "SCW_DEFAULT_PROJECT_ID=" + testSCWProject}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, %v", got, err)
	}
	t.Setenv("SCW_DEFAULT_ZONE", "nl-ams-1")
	got, err = (&scaleway{}).CredentialsEnv()
	if err != nil || !reflect.DeepEqual(got, append(want, "SCW_DEFAULT_ZONE=nl-ams-1")) {
		t.Fatalf("with zone: got %v, %v", got, err)
	}
}

func TestScalewayCredentialsEnvPartialMissing(t *testing.T) {
	scwHome(t)
	t.Setenv("SCW_ACCESS_KEY", testSCWAccess)
	t.Setenv("SCW_SECRET_KEY", testSCWSecret)
	_, err := (&scaleway{}).CredentialsEnv()
	if err == nil || !strings.Contains(err.Error(), "SCW_DEFAULT_PROJECT_ID") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), testSCWSecret) {
		t.Errorf("secret leaked: %v", err)
	}
}

func TestScalewayCredentialsProfile(t *testing.T) {
	writeScwConfig(t, "access_key: "+testSCWAccess+"\nsecret_key: "+testSCWSecret+"\ndefault_project_id: "+testSCWProject+"\ndefault_zone: fr-par-2\n")
	got, err := (&scaleway{}).CredentialsEnv()
	want := []string{"SCW_ACCESS_KEY=" + testSCWAccess, "SCW_SECRET_KEY=" + testSCWSecret, "SCW_DEFAULT_PROJECT_ID=" + testSCWProject, "SCW_DEFAULT_ZONE=fr-par-2"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestScalewayCredentialsActiveProfileAndEnvOverride(t *testing.T) {
	writeScwConfig(t, "access_key: "+testSCWAccess+"\nsecret_key: "+testSCWSecret+"\ndefault_project_id: "+testSCWProject+
		"\nactive_profile: work\nprofiles:\n  work:\n    access_key: SCWWORKWORKWORKWORK1\n    secret_key: aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee\n")
	got, err := (&scaleway{}).CredentialsEnv()
	if err != nil || got[0] != "SCW_ACCESS_KEY=SCWWORKWORKWORKWORK1" || got[2] != "SCW_DEFAULT_PROJECT_ID="+testSCWProject {
		t.Fatalf("active profile: got %v, %v", got, err)
	}
	t.Setenv("SCW_ACCESS_KEY", testSCWAccess)
	got, _ = (&scaleway{}).CredentialsEnv()
	if got[0] != "SCW_ACCESS_KEY="+testSCWAccess {
		t.Errorf("env should win over profile: %v", got)
	}
}

func TestScalewayCredentialsProfileIncomplete(t *testing.T) {
	writeScwConfig(t, "access_key: "+testSCWAccess+"\n")
	if _, err := (&scaleway{}).CredentialsEnv(); err == nil || !strings.Contains(err.Error(), "SCW_SECRET_KEY") {
		t.Fatalf("err = %v", err)
	}
}

func TestScalewayCredentialsConfigInvalidNoLeak(t *testing.T) {
	writeScwConfig(t, "secret_key: [supersecretvalue\n")
	_, err := (&scaleway{}).CredentialsEnv()
	if err == nil || !strings.Contains(err.Error(), "config.yaml") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "supersecretvalue") {
		t.Errorf("secret leaked in error: %v", err)
	}
}

func TestScalewayBadKeyFormatNoLeak(t *testing.T) {
	scwHome(t)
	t.Setenv("SCW_ACCESS_KEY", testSCWAccess)
	t.Setenv("SCW_SECRET_KEY", "not-a-uuid-topsecret")
	t.Setenv("SCW_DEFAULT_PROJECT_ID", testSCWProject)
	err := (&scaleway{}).CheckAuth(context.Background())
	if err == nil {
		t.Fatal("want error")
	}
	if strings.Contains(err.Error(), "topsecret") {
		t.Errorf("secret leaked in error: %v", err)
	}
}

// fakeSCWAPI serves canned Scaleway API responses and requires the secret key.
func fakeSCWAPI(t *testing.T) *scaleway {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/instance/v1/zones/fr-par-1/servers", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"servers":[]}`))
	})
	mux.HandleFunc("/instance/v1/zones/fr-par-1/products/servers/availability", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"total_count":6,"servers":{
			"PLAY2-PICO":{"availability":"available"},
			"PLAY2-NANO":{"availability":"scarce"},
			"PRO2-XXS":{"availability":"available"},
			"STARDUST1-S":{"availability":"shortage"},
			"COPARM1-2C-8G":{"availability":"available"},
			"RETIRED-1":{"availability":"available"}
		}}`))
	})
	mux.HandleFunc("/instance/v1/zones/fr-par-1/products/servers", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"total_count":7,"servers":{
			"PRO2-XXS":{"ncpus":2,"ram":8589934592,"arch":"x86_64","hourly_price":0.0168,"monthly_price":12.26},
			"PLAY2-NANO":{"ncpus":2,"ram":4294967296,"arch":"x86_64","hourly_price":0.0103,"monthly_price":7.5},
			"PLAY2-PICO":{"ncpus":1,"ram":2147483648,"arch":"x86_64","hourly_price":0.0055},
			"STARDUST1-S":{"ncpus":1,"ram":1073741824,"arch":"x86_64","hourly_price":0.0001},
			"COPARM1-2C-8G":{"ncpus":2,"ram":8589934592,"arch":"arm64","hourly_price":0.01},
			"RETIRED-1":{"ncpus":1,"ram":1073741824,"arch":"x86_64","hourly_price":0.001,"end_of_service":true},
			"UNLISTED-1":{"ncpus":1,"ram":1073741824,"arch":"x86_64","hourly_price":0.001}
		}}`))
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("X-Auth-Token") != testSCWSecret {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"type":"denied_authentication","message":"authentication required"}`))
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	scwHome(t)
	t.Setenv("SCW_ACCESS_KEY", testSCWAccess)
	t.Setenv("SCW_SECRET_KEY", testSCWSecret)
	t.Setenv("SCW_DEFAULT_PROJECT_ID", testSCWProject)
	return &scaleway{endpoint: srv.URL}
}

func TestScalewayCheckAuth(t *testing.T) {
	s := fakeSCWAPI(t)
	if err := s.CheckAuth(context.Background()); err != nil {
		t.Fatalf("good key: %v", err)
	}
	t.Setenv("SCW_SECRET_KEY", "99999999-9999-9999-9999-999999999999")
	err := s.CheckAuth(context.Background())
	if err == nil || !strings.Contains(err.Error(), "scaleway") {
		t.Fatalf("bad key err = %v", err)
	}
	if strings.Contains(err.Error(), "99999999-9999") {
		t.Errorf("secret leaked in error: %v", err)
	}
	t.Setenv("SCW_SECRET_KEY", "")
	if err := s.CheckAuth(context.Background()); err == nil || !strings.Contains(err.Error(), "SCW_ACCESS_KEY") {
		t.Fatalf("no key err = %v", err)
	}
}

func TestScalewayRegions(t *testing.T) {
	got, err := (&scaleway{}).Regions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0].ID != "fr-par-1" {
		t.Fatalf("Regions = %#v", got)
	}
	for _, o := range got {
		if !strings.HasPrefix(o.Label, o.ID+" · ") {
			t.Errorf("label %q", o.Label)
		}
	}
}

func TestScalewaySizes(t *testing.T) {
	got, err := fakeSCWAPI(t).Sizes(context.Background(), "fr-par-1")
	if err != nil {
		t.Fatal(err)
	}
	want := []Option{
		{ID: "PLAY2-PICO", Label: "PLAY2-PICO · 1 vCPU · 2 GB · €3.96/mo"},
		{ID: "PLAY2-NANO", Label: "PLAY2-NANO · 2 vCPU · 4 GB · €7.50/mo"},
		{ID: "PRO2-XXS", Label: "PRO2-XXS · 2 vCPU · 8 GB · €12.26/mo"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Sizes = %#v", got)
	}
}
