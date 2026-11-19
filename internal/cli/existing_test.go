package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jean-baptisteAZAN/vauban/internal/config"
	"github.com/jean-baptisteAZAN/vauban/internal/engine"
	"github.com/jean-baptisteAZAN/vauban/internal/module"
)

var existingFlags = []string{"--target", "existing", "--host", "203.0.113.10", "--ssh-user", "jb",
	"--ssh-key", "~/.ssh/k", "--domain", "a.example.com", "--yes"}

// fakeAudit installs an audit runner returning the fixture (or err).
func fakeAudit(t *testing.T, fixture string, err error) *auditFake {
	t.Helper()
	b, rerr := os.ReadFile("../remote/testdata/" + fixture)
	if rerr != nil {
		t.Fatal(rerr)
	}
	fr := &auditFake{out: string(b), err: err}
	old := cmdRunner
	cmdRunner = fr
	t.Cleanup(func() { cmdRunner = old })
	return fr
}

func TestPlanExistingNonInteractive(t *testing.T) {
	root, prov := planEnv(t, "go-app")
	fr := fakeAudit(t, "audit-user-caddy-ready.txt", nil)
	out, err := execPlan(t, root, existingFlags...)
	if err != nil {
		t.Fatalf("plan: %v\n%s", err, out)
	}
	raw, _ := os.ReadFile(filepath.Join(root, "vauban.yaml"))
	y := string(raw)
	for _, want := range []string{"target: existing", "host: 203.0.113.10", "user: jb", "key: ~/.ssh/k"} {
		if !strings.Contains(y, want) {
			t.Errorf("vauban.yaml missing %q:\n%s", want, y)
		}
	}
	for _, bad := range []string{"cloud:", "region:", "size:"} {
		if strings.Contains(y, bad) {
			t.Errorf("vauban.yaml has %q:\n%s", bad, y)
		}
	}
	audit, steps, ok := strings.Cut(out, "server\n")
	if !ok || !strings.Contains(audit, "Docker engine") {
		t.Errorf("audit table must be above the steps:\n%s", out)
	}
	_ = steps
	if !strings.Contains(out, "DNS: point a.example.com to 203.0.113.10") {
		t.Errorf("no DNS line:\n%s", out)
	}
	if len(fr.calls) != 1 {
		t.Errorf("audit ran %d times, want 1", len(fr.calls))
	}
	if prov.quotaCalls != 0 {
		t.Error("quota checked in existing mode")
	}
}

func TestPlanExistingGuide(t *testing.T) {
	root, _ := planEnv(t, "go-app")
	fakeAudit(t, "audit-user-caddy-baked.txt", nil)
	out, err := execPlan(t, root, existingFlags...)
	if err != nil {
		t.Fatalf("plan: %v\n%s", err, out)
	}
	if !strings.Contains(out, "import /etc/caddy/sites/*.caddy") {
		t.Errorf("no caddy guide:\n%s", out)
	}
}

func TestPlanExistingBlockersInWarnings(t *testing.T) {
	root, _ := planEnv(t, "go-app")
	fakeAudit(t, "audit-nginx.txt", nil)
	out, err := execPlan(t, root, existingFlags...)
	if err != nil {
		t.Fatalf("plan must exit 0: %v\n%s", err, out)
	}
	_, after, ok := strings.Cut(out, "before apply:")
	if !ok || !strings.Contains(after, "port 80 held by nginx") {
		t.Errorf("blocker not in before apply:\n%s", out)
	}
}

func TestPlanExistingSSHFailure(t *testing.T) {
	root, _ := planEnv(t, "go-app")
	fakeAudit(t, "audit-user-caddy-ready.txt", errors.New("ssh: connect refused"))
	out, err := execPlan(t, root, existingFlags...)
	if err != nil {
		t.Fatalf("plan must exit 0: %v\n%s", err, out)
	}
	_, after, ok := strings.Cut(out, "before apply:")
	if !ok || !strings.Contains(after, "connect refused") {
		t.Errorf("ssh error not in before apply:\n%s", out)
	}
	if !strings.Contains(out, "server:") && !strings.Contains(out, "server\n") {
		t.Errorf("steps not printed:\n%s", out)
	}
}

func TestPlanExistingMissingFlags(t *testing.T) {
	root, _ := planEnv(t, "go-app")
	fakeAudit(t, "audit-user-caddy-ready.txt", nil)
	_, err := execPlan(t, root, "--target", "existing", "--host", "h.example.com", "--yes")
	if exitCode(err) != 2 {
		t.Fatalf("want exit 2, got %v", err)
	}
	for _, want := range []string{"--ssh-user", "--ssh-key", "--domain"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %s: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "--host") {
		t.Errorf("--host was given: %v", err)
	}
}

func TestPlanTargetSwitchRefusedWhileInfraApplied(t *testing.T) {
	root, _ := planEnv(t, "go-app")
	fakeAudit(t, "audit-user-caddy-ready.txt", nil)
	cloudYAML := "project: shop\ncloud: hetzner\nregion: nbg1\nsize: cx23\ndomain: \"\"\napp_port: 8080\n"
	writeFile(t, root, "vauban.yaml", cloudYAML)
	saveState(t, root, &engine.State{Done: map[string]bool{}, Outputs: engine.Outputs{"infra_fingerprint": "hetzner/nbg1/cx23"}})
	_, err := execPlan(t, root, existingFlags...)
	if err == nil || !strings.Contains(err.Error(), "target") {
		t.Fatalf("want refusal naming target, got %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(root, "vauban.yaml"))
	if string(raw) != cloudYAML {
		t.Errorf("vauban.yaml changed:\n%s", raw)
	}
}

func TestPlanSwitchToCloudClearsServer(t *testing.T) {
	root, _ := planEnv(t, "go-app")
	fakeAudit(t, "audit-user-caddy-ready.txt", nil)
	writeFile(t, root, "vauban.yaml", "project: shop\ntarget: existing\ndomain: a.example.com\napp_port: 8080\nserver:\n  host: 203.0.113.10\n  user: jb\n  key: agent\n")
	if out, err := execPlan(t, root, "--target", "cloud", "--cloud", "hetzner"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	c, _ := config.Load(root)
	if c.Server != nil || c.Existing() || c.Cloud != "hetzner" {
		t.Errorf("unexpected config %+v", c)
	}
}

func TestPlanSwitchToExistingClearsCloud(t *testing.T) {
	root, _ := planEnv(t, "go-app")
	fakeAudit(t, "audit-user-caddy-ready.txt", nil)
	writeFile(t, root, "vauban.yaml", "project: shop\ncloud: hetzner\nregion: nbg1\nsize: cx23\ndomain: \"\"\napp_port: 8080\n")
	if out, err := execPlan(t, root, existingFlags...); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	c, _ := config.Load(root)
	if c.Cloud != "" || c.Region != "" || c.Size != "" || !c.Existing() {
		t.Errorf("unexpected config %+v", c)
	}
}

func TestSSHKeyChoices(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, f := range []string{"id_ed25519", "id_ed25519.pub", "lonely", "known_hosts", "work", "work.pub"} {
		writeFile(t, home, ".ssh/"+f, "x")
	}
	got, err := sshKeyChoices()
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, c := range got {
		ids = append(ids, c.ID)
	}
	if strings.Join(ids, ",") != "~/.ssh/id_ed25519,~/.ssh/work" {
		t.Errorf("ids = %v", ids)
	}
}

func TestFooterExisting(t *testing.T) {
	c := &config.Config{Target: config.TargetExisting, Domain: "a.example.com"}
	f := footer(c, &engine.State{Done: map[string]bool{}, Outputs: engine.Outputs{}}, nil)
	if f.Domain != "a.example.com" {
		t.Errorf("domain = %q", f.Domain)
	}
}

func TestDoctorTarget(t *testing.T) {
	root, _ := planEnv(t, "go-app")
	writeFile(t, root, "vauban.yaml", existingYAML)
	got := doctorOutArgs(t, "doctor", "--dir", root)
	if !strings.Contains(got, "existing (ops@203.0.113.7)") {
		t.Errorf("no target row:\n%s", got)
	}
	if !strings.Contains(got, "server") || strings.Contains(got, "infra") {
		t.Errorf("modules row wrong for existing:\n%s", got)
	}

	writeFile(t, root, "vauban.yaml", "project: shop\ncloud: hetzner\ndomain: \"\"\napp_port: 8080\n")
	got = doctorOutArgs(t, "doctor", "--dir", root)
	if strings.Contains(got, "server") || !strings.Contains(got, "infra") {
		t.Errorf("modules row wrong for cloud:\n%s", got)
	}
	_ = module.All
}
