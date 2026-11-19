package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jean-baptisteAZAN/vauban/internal/config"
	"github.com/jean-baptisteAZAN/vauban/internal/engine"
)

// serverFake answers every command of an existing-server apply, plan and
// destroy: the audit (its only argument is the project) gets the fixture,
// every other script and gh call succeeds with no output.
type serverFake struct {
	mu    sync.Mutex
	audit string
	calls []engine.Cmd
	stdin []string
}

func (s *serverFake) Run(_ context.Context, c engine.Cmd) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var in []byte
	if c.Stdin != nil {
		in, _ = io.ReadAll(c.Stdin)
	}
	s.calls = append(s.calls, c)
	s.stdin = append(s.stdin, string(in))
	if a := scriptArgs(c); c.Name == "ssh" && len(a) == 1 && a[0] == "shop" {
		return []byte(s.audit), nil
	}
	return nil, nil
}

func (s *serverFake) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls, s.stdin = nil, nil
}

// sshCalls returns the script arguments of every ssh call.
func (s *serverFake) sshCalls() [][]string {
	var out [][]string
	for _, c := range s.calls {
		if c.Name == "ssh" {
			out = append(out, scriptArgs(c))
		}
	}
	return out
}

// destinations lists the user@host of every ssh call.
func (s *serverFake) destinations() []string {
	var out []string
	for _, c := range s.calls {
		if c.Name == "ssh" {
			i := slices.Index(c.Args, "--")
			out = append(out, c.Args[i+1])
		}
	}
	return out
}

// siteWrites returns the site files written (decoded), in order.
func (s *serverFake) siteWrites(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, a := range s.sshCalls() {
		if len(a) == 7 && a[0] == "write" {
			b, err := base64.StdEncoding.DecodeString(a[6])
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, string(b))
		}
	}
	return out
}

func (s *serverFake) secretsSet() []string {
	var out []string
	for _, c := range s.calls {
		if c.Name == "gh" && len(c.Args) > 2 && c.Args[0] == "secret" && c.Args[1] == "set" {
			out = append(out, c.Args[2])
		}
	}
	return out
}

// appliedExisting runs a first existing-server apply and returns the root.
func appliedExisting(t *testing.T) (string, *serverFake) {
	t.Helper()
	root, _ := applyEnv(t)
	bin := t.TempDir()
	writeFile(t, bin, "ssh", "#!/bin/sh\nexit 1\n")
	if err := os.Chmod(filepath.Join(bin, "ssh"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	home, _ := os.UserHomeDir()
	writeFile(t, home, ".ssh/k", "PRIVATE KEY\n")
	b, err := os.ReadFile("../remote/testdata/audit-user-caddy-ready.txt")
	if err != nil {
		t.Fatal(err)
	}
	f := &serverFake{audit: string(b)}
	setRunner(t, f)
	// ssh pins the host on its first connect; the fake does not.
	writeFile(t, root, ".vauban/known_hosts", "203.0.113.10 ssh-ed25519 AAAA\n")
	out, err := execVauban(t, context.Background(), append([]string{"apply", "--dir", root}, existingFlags...)...)
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	st, err := engine.LoadState(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Outputs["server_target"]; got != "jb@203.0.113.10:22" {
		t.Fatalf("server_target = %q", got)
	}
	if got := st.Outputs["server_site"]; got != "a.example.com|8080" {
		t.Fatalf("server_site = %q", got)
	}
	f.reset()
	return root, f
}

func TestExistingDomainChangeReappliesSite(t *testing.T) {
	root, f := appliedExisting(t)

	out, err := execPlan(t, root, "--domain", "b.example.com")
	if err != nil {
		t.Fatalf("plan: %v\n%s", err, out)
	}
	for _, want := range []string{
		"+ write the Caddy site for b.example.com on 203.0.113.10 and reload Caddy\n",
		"[secret] gh secret set DEPLOY_DOMAIN --repo acme/shop\n",
		"[secret] gh secret set DEPLOY_HOST --repo acme/shop\n",
		"a.example.com|8080 → b.example.com|8080",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plan missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Caddy site for b.example.com on 203.0.113.10 and reload Caddy (done)") {
		t.Errorf("site shown as done:\n%s", out)
	}

	f.reset()
	out, err = execVauban(t, context.Background(), "apply", "--dir", root, "--domain", "b.example.com", "--yes")
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	sites := f.siteWrites(t)
	if len(sites) != 1 || !strings.HasPrefix(sites[0], "b.example.com {") {
		t.Errorf("site writes = %q", sites)
	}
	secrets := f.secretsSet()
	for _, want := range []string{"DEPLOY_DOMAIN", "DEPLOY_HOST"} {
		if !slices.Contains(secrets, want) {
			t.Errorf("secret %s not set again: %q", want, secrets)
		}
	}
	st, _ := engine.LoadState(root)
	if got := st.Outputs["server_site"]; got != "b.example.com|8080" {
		t.Errorf("server_site = %q", got)
	}

	// Nothing changed since: the site is done again.
	f.reset()
	out, err = execVauban(t, context.Background(), "apply", "--dir", root, "--yes")
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	if got := f.siteWrites(t); len(got) != 0 {
		t.Errorf("unchanged apply rewrote the site: %q", got)
	}
}

func TestExistingPortChangeReappliesSite(t *testing.T) {
	root, f := appliedExisting(t)
	out, err := execVauban(t, context.Background(), "apply", "--dir", root, "--port", "9090", "--yes")
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	sites := f.siteWrites(t)
	if len(sites) != 1 || !strings.Contains(sites[0], "reverse_proxy shop-app:9090") {
		t.Errorf("site writes = %q", sites)
	}
}

func TestExistingHostChangeRefused(t *testing.T) {
	for _, args := range [][]string{
		{"--host", "198.51.100.1"},
		{"--ssh-port", "2222"},
	} {
		t.Run(args[0], func(t *testing.T) {
			root, f := appliedExisting(t)
			before, _ := os.ReadFile(filepath.Join(root, config.FileName))
			for _, cmd := range []string{"plan", "apply"} {
				full := append([]string{cmd, "--dir", root, "--yes"}, args...)
				out, err := execVauban(t, context.Background(), full...)
				if err == nil {
					t.Fatalf("%s: want refusal\n%s", cmd, out)
				}
				for _, want := range []string{"deployed on 203.0.113.10:22", "migrating between servers is not supported", "vauban destroy"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("%s: error %q lacks %q", cmd, err, want)
					}
				}
				after, _ := os.ReadFile(filepath.Join(root, config.FileName))
				if !bytes.Equal(before, after) {
					t.Errorf("%s: vauban.yaml changed:\n%s", cmd, after)
				}
			}
			if len(f.calls) != 0 {
				t.Errorf("commands ran: %d", len(f.calls))
			}
		})
	}
}

func TestExistingToCloudRefused(t *testing.T) {
	root, f := appliedExisting(t)
	before, _ := os.ReadFile(filepath.Join(root, config.FileName))
	out, err := execPlan(t, root, "--target", "cloud", "--cloud", "hetzner")
	if err == nil || !strings.Contains(err.Error(), "existing → cloud") || !strings.Contains(err.Error(), "vauban destroy") {
		t.Fatalf("want refusal, got %v\n%s", err, out)
	}
	after, _ := os.ReadFile(filepath.Join(root, config.FileName))
	if !bytes.Equal(before, after) {
		t.Errorf("vauban.yaml changed:\n%s", after)
	}
	if len(f.calls) != 0 {
		t.Errorf("commands ran: %d", len(f.calls))
	}
}

// Before any server step is done, switching back to cloud is fine.
func TestExistingToCloudAllowedBeforeApply(t *testing.T) {
	root, _ := planEnv(t, "go-app")
	fakeAudit(t, "audit-user-caddy-ready.txt", nil)
	if out, err := execPlan(t, root, existingFlags...); err != nil {
		t.Fatalf("plan: %v\n%s", err, out)
	}
	if out, err := execPlan(t, root, "--target", "cloud", "--cloud", "hetzner"); err != nil {
		t.Fatalf("plan: %v\n%s", err, out)
	}
}

func TestExistingDestroyUsesRecordedTarget(t *testing.T) {
	root, f := appliedExisting(t)
	// vauban.yaml edited by hand to another server: destroy still cleans up
	// the one the project is deployed on.
	y, _ := os.ReadFile(filepath.Join(root, config.FileName))
	edited := strings.Replace(string(y), "203.0.113.10", "198.51.100.1", 1)
	writeFile(t, root, config.FileName, edited)
	out, err := execVauban(t, context.Background(), "destroy", "--dir", root, "--confirm", "shop")
	if err != nil {
		t.Fatalf("destroy: %v\n%s", err, out)
	}
	dests := f.destinations()
	if len(dests) == 0 {
		t.Fatal("no ssh call")
	}
	for _, d := range dests {
		if d != "jb@203.0.113.10" {
			t.Errorf("ssh to %s, want jb@203.0.113.10", d)
		}
	}
	if !strings.Contains(out, "198.51.100.1") || !strings.Contains(out, "deployed on 203.0.113.10:22") {
		t.Errorf("destroy must say which server it uses:\n%s", out)
	}
}

func TestDestroyCloudConfigOnExistingStateRefused(t *testing.T) {
	root, f := appliedExisting(t)
	writeFile(t, root, config.FileName, "project: shop\ncloud: hetzner\napp_port: 8080\n")
	out, err := execVauban(t, context.Background(), "destroy", "--dir", root, "--confirm", "shop")
	if err == nil || !strings.Contains(err.Error(), "deployed on 203.0.113.10:22") {
		t.Fatalf("want refusal, got %v\n%s", err, out)
	}
	if len(f.calls) != 0 || !exists(statePath(root)) {
		t.Errorf("destroy acted: %d calls", len(f.calls))
	}
}

// audits counts the audit runs.
func (s *serverFake) audits() int {
	n := 0
	for _, a := range s.sshCalls() {
		if len(a) == 1 && a[0] == "shop" {
			n++
		}
	}
	return n
}

// existingApplyEnv is a project ready for a first existing-server apply whose
// server answers the audit with fixture.
func existingApplyEnv(t *testing.T, fixture string) (string, *serverFake) {
	t.Helper()
	root, f := appliedExisting(t)
	if err := os.Remove(statePath(root)); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("../remote/testdata/" + fixture)
	if err != nil {
		t.Fatal(err)
	}
	f.audit = string(b)
	f.reset()
	return root, f
}

func TestApplyExistingShowsAuditBeforePlan(t *testing.T) {
	root, f := existingApplyEnv(t, "audit-user-caddy-ready.txt")
	out, err := execVauban(t, context.Background(), "apply", "--dir", root, "--yes")
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	i, j := strings.Index(out, "Docker engine"), strings.Index(out, "\nserver\n")
	if i < 0 || j < 0 || i > j {
		t.Errorf("audit table must come before the plan:\n%s", out)
	}
	// One audit before the prompt, one in the server:audit step.
	if n := f.audits(); n != 2 {
		t.Errorf("audits = %d, want 2: %q", n, f.sshCalls())
	}
	// Re-apply: server:audit is done, only the pre-prompt audit runs.
	f.reset()
	if out, err := execVauban(t, context.Background(), "apply", "--dir", root, "--yes"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	if n := f.audits(); n != 1 {
		t.Errorf("re-apply audits = %d, want 1", n)
	}
}

func TestApplyExistingStopsOnBlockers(t *testing.T) {
	for _, tc := range []struct{ fixture, want string }{
		{"audit-nginx.txt", "nginx"},
		{"audit-user-caddy-baked.txt", "make this one-time change"},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			root, f := existingApplyEnv(t, tc.fixture)
			out, err := execVauban(t, context.Background(), "apply", "--dir", root, "--yes")
			wantExit(t, err, 1, "", out)
			if !strings.Contains(out, tc.want) || !strings.Contains(out, "Docker engine") {
				t.Errorf("output lacks the audit or %q:\n%s", tc.want, out)
			}
			if strings.Contains(out, "\nserver\n") {
				t.Errorf("plan shown although apply cannot run:\n%s", out)
			}
			if n := len(f.sshCalls()); n != 1 || f.audits() != 1 {
				t.Errorf("ssh calls = %v, want only the audit", f.sshCalls())
			}
			if len(f.secretsSet()) != 0 {
				t.Errorf("secrets set: %v", f.secretsSet())
			}
		})
	}
}

func TestExistingKnownHostsChangeResetsSecret(t *testing.T) {
	root, f := appliedExisting(t)
	out, err := execPlan(t, root)
	if err != nil || !strings.Contains(out, "✓ gh secret set DEPLOY_KNOWN_HOSTS --repo acme/shop (done)") {
		t.Fatalf("unchanged plan: %v\n%s", err, out)
	}
	writeFile(t, root, ".vauban/known_hosts", "203.0.113.10 ssh-ed25519 NEWKEY\n")
	out, err = execPlan(t, root)
	if err != nil || !strings.Contains(out, "[secret] gh secret set DEPLOY_KNOWN_HOSTS --repo acme/shop\n") {
		t.Fatalf("plan: %v\n%s", err, out)
	}
	f.reset()
	if out, err := execVauban(t, context.Background(), "apply", "--dir", root, "--yes"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	if got := f.secretsSet(); !slices.Equal(got, []string{"DEPLOY_KNOWN_HOSTS"}) {
		t.Errorf("secrets set = %q", got)
	}
}

// First applied as root, then root ssh is turned off: a new ssh user on the
// same host and port is allowed and re-runs only the audit.
func TestExistingUserChangeAllowed(t *testing.T) {
	root, f := appliedExisting(t)
	out, err := execPlan(t, root, "--ssh-user", "admin")
	if err != nil {
		t.Fatalf("plan: %v\n%s", err, out)
	}
	if !strings.Contains(out, "+ audit admin@203.0.113.10 over ssh (read-only)\n") ||
		!strings.Contains(out, "✓ create ~deploy/apps/shop with an owner marker on 203.0.113.10 (done)") {
		t.Errorf("want only the audit pending:\n%s", out)
	}
	f.reset()
	if out, err := execVauban(t, context.Background(), "apply", "--dir", root, "--yes"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	for _, d := range f.destinations() {
		if d != "admin@203.0.113.10" {
			t.Errorf("ssh to %s", d)
		}
	}
	if got := f.siteWrites(t); len(got) != 0 {
		t.Errorf("site rewritten: %q", got)
	}
	st, _ := engine.LoadState(root)
	if got := st.Outputs["server_target"]; got != "admin@203.0.113.10:22" {
		t.Errorf("server_target = %q", got)
	}
}

// destroy keeps the recorded host and port but connects as vauban.yaml's
// user (a hand edit, not applied yet).
func TestExistingDestroyUsesConfigUser(t *testing.T) {
	root, f := appliedExisting(t)
	y, _ := os.ReadFile(filepath.Join(root, config.FileName))
	writeFile(t, root, config.FileName, strings.Replace(string(y), "user: jb", "user: admin", 1))
	if out, err := execVauban(t, context.Background(), "destroy", "--dir", root, "--confirm", "shop"); err != nil {
		t.Fatalf("destroy: %v\n%s", err, out)
	}
	dests := f.destinations()
	if len(dests) == 0 {
		t.Fatal("no ssh call")
	}
	for _, d := range dests {
		if d != "admin@203.0.113.10" {
			t.Errorf("ssh to %s, want admin@203.0.113.10", d)
		}
	}
}
