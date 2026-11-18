package cli

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"charm.land/fang/v2"
	"charm.land/huh/v2"
	"github.com/spf13/cobra"

	"github.com/jean-baptisteAZAN/vauban/internal/config"
	"github.com/jean-baptisteAZAN/vauban/internal/engine"
)

const (
	tofuPlanCmd   = "tofu -chdir=infra plan -input=false -no-color -out=tfplan"
	tofuShowCmd   = "tofu -chdir=infra show -json tfplan"
	tofuApplyCmd  = "tofu -chdir=infra apply -input=false tfplan"
	tofuOutputCmd = "tofu -chdir=infra output -json"
	testIP        = "203.0.113.10"
	testDomain    = "203-0-113-10.sslip.io"
	createJSON    = `{"resource_changes":[{"change":{"actions":["create"]}}]}`
	replaceJSON   = `{"resource_changes":[{"change":{"actions":["delete","create"]}}]}`
	replacePlan   = "  # hcloud_server.app must be replaced\n-/+ resource \"hcloud_server\" \"app\" {\nPlan: 1 to add, 0 to change, 1 to destroy.\n"
)

// applyEnv is planEnv plus what apply needs offline: fake tofu and
// ssh-keygen on PATH (never run), an existing deploy key, no TTY and a
// FakeRunner answering tofu show/output.
func applyEnv(t *testing.T) (string, *engine.FakeRunner) {
	t.Helper()
	root, _ := planEnv(t, "go-app")
	bin := t.TempDir()
	for _, name := range []string{"tofu", "ssh-keygen"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	home, _ := os.UserHomeDir()
	key := filepath.Join(home, ".ssh", "vauban_shop_ed25519")
	writeFile(t, "/", key, "PRIVATE KEY\n")
	writeFile(t, "/", key+".pub", "ssh-ed25519 AAAA vauban-shop\n")

	oldTTY := interactive
	interactive = func() bool { return false }
	t.Cleanup(func() { interactive = oldTTY })

	fr := &engine.FakeRunner{Responses: map[string][]byte{
		tofuShowCmd:   []byte(createJSON),
		tofuOutputCmd: []byte(`{"server_ip":{"value":"` + testIP + `"}}`),
	}}
	setRunner(t, fr)
	return root, fr
}

func setRunner(t *testing.T, r engine.Runner) {
	t.Helper()
	old := cmdRunner
	cmdRunner = r
	t.Cleanup(func() { cmdRunner = old })
}

func execVauban(t *testing.T, ctx context.Context, args ...string) (string, error) {
	t.Helper()
	cmd := NewRoot("dev")
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(ctx)
	return out.String(), err
}

func callLines(fr *engine.FakeRunner) []string {
	var out []string
	for _, c := range fr.Calls {
		out = append(out, strings.Join(append([]string{c.Name}, c.Args...), " "))
	}
	return out
}

func wantExit(t *testing.T, err error, code int, msg, out string) {
	t.Helper()
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != code {
		t.Fatalf("want ExitError code %d, got %v\n%s", code, err, out)
	}
	if msg != "" && err.Error() != msg {
		t.Errorf("message = %q, want %q", err.Error(), msg)
	}
}

func TestApplyYesRunsAllSteps(t *testing.T) {
	root, fr := applyEnv(t)
	out, err := execVauban(t, context.Background(), "apply", "--dir", root, "--yes", "--cloud", "hetzner")
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	st, err := engine.LoadState(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"file:Dockerfile", "infra:main-tf", "infra:apply", "infra:output", "deploy:domain", "secret:DEPLOY_HOST", "secret:DEPLOY_SSH_KEY", "secret:DEPLOY_DOMAIN"} {
		if !st.Done[id] {
			t.Errorf("%s not done", id)
		}
	}
	calls := callLines(fr)
	for _, want := range []string{tofuApplyCmd, "gh secret set DEPLOY_HOST --repo acme/shop", "gh secret set DEPLOY_SSH_KEY --repo acme/shop", "gh secret set DEPLOY_DOMAIN --repo acme/shop"} {
		if !slices.Contains(calls, want) {
			t.Errorf("missing call %q in %q", want, calls)
		}
	}
	for _, want := range []string{
		"+ write Dockerfile", // the plan is shown first
		"[run] write Dockerfile\n[ok] write Dockerfile\n",
		"[ok] $ " + tofuApplyCmd + "\n",
		"[ok] gh secret set DEPLOY_DOMAIN --repo acme/shop\n",
		"[ok] ready · https://" + testDomain + "\n",
		`next: git add -A && git commit -m "chore: vauban setup" && git push` + "\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Index(out, "+ write Dockerfile") > strings.Index(out, "[run]") {
		t.Errorf("plan not rendered before execution:\n%s", out)
	}
	if strings.Contains(out, planToken) || strings.Contains(out, "PRIVATE KEY") || strings.Contains(out, "\x1b") {
		t.Errorf("secret or ANSI in output:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(root, config.FileName)); err != nil {
		t.Errorf("vauban.yaml not saved: %v", err)
	}
}

func TestApplySecondRunMakesNoCalls(t *testing.T) {
	root, _ := applyEnv(t)
	if out, err := execVauban(t, context.Background(), "apply", "--dir", root, "--yes", "--cloud", "hetzner"); err != nil {
		t.Fatalf("first apply: %v\n%s", err, out)
	}
	fr := &engine.FakeRunner{}
	setRunner(t, fr)
	out, err := execVauban(t, context.Background(), "apply", "--dir", root, "--yes")
	if err != nil {
		t.Fatalf("second apply: %v\n%s", err, out)
	}
	if len(fr.Calls) != 0 {
		t.Errorf("second apply made calls: %q", callLines(fr))
	}
	if !strings.Contains(out, "[skip] write Dockerfile") || !strings.Contains(out, "ready · https://"+testDomain) {
		t.Errorf("unexpected output:\n%s", out)
	}
}

func TestApplyNonTTYWithoutYesExit2(t *testing.T) {
	root, fr := applyEnv(t)
	writeFile(t, root, config.FileName, "project: shop\ncloud: hetzner\napp_port: 8080\n")
	out, err := execVauban(t, context.Background(), "apply", "--dir", root)
	wantExit(t, err, 2, "apply needs a terminal or --yes", out)
	if len(fr.Calls) != 0 {
		t.Errorf("calls made: %q", callLines(fr))
	}
	if _, err := os.Stat(filepath.Join(root, "Dockerfile")); !errors.Is(err, fs.ErrNotExist) {
		t.Error("Dockerfile written without confirmation")
	}
}

func TestApplyDestructiveNotConfirmedExit3(t *testing.T) {
	root, fr := applyEnv(t)
	fr.Responses[tofuShowCmd] = []byte(replaceJSON)
	fr.Responses[tofuPlanCmd] = []byte(replacePlan)
	out, err := execVauban(t, context.Background(), "apply", "--dir", root, "--yes", "--cloud", "hetzner")
	wantExit(t, err, 3, "destructive infra change not confirmed — nothing was applied; rerun and type the project name (or pass --force)", out)
	if slices.Contains(callLines(fr), tofuApplyCmd) {
		t.Errorf("tofu apply ran without confirmation: %q", callLines(fr))
	}
	for _, want := range []string{"infra changes will destroy or replace resources", "must be replaced", "-/+ resource"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("ANSI in plain output:\n%s", out)
	}
}

func TestApplyDestructiveForce(t *testing.T) {
	root, fr := applyEnv(t)
	fr.Responses[tofuShowCmd] = []byte(replaceJSON)
	fr.Responses[tofuPlanCmd] = []byte(replacePlan)
	out, err := execVauban(t, context.Background(), "apply", "--dir", root, "--yes", "--force", "--cloud", "hetzner")
	if err != nil {
		t.Fatalf("apply --force: %v\n%s", err, out)
	}
	if !slices.Contains(callLines(fr), tofuApplyCmd) {
		t.Errorf("tofu apply not called: %q", callLines(fr))
	}
	if !strings.Contains(out, "must be replaced") {
		t.Errorf("destructive plan not shown:\n%s", out)
	}
}

func TestApplyPreflightFailureExit1(t *testing.T) {
	root, fr := applyEnv(t)
	t.Setenv("PATH", t.TempDir()) // no tofu, no ssh-keygen
	out, err := execVauban(t, context.Background(), "apply", "--dir", root, "--yes", "--cloud", "hetzner")
	wantExit(t, err, 1, "", out)
	if len(fr.Calls) != 0 {
		t.Errorf("calls made: %q", callLines(fr))
	}
	for _, want := range []string{"OpenTofu (tofu) not found", "ssh-keygen not found"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "Dockerfile")); !errors.Is(err, fs.ErrNotExist) {
		t.Error("Dockerfile written despite preflight failure")
	}
}

// cancelRunner cancels the context once it sees the command `on`.
type cancelRunner struct {
	*engine.FakeRunner
	on     string
	cancel context.CancelFunc
}

func (c cancelRunner) Run(ctx context.Context, cmd engine.Cmd) ([]byte, error) {
	out, err := c.FakeRunner.Run(ctx, cmd)
	if strings.Join(append([]string{cmd.Name}, cmd.Args...), " ") == c.on {
		c.cancel()
	}
	return out, err
}

func TestApplyCancelledExit130(t *testing.T) {
	root, fr := applyEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	setRunner(t, cancelRunner{FakeRunner: fr, on: "tofu -chdir=infra init -input=false", cancel: cancel})
	out, err := execVauban(t, ctx, "apply", "--dir", root, "--yes", "--cloud", "hetzner")
	wantExit(t, err, 130, "interrupted — rerun `vauban apply` to resume", out)
	st, err := engine.LoadState(root)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Done["infra:init"] || st.Done["infra:plan"] {
		t.Errorf("state not saved at the interruption point: %v", st.Done)
	}
	if slices.Contains(callLines(fr), tofuPlanCmd) {
		t.Errorf("ran after cancel: %q", callLines(fr))
	}
}

func TestQuietExitOnUserAbort(t *testing.T) {
	run := abortable(func(*cobra.Command, []string) error {
		return errors.Join(errors.New("form"), huh.ErrUserAborted)
	})
	err := run(nil, nil)
	wantExit(t, err, 130, "", "")
	var buf bytes.Buffer
	errorHandler(&buf, fang.Styles{}, err)
	if buf.Len() != 0 {
		t.Errorf("aborted run printed %q", buf.String())
	}
}
