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
	calls []engine.Cmd
}

func (f *auditFake) Run(_ context.Context, c engine.Cmd) ([]byte, error) {
	f.calls = append(f.calls, c)
	return []byte(f.out), nil
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
	fr := &auditFake{out: string(b)}
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
	if !strings.Contains(out, "cannot apply:") {
		t.Errorf("no blockers heading:\n%s", out)
	}
}

func TestAuditGuide(t *testing.T) {
	out, _, _ := auditRun(t, existingYAML, "audit-user-caddy-baked.txt")
	if !strings.Contains(out, "import /etc/caddy/sites/*.caddy") {
		t.Errorf("guide not printed:\n%s", out)
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
