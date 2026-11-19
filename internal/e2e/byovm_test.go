//go:build e2e

// Package e2e drives vauban's existing-server code against a throwaway Debian
// host (sshd + Docker) running in a privileged container.
package e2e

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jean-baptisteAZAN/vauban/internal/config"
	"github.com/jean-baptisteAZAN/vauban/internal/detect"
	"github.com/jean-baptisteAZAN/vauban/internal/engine"
	"github.com/jean-baptisteAZAN/vauban/internal/module/server"
	"github.com/jean-baptisteAZAN/vauban/internal/remote"
)

const (
	image   = "vauban-e2e-test"
	sites   = "/opt/vauban/caddy/sites"
	caddyID = "vauban-caddy"
)

func sh(t *testing.T, ctx context.Context, name string, args ...string) string {
	t.Helper()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// startHost builds the image, starts the container and waits for sshd.
func startHost(t *testing.T, ctx context.Context, pub string) (container string, port int) {
	t.Helper()
	sh(t, ctx, "docker", "build", "-q", "-t", image, "testdata")
	container = sh(t, ctx, "docker", "run", "-d", "--rm", "--privileged", "--label", "vauban-e2e",
		"-e", "OPS_PUBKEY="+pub, "-p", "127.0.0.1::22", image)
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", container).Run()
	})
	p := sh(t, ctx, "docker", "port", container, "22/tcp")
	port, err := strconv.Atoi(p[strings.LastIndex(p, ":")+1:])
	if err != nil {
		t.Fatalf("port %q: %v", p, err)
	}
	return container, port
}

type host struct {
	t      *testing.T
	ctx    context.Context
	run    engine.Runner
	target remote.Target
}

// as runs a script as the admin user and returns its stdout.
func (h host) as(script string, args ...string) (string, error) {
	out, err := remote.Exec(h.ctx, h.run, h.target, script, args...)
	return string(out), err
}

func (h host) must(script string, args ...string) string {
	h.t.Helper()
	out, err := h.as(script, args...)
	if err != nil {
		h.t.Fatalf("remote %q: %v\n%s", script, err, out)
	}
	return strings.TrimSpace(out)
}

func (h host) mustEq(want, script string) {
	h.t.Helper()
	if got := h.must(script); got != want {
		h.t.Fatalf("%s\n got: %q\nwant: %q", script, got, want)
	}
}

func steps(t *testing.T, root string, srv *config.Server, project, repo, domain string) []engine.Step {
	t.Helper()
	c := &config.Config{Project: project, Target: config.TargetExisting, Domain: domain, AppPort: 8080, Server: srv}
	s, err := server.New().Plan(&detect.Project{Root: root, GitHubRepo: repo}, c)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func execute(t *testing.T, ctx context.Context, root string, s []engine.Step, run engine.Runner) (*engine.State, error) {
	t.Helper()
	st, err := engine.LoadState(root)
	if err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	env := &engine.Env{Root: root, Run: run, In: engine.Outputs{}, Log: &log,
		Progress: func(id, _ string, ph engine.Phase, err error) {
			t.Logf("  %-24s %s %v", id, ph, err)
		}}
	return st, engine.Execute(ctx, s, st, env)
}

func TestBringYourOwnVM(t *testing.T) {
	if os.Getenv("VAUBAN_E2E") != "1" {
		t.Skip("set VAUBAN_E2E=1 to run (needs Docker, starts a privileged container)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// Keep the docker CLI's config (contexts) while HOME moves to a temp dir.
	if os.Getenv("DOCKER_CONFIG") == "" {
		if h, err := os.UserHomeDir(); err == nil {
			t.Setenv("DOCKER_CONFIG", filepath.Join(h, ".docker"))
		}
	}
	t.Setenv("HOME", t.TempDir())
	keyDir := t.TempDir()
	adminKey := filepath.Join(keyDir, "admin")
	sh(t, ctx, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", adminKey)
	pubBytes, err := os.ReadFile(adminKey + ".pub")
	if err != nil {
		t.Fatal(err)
	}

	container, port := startHost(t, ctx, strings.TrimSpace(string(pubBytes)))
	srv := &config.Server{Host: "127.0.0.1", Port: port, User: "ops", Key: adminKey}
	root := t.TempDir()
	tg, err := remote.TargetFor(root, srv)
	if err != nil {
		t.Fatal(err)
	}
	run := engine.ExecRunner{}
	h := host{t: t, ctx: ctx, run: run, target: tg}

	// Wait for sshd and dockerd (the entrypoint starts sshd last).
	deadline := time.Now().Add(3 * time.Minute)
	for {
		if _, err := h.as("true"); err == nil {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("host never became reachable: %v\n%s", err, sh(t, ctx, "docker", "logs", container))
		}
		time.Sleep(2 * time.Second)
	}

	const (
		slug    = "acme/shop"
		project = "shop"
		domain  = "shop.localhost"
	)

	// 1. read-only audit.
	t.Log("1. audit")
	baseline := sh(t, ctx, "docker", "diff", container)
	au, err := remote.RunAudit(ctx, run, tg, project, slug)
	if err != nil {
		t.Fatal(err)
	}
	f := au.Facts
	if f.OSID != "debian" || !f.Sudo || !f.Docker || f.DeployUser || au.Caddy != remote.CaddyInstall {
		t.Fatalf("unexpected facts: %+v caddy=%v", f, au.Caddy)
	}
	if len(au.Blockers()) > 0 {
		t.Fatalf("blockers: %v", au.Blockers())
	}
	// A second audit, then compare the container filesystem with the one taken
	// before the first: only the engine's own runtime state may differ.
	if _, err := remote.RunAudit(ctx, run, tg, project, slug); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, l := range strings.Split(baseline, "\n") {
		seen[l] = true
	}
	allowed := []string{"/tmp", "/run", "/var/log", "/var/lib/docker", "/opt/containerd", "/var/lib/sudo",
		// the container runtime injects its CA certificate after start
		"/etc/ca-certificates", "/etc/ssl/certs", "/usr/local/share/ca-certificates"}
	dirs := map[string]bool{"/etc": true, "/etc/ssl": true, "/usr": true, "/usr/local": true, "/usr/local/share": true}
	for _, l := range strings.Split(sh(t, ctx, "docker", "diff", container), "\n") {
		if l == "" || seen[l] {
			continue
		}
		p := strings.TrimSpace(l[1:])
		ok := dirs[p] && l[0] == 'C'
		for _, a := range allowed {
			ok = ok || p == a || strings.HasPrefix(p, a+"/")
		}
		if !ok {
			t.Errorf("audit changed %s", l)
		}
	}

	// 2. server steps.
	t.Log("2. apply")
	st, err := execute(t, ctx, root, steps(t, root, srv, project, slug, domain), run)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if st.Outputs["server_caddy_mode"] != "install" {
		t.Fatalf("caddy mode %q", st.Outputs["server_caddy_mode"])
	}
	checkApplied := func() {
		t.Helper()
		h.mustEq("deploy", `id -un deploy`)
		if g := h.must(`id -nG deploy`); !strings.Contains(" "+g+" ", " docker ") {
			t.Fatalf("deploy groups: %s", g)
		}
		h.mustEq("1", `sudo -n grep -c '# vauban:shop$' /home/deploy/.ssh/authorized_keys`)
		h.mustEq("1", `sudo -n grep -c . /home/deploy/.ssh/authorized_keys`)
		h.mustEq("vauban", `sudo -n docker network ls --format '{{.Name}}' | grep -x vauban`)
		h.mustEq("true", `sudo -n docker inspect -f '{{.State.Running}}' vauban-caddy`)
		h.mustEq(slug, `sudo -n cat /home/deploy/apps/shop/.vauban-owner`)
		h.mustEq(strings.TrimSpace(server.RenderSite(project, domain, 8080)), `sudo -n cat `+sites+`/shop.caddy`)
		h.mustEq("1", `sudo -n sh -c 'ls /home/deploy/apps/shop/.vauban-site | wc -l'`)
		h.mustEq("1", `sudo -n sh -c 'ls `+sites+` | wc -l'`)
		h.mustEq("shop.caddy", `sudo -n ls `+sites)
	}
	checkApplied()

	// 3. resume with a fresh state: idempotent.
	t.Log("3. re-apply with a fresh state")
	root2 := t.TempDir()
	if err := os.CopyFS(filepath.Join(root2, ".vauban"), os.DirFS(filepath.Join(root, ".vauban"))); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root2, ".vauban", "state.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := execute(t, ctx, root2, steps(t, root2, srv, project, slug, domain), run); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	checkApplied()

	// 4. a foreign site file is never overwritten.
	t.Log("4. foreign site")
	h.must(`sudo -n sh -c 'printf "blog.localhost {\n\trespond hi\n}\n" > ` + sites + `/blog.caddy'`)
	want := h.must(`sudo -n cat ` + sites + `/blog.caddy`)
	root3 := t.TempDir()
	if _, err := execute(t, ctx, root3, steps(t, root3, srv, "blog", "other/blog", "blog.localhost"), run); err == nil {
		t.Fatal("applying over a foreign site file must fail")
	} else {
		t.Logf("refused: %v", err)
		if !strings.Contains(err.Error(), "not written by vauban") {
			t.Errorf("unexpected refusal: %v", err)
		}
	}
	h.mustEq(want, `sudo -n cat `+sites+`/blog.caddy`)

	// 5. destroy shop: plan then run, as the CLI does.
	t.Log("5. destroy")
	args := func(action string) []string {
		return []string{action, "sudo", sites, project, slug, caddyID, "no"}
	}
	for _, a := range []string{"plan", "run"} {
		out, err := remote.Exec(ctx, run, tg, server.DestroyScript(), args(a)...)
		if err != nil {
			t.Fatalf("destroy %s: %v\n%s", a, err, out)
		}
		t.Logf("destroy %s: %s", a, strings.TrimSpace(string(out)))
	}
	if out := h.must(`sudo -n test -e /home/deploy/apps/shop || echo gone`); out != "gone" {
		t.Errorf("project dir still there")
	}
	if out := h.must(`sudo -n test -e ` + sites + `/shop.caddy || echo gone`); out != "gone" {
		t.Errorf("site file still there")
	}
	h.mustEq("0", `sudo -n grep -c '# vauban:shop$' /home/deploy/.ssh/authorized_keys || true`)
	h.mustEq("true", `sudo -n docker inspect -f '{{.State.Running}}' vauban-caddy`)
	h.mustEq("vauban", `sudo -n docker network ls --format '{{.Name}}' | grep -x vauban`)
	h.mustEq("deploy", `id -un deploy`)
	h.mustEq(want, `sudo -n cat `+sites+`/blog.caddy`)
	fmt.Fprintln(os.Stderr, "e2e ok")
}
