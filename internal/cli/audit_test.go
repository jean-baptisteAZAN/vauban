package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jean-baptisteAZAN/vauban/internal/engine"
)

type auditFake struct {
	out   string
	err   error
	calls []engine.Cmd
}

func (f *auditFake) Run(_ context.Context, c engine.Cmd) ([]byte, error) {
	f.calls = append(f.calls, c)
	return []byte(f.out), f.err
}

const existingYAML = `project: shop
target: existing
domain: app.example.com
server:
  host: 203.0.113.7
  user: ops
  key: agent
`

func auditRun(t *testing.T, yaml, fixture string) (string, *auditFake, error) {
	t.Helper()
	return auditRunErr(t, yaml, fixture, nil)
}

func auditRunErr(t *testing.T, yaml, fixture string, runErr error) (string, *auditFake, error) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("NO_COLOR", "1")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "vauban.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("../remote/testdata/" + fixture)
	if err != nil {
		t.Fatal(err)
	}
	fr := &auditFake{out: string(b), err: runErr}
	old := cmdRunner
	cmdRunner = fr
	t.Cleanup(func() { cmdRunner = old })
	root := NewRoot("dev")
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"audit", "--dir", dir})
	err = root.Execute()
	return out.String(), fr, err
}

func exitCode(err error) int {
	var e *ExitError
	if errors.As(err, &e) {
		return e.Code
	}
	return -1
}

func TestAuditReady(t *testing.T) {
	out, fr, err := auditRun(t, existingYAML, "audit-user-caddy-ready.txt")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"OS", "Docker engine", "Caddy container", "Firewall", "Disk space", "Project dir"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\x1b") {
		t.Error("ANSI in plain output")
	}
	if len(fr.calls) != 1 || fr.calls[0].Name != "ssh" {
		t.Fatalf("calls: %+v", fr.calls)
	}
}

func TestAuditBlockers(t *testing.T) {
	out, _, err := auditRun(t, existingYAML, "audit-nginx.txt")
	if exitCode(err) != 1 {
		t.Fatalf("want exit 1, got %v", err)
	}
	table, blockers, ok := strings.Cut(out, "cannot apply:")
	if !ok {
		t.Fatalf("no blockers heading:\n%s", out)
	}
	if !strings.Contains(table, "Ports 80/443") || !strings.Contains(table, "blocked") {
		t.Errorf("table not printed before blockers:\n%s", out)
	}
	if !strings.Contains(blockers, "  - port 80 held by nginx") {
		t.Errorf("missing blocker line:\n%s", blockers)
	}
}

func TestAuditGuide(t *testing.T) {
	out, _, err := auditRun(t, existingYAML, "audit-user-caddy-baked.txt")
	if err != nil {
		t.Fatalf("guide-only audit must not fail, got %v", err)
	}
	_, after, ok := strings.Cut(out, "caddy setup\n")
	if !ok {
		t.Fatalf("no caddy setup section:\n%s", out)
	}
	if !strings.Contains(after, "import /etc/caddy/sites/*.caddy") {
		t.Errorf("guide not under caddy setup:\n%s", out)
	}
}

func TestAuditSSHFailure(t *testing.T) {
	out, fr, err := auditRunErr(t, existingYAML, "audit-nginx.txt", errors.New("ssh: connect to host: Connection refused"))
	if err == nil || !strings.Contains(err.Error(), "Connection refused") {
		t.Fatalf("want ssh error, got %v", err)
	}
	if len(fr.calls) != 1 {
		t.Fatalf("calls: %d", len(fr.calls))
	}
	if strings.Contains(out, "Docker engine") || strings.Contains(out, "cannot apply:") {
		t.Errorf("audit table printed on failure:\n%s", out)
	}
}

func TestAuditHostKeyChanged(t *testing.T) {
	out, _, err := auditRunErr(t, existingYAML, "audit-nginx.txt", errors.New("ssh: exit 255: WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!"))
	if err == nil || !strings.Contains(err.Error(), ".vauban/known_hosts") || !strings.Contains(err.Error(), "203.0.113.7") {
		t.Fatalf("want host key message, got %v", err)
	}
	if strings.Contains(out, "Docker engine") {
		t.Errorf("table printed:\n%s", out)
	}
}

func TestAuditCloudConfig(t *testing.T) {
	_, fr, err := auditRun(t, "project: shop\ncloud: hetzner\n", "audit-nginx.txt")
	if exitCode(err) != 2 || !strings.Contains(err.Error(), "target: existing") {
		t.Fatalf("want exit 2, got %v", err)
	}
	if len(fr.calls) != 0 {
		t.Fatal("must not ssh")
	}
}
