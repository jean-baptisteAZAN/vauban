package remote

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/jean-baptisteAZAN/vauban/internal/engine"
)

func fixture(t *testing.T, name string) Facts {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	f, err := ParseFacts(b)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestParseFacts(t *testing.T) {
	f := fixture(t, "audit-fresh-ubuntu.txt")
	if f.OSID != "ubuntu" || f.OSLike != "debian" || !f.Sudo || f.Docker || f.Caddy != nil ||
		f.Firewall != "inactive" || f.SSHPasswordAuth != "yes" || f.DiskFreeGB != 40 || f.PendingUpdates != 12 {
		t.Fatalf("%+v", f)
	}
	f = fixture(t, "audit-user-caddy-ready.txt")
	c := f.Caddy
	if c == nil || c.Container != "caddy" || c.VaubanManaged || c.SitesHostDir != "/srv/proxy/sites" ||
		c.CaddyfileHostPath != "/srv/proxy/Caddyfile" || !c.HasImport || !c.OnNetwork {
		t.Fatalf("%+v", c)
	}
	if f.Ports[80] != "caddy" || f.Ports[443] != "caddy" || len(f.Projects) != 2 || f.Projects[1] != "docs" {
		t.Fatalf("%+v", f)
	}
	f = fixture(t, "audit-user-caddy-baked.txt")
	if c := f.Caddy; c == nil || c.SitesHostDir != "" || c.CaddyfileHostPath != "" || c.HasImport || c.OnNetwork {
		t.Fatalf("%+v", f.Caddy)
	}
	if f = fixture(t, "audit-nginx.txt"); f.Ports[80] != "nginx" || f.Caddy != nil {
		t.Fatalf("%+v", f)
	}
	if f = fixture(t, "audit-nosudo-ready.txt"); f.Sudo || !f.Caddy.VaubanManaged {
		t.Fatalf("%+v", f)
	}
}

func TestParseFactsTolerance(t *testing.T) {
	f, err := ParseFacts([]byte("future_key=1\nsudo=yes\nnoise line\n"))
	if err != nil || !f.Sudo || f.Docker || f.DiskFreeGB != 0 {
		t.Fatalf("%+v %v", f, err)
	}
	if _, err := ParseFacts([]byte("bash: command not found\nnope\n")); err == nil {
		t.Fatal("garbage must error")
	}
	if _, err := ParseFacts(nil); err == nil {
		t.Fatal("empty must error")
	}
	// bind of /etc/caddy: Caddyfile inside it.
	f, err = ParseFacts([]byte(`caddy_container=c
caddy_mounts=[{"Type":"bind","Source":"/srv/c","Destination":"/etc/caddy"}]
`))
	if err != nil || f.Caddy.CaddyfileHostPath != "/srv/c/Caddyfile" || f.Caddy.SitesHostDir != "" {
		t.Fatalf("%+v %v", f.Caddy, err)
	}
}

func hasBlocker(a *Audit, sub string) bool {
	for _, b := range a.Blockers() {
		if strings.Contains(b, sub) {
			return true
		}
	}
	return false
}

func check(a *Audit, name string) Check {
	for _, c := range a.Checks {
		if c.Name == name {
			return c
		}
	}
	return Check{}
}

func TestEvaluate(t *testing.T) {
	const repo = "me/shop"
	t.Run("fresh", func(t *testing.T) {
		a := Evaluate(fixture(t, "audit-fresh-ubuntu.txt"), repo)
		if a.Caddy != CaddyInstall || len(a.Blockers()) != 0 || a.CaddyContainer != "vauban-caddy" {
			t.Fatalf("%v %v", a.Caddy, a.Blockers())
		}
		if check(a, "Docker engine").Status != "missing" {
			t.Fatalf("%+v", a.Checks)
		}
	})
	t.Run("nginx", func(t *testing.T) {
		a := Evaluate(fixture(t, "audit-nginx.txt"), repo)
		if a.Caddy != CaddyBlocked || !hasBlocker(a, "nginx") {
			t.Fatalf("%v %v", a.Caddy, a.Blockers())
		}
	})
	t.Run("baked", func(t *testing.T) {
		a := Evaluate(fixture(t, "audit-user-caddy-baked.txt"), repo)
		if a.Caddy != CaddyUserNeedsSetup || len(a.Blockers()) != 0 {
			t.Fatalf("%v %v", a.Caddy, a.Blockers())
		}
		g := CaddyGuide(a)
		if !strings.Contains(g, "not on a host bind mount") || !strings.Contains(g, "import /etc/caddy/sites/*.caddy") {
			t.Fatal(g)
		}
		if c := check(a, "Caddy container"); c.Status != "warn" || !strings.Contains(c.Detail, "not editable") {
			t.Fatalf("%+v", c)
		}
	})
	t.Run("user-ready", func(t *testing.T) {
		a := Evaluate(fixture(t, "audit-user-caddy-ready.txt"), repo)
		if a.Caddy != CaddyUserReady || a.SitesDir != "/srv/proxy/sites" || a.CaddyContainer != "caddy" || len(a.Blockers()) != 0 {
			t.Fatalf("%+v %v", a, a.Blockers())
		}
	})
	t.Run("nosudo-ready", func(t *testing.T) {
		a := Evaluate(fixture(t, "audit-nosudo-ready.txt"), repo)
		if a.Caddy != CaddyVaubanReady || len(a.Blockers()) != 0 || a.SitesDir != "/opt/vauban/caddy/sites" {
			t.Fatalf("%v %v", a.Caddy, a.Blockers())
		}
	})
	t.Run("nosudo-docker-missing", func(t *testing.T) {
		f := fixture(t, "audit-fresh-ubuntu.txt")
		f.Sudo = false
		if a := Evaluate(f, repo); !hasBlocker(a, "Docker missing and no passwordless sudo") {
			t.Fatal(a.Blockers())
		}
	})
	t.Run("non-debian", func(t *testing.T) {
		f := fixture(t, "audit-fresh-ubuntu.txt")
		f.OSID, f.OSLike, f.OSPretty = "fedora", "rhel", "Fedora 40"
		if a := Evaluate(f, repo); !hasBlocker(a, "Docker missing") || !hasBlocker(a, "Fedora 40") {
			t.Fatal(a.Blockers())
		}
	})
	t.Run("owner", func(t *testing.T) {
		f := fixture(t, "audit-user-caddy-ready.txt")
		f.ProjectOwner = "other/repo"
		if a := Evaluate(f, repo); !hasBlocker(a, "apps/shop belongs to other/repo") {
			t.Fatal(a.Blockers())
		}
		f.ProjectOwner = "-"
		if a := Evaluate(f, repo); !hasBlocker(a, "apps/shop") {
			t.Fatal(a.Blockers())
		}
		f.ProjectOwner = repo
		if a := Evaluate(f, repo); len(a.Blockers()) != 0 {
			t.Fatal(a.Blockers())
		}
	})
	t.Run("disk", func(t *testing.T) {
		f := fixture(t, "audit-user-caddy-ready.txt")
		f.DiskFreeGB = 3
		a := Evaluate(f, repo)
		if check(a, "Disk space").Status != "warn" || len(a.Blockers()) != 0 {
			t.Fatalf("%+v", a.Checks)
		}
	})
	t.Run("firewall", func(t *testing.T) {
		f := fixture(t, "audit-user-caddy-ready.txt")
		f.Firewall = "active-closed"
		c := check(Evaluate(f, repo), "Firewall")
		if c.Status != "warn" || !strings.Contains(c.Detail, "sudo ufw allow 80,443/tcp") {
			t.Fatalf("%+v", c)
		}
	})
	t.Run("checks-rows", func(t *testing.T) {
		a := Evaluate(fixture(t, "audit-user-caddy-ready.txt"), repo)
		for _, n := range []string{"OS", "sudo", "Docker engine", "Compose plugin", "Ports 80/443",
			"Caddy container", "Deploy user", "Firewall", "SSH password auth", "Disk space", "Other projects"} {
			if check(a, n).Name == "" {
				t.Errorf("missing check %q", n)
			}
		}
	})
}

func TestCaddyGuideTailored(t *testing.T) {
	f := fixture(t, "audit-user-caddy-ready.txt")
	f.Caddy.HasImport, f.Caddy.OnNetwork, f.Caddy.SitesHostDir = false, false, ""
	g := CaddyGuide(Evaluate(f, "me/shop"))
	for _, want := range []string{"import /etc/caddy/sites/*.caddy", "- /srv/proxy/sites:/etc/caddy/sites:ro",
		"networks: [vauban]", "external: true", "docker compose up -d", "vauban apply"} {
		if !strings.Contains(g, want) {
			t.Errorf("guide missing %q:\n%s", want, g)
		}
	}
}

type fakeRunner struct {
	cmd engine.Cmd
	out string
}

func (f *fakeRunner) Run(_ context.Context, c engine.Cmd) ([]byte, error) {
	f.cmd = c
	return []byte(f.out), nil
}

func TestRunAudit(t *testing.T) {
	b, _ := os.ReadFile("testdata/audit-nginx.txt")
	r := &fakeRunner{out: string(b)}
	a, err := RunAudit(context.Background(), r, target(t, ""), "shop", "me/shop")
	if err != nil || a.Caddy != CaddyBlocked {
		t.Fatalf("%v %+v", err, a)
	}
	args := strings.Join(r.cmd.Args, " ")
	if !strings.HasSuffix(args, "sh -s -- 'shop'") {
		t.Fatalf("args %q", args)
	}
	if script, _ := readAll(r.cmd); script != auditScript {
		t.Fatal("stdin must be the embedded script")
	}
}

func readAll(c engine.Cmd) (string, error) {
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := c.Stdin.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			return sb.String(), nil
		}
	}
}

func TestAuditScriptReadOnly(t *testing.T) {
	for _, bad := range []string{"rm ", "mkdir", "tee", "apt-get install", "useradd", "docker run", "apt update", "apt install", "chmod", "mv "} {
		if strings.Contains(auditScript, bad) {
			t.Errorf("script contains %q", bad)
		}
	}
	redirect := regexp.MustCompile(`[^-]>>?\s*([^\s;)"]+)`)
	for _, m := range redirect.FindAllStringSubmatch(auditScript, -1) {
		if m[1] != "/dev/null" && !strings.HasPrefix(m[1], "&") {
			t.Errorf("redirect to %q", m[1])
		}
	}
	text := strings.NewReplacer("have sudo", "", "sudo_ok", "", "emit sudo", "").Replace(auditScript)
	sudo := regexp.MustCompile(`sudo (\S+ ?\S* ?\S*)`)
	for _, m := range sudo.FindAllStringSubmatch(text, -1) {
		if !strings.HasPrefix(m[1], "-n true") && !strings.HasPrefix(m[1], "-n sshd -T") && !strings.HasPrefix(m[1], "-n docker") {
			t.Errorf("disallowed sudo use: sudo %s", m[1])
		}
	}
	// sudo docker is only reachable through $D: restrict its subcommands.
	use := regexp.MustCompile(`\$D (\S+ ?\S*)`)
	for _, m := range use.FindAllStringSubmatch(auditScript, -1) {
		ok := false
		for _, p := range []string{"version", "compose version", "ps", "inspect", "network ls", "exec"} {
			if strings.HasPrefix(m[1], p) {
				ok = true
			}
		}
		if !ok {
			t.Errorf("disallowed docker use: $D %s", m[1])
		}
	}
	for _, m := range regexp.MustCompile(`\$D exec (\S+) (\S+)`).FindAllStringSubmatch(auditScript, -1) {
		if m[2] != "cat" {
			t.Errorf("docker exec must be cat, got %q", m[2])
		}
	}
}
