package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/jean-baptisteAZAN/vauban/internal/config"
	"github.com/jean-baptisteAZAN/vauban/internal/engine"
)

// sshFake answers the ssh calls of an existing-server destroy by action: the
// first script argument is plan or run; an audit has only the project.
type sshFake struct {
	calls            []engine.Cmd
	planOut, runOut  string
	planErr, runErr  error
	auditOut         string
	secretErr        map[string]error
	ranRun, ranAudit bool
}

// scriptArgs returns the unquoted arguments after "sh -s --".
func scriptArgs(c engine.Cmd) []string {
	i := slices.Index(c.Args, "sh")
	var out []string
	for _, a := range c.Args[i+3:] {
		out = append(out, strings.Trim(a, "'"))
	}
	return out
}

func (s *sshFake) Run(_ context.Context, c engine.Cmd) ([]byte, error) {
	s.calls = append(s.calls, c)
	if c.Name != "ssh" {
		if err := s.secretErr[strings.Join(c.Args, " ")]; err != nil {
			return nil, err
		}
		return nil, nil
	}
	if c.Stdin != nil {
		_, _ = io.Copy(io.Discard, c.Stdin)
	}
	a := scriptArgs(c)
	switch {
	case len(a) == 1:
		s.ranAudit = true
		return []byte(s.auditOut), nil
	case a[0] == "plan":
		return []byte(s.planOut), s.planErr
	case a[0] == "run":
		s.ranRun = true
		return []byte(s.runOut), s.runErr
	}
	return nil, errors.New("unexpected ssh call")
}

func (s *sshFake) sshActions() []string {
	var out []string
	for _, c := range s.calls {
		if c.Name == "ssh" {
			a := scriptArgs(c)
			out = append(out, a[0])
		}
	}
	return out
}

func (s *sshFake) secrets() []string {
	var out []string
	for _, c := range s.calls {
		if c.Name == "gh" {
			out = append(out, c.Args[2])
		}
	}
	return out
}

func (s *sshFake) call(action string) engine.Cmd {
	for _, c := range s.calls {
		if c.Name == "ssh" && scriptArgs(c)[0] == action {
			return c
		}
	}
	return engine.Cmd{}
}

const destroyExistingYAML = "project: shop\ntarget: existing\ndomain: shop.example.com\napp_port: 8080\nserver:\n  host: 203.0.113.10\n  user: ops\n  key: agent\n"

// existingDestroyEnv is an applied existing-server project.
func existingDestroyEnv(t *testing.T) (string, *sshFake) {
	t.Helper()
	root, _ := applyEnv(t)
	writeFile(t, root, config.FileName, destroyExistingYAML)
	writeFile(t, root, "Dockerfile", "FROM scratch\n")
	writeFile(t, root, ".vauban/known_hosts", "203.0.113.10 ssh-ed25519 AAAA\n")
	saveState(t, root, &engine.State{
		Done: map[string]bool{"file:Dockerfile": true, "server:audit": true},
		Outputs: engine.Outputs{
			"server_sites_dir": "/opt/vauban/caddy/sites", "server_caddy_container": "vauban-caddy",
			"server_user": "ops", "server_ip": "203.0.113.10",
		},
	})
	f := &sshFake{}
	setRunner(t, f)
	return root, f
}

func TestDestroyExistingConfirmed(t *testing.T) {
	root, f := existingDestroyEnv(t)
	out, err := execVauban(t, context.Background(), "destroy", "--dir", root, "--confirm", "shop")
	if err != nil {
		t.Fatalf("destroy: %v\n%s", err, out)
	}
	if got := f.sshActions(); !slices.Equal(got, []string{"plan", "run"}) {
		t.Errorf("ssh actions = %q", got)
	}
	want := []string{"run", "sudo", "/opt/vauban/caddy/sites", "shop", "acme/shop", "vauban-caddy", "no"}
	if got := scriptArgs(f.call("run")); !slices.Equal(got, want) {
		t.Errorf("run args = %q, want %q", got, want)
	}
	wantSecrets := []string{"DEPLOY_HOST", "DEPLOY_SSH_KEY", "DEPLOY_DOMAIN", "DEPLOY_KNOWN_HOSTS", "DEPLOY_PORT"}
	if got := f.secrets(); !slices.Equal(got, wantSecrets) {
		t.Errorf("secrets = %q, want %q", got, wantSecrets)
	}
	if exists(statePath(root)) {
		t.Error("state.json not removed")
	}
	if !exists(root + "/.vauban/known_hosts") {
		t.Error("known_hosts removed")
	}
	for _, bad := range []string{"docker network rm", "userdel", "apt-get", "container stop", "down -v"} {
		if strings.Contains(out, bad) {
			t.Errorf("output mentions %q:\n%s", bad, out)
		}
	}
	for _, s := range []string{"docker compose -p vauban-shop", "# vauban:shop", "shop.caddy", "left in place"} {
		if !strings.Contains(out, s) {
			t.Errorf("missing %q in:\n%s", s, out)
		}
	}
}

func TestDestroyExistingDirectMode(t *testing.T) {
	root, f := existingDestroyEnv(t)
	writeFile(t, root, config.FileName, strings.Replace(destroyExistingYAML, "user: ops", "user: deploy", 1))
	// destroy uses the user the project was applied with (server_target).
	st, _ := engine.LoadState(root)
	st.Outputs["server_user"] = "deploy"
	saveState(t, root, st)
	if out, err := execVauban(t, context.Background(), "destroy", "--dir", root, "--confirm", "shop"); err != nil {
		t.Fatalf("destroy: %v\n%s", err, out)
	}
	if got := scriptArgs(f.call("run"))[1]; got != "direct" {
		t.Errorf("mode = %q", got)
	}
}

func TestDestroyExistingPurge(t *testing.T) {
	root, f := existingDestroyEnv(t)
	f.planOut = "VAUBAN_VOLUME vauban-shop_pgdata\nVAUBAN_VOLUME vauban-shop_uploads\n"
	out, err := execVauban(t, context.Background(), "destroy", "--dir", root, "--confirm", "shop", "--purge")
	if err != nil {
		t.Fatalf("destroy: %v\n%s", err, out)
	}
	for _, a := range []string{"plan", "run"} {
		if got := scriptArgs(f.call(a)); got[len(got)-1] != "yes" {
			t.Errorf("%s args = %q, want purge yes", a, got)
		}
	}
	for _, s := range []string{"down -v", "vauban-shop_pgdata", "vauban-shop_uploads"} {
		if !strings.Contains(out, s) {
			t.Errorf("missing %q in:\n%s", s, out)
		}
	}
}

func TestDestroyExistingPurgeWrongConfirmRunsNothing(t *testing.T) {
	root, f := existingDestroyEnv(t)
	out, err := execVauban(t, context.Background(), "destroy", "--dir", root, "--confirm", "shopp", "--purge")
	wantExit(t, err, 2, "", out)
	if f.ranRun || len(f.secrets()) != 0 {
		t.Errorf("destructive calls: %q", callLines(&engine.FakeRunner{Calls: f.calls}))
	}
	if !exists(statePath(root)) {
		t.Error("state.json removed")
	}
}

func TestDestroyExistingOwnerMismatchRefuses(t *testing.T) {
	root, f := existingDestroyEnv(t)
	f.planOut, f.planErr = "VAUBAN_OWNER_CONFLICT other/shop\n", errors.New("exit status 3")
	out, err := execVauban(t, context.Background(), "destroy", "--dir", root, "--confirm", "shop")
	wantExit(t, err, 1, "", out)
	if !strings.Contains(err.Error()+out, "other/shop") {
		t.Errorf("owner not named: %v\n%s", err, out)
	}
	if f.ranRun || len(f.secrets()) != 0 || !exists(statePath(root)) {
		t.Error("something was done")
	}
}

func TestDestroyExistingProjectAbsent(t *testing.T) {
	root, f := existingDestroyEnv(t)
	f.planOut = "VAUBAN_PROJECT_ABSENT\n"
	out, err := execVauban(t, context.Background(), "destroy", "--dir", root, "--confirm", "shop")
	if err != nil {
		t.Fatalf("destroy: %v\n%s", err, out)
	}
	if !f.ranRun {
		t.Error("run not called: a leftover authorized_keys line would stay")
	}
	if !strings.Contains(out, "nothing on the server to remove") {
		t.Errorf("missing notice:\n%s", out)
	}
	if len(f.secrets()) != 5 || exists(statePath(root)) {
		t.Errorf("secrets %q, state exists %v", f.secrets(), exists(statePath(root)))
	}
}

func TestDestroyExistingNoOutputsRunsAudit(t *testing.T) {
	root, f := existingDestroyEnv(t)
	saveState(t, root, &engine.State{Done: map[string]bool{}, Outputs: engine.Outputs{}})
	b, err := os.ReadFile("../remote/testdata/audit-caddy-dirbind.txt")
	if err != nil {
		t.Fatal(err)
	}
	f.auditOut = strings.Replace(string(b), "project_owner=\n", "project_owner=acme/shop\n", 1)
	out, err := execVauban(t, context.Background(), "destroy", "--dir", root, "--confirm", "shop")
	if err != nil {
		t.Fatalf("destroy: %v\n%s", err, out)
	}
	if !f.ranAudit {
		t.Fatal("no audit")
	}
	got := scriptArgs(f.call("run"))
	if got[2] != "/srv/c/sites" || got[5] != "caddy" {
		t.Errorf("run args = %q", got)
	}
}

func TestDestroyExistingNoOutputsAuditSaysAbsent(t *testing.T) {
	root, f := existingDestroyEnv(t)
	saveState(t, root, &engine.State{Done: map[string]bool{}, Outputs: engine.Outputs{}})
	b, _ := os.ReadFile("../remote/testdata/audit-caddy-dirbind.txt")
	f.auditOut = string(b) // project_owner= : the project dir does not exist
	out, err := execVauban(t, context.Background(), "destroy", "--dir", root, "--confirm", "shop")
	if err != nil {
		t.Fatalf("destroy: %v\n%s", err, out)
	}
	if !f.ranRun || !strings.Contains(out, "nothing on the server to remove") {
		t.Errorf("ranRun=%v out:\n%s", f.ranRun, out)
	}
}

func TestDestroyExistingServerFailureKeepsSecretsAndState(t *testing.T) {
	root, f := existingDestroyEnv(t)
	f.runErr = errors.New("exit status 6: caddy invalid")
	out, err := execVauban(t, context.Background(), "destroy", "--dir", root, "--confirm", "shop")
	wantExit(t, err, 1, "", out)
	if len(f.secrets()) != 0 || !exists(statePath(root)) {
		t.Error("secrets deleted or state removed after a server failure")
	}
}

func TestDestroyExistingForeignSiteWarns(t *testing.T) {
	root, f := existingDestroyEnv(t)
	f.runOut = "VAUBAN_SITE_FOREIGN\nproject removed\n"
	out, err := execVauban(t, context.Background(), "destroy", "--dir", root, "--confirm", "shop")
	if err != nil {
		t.Fatalf("destroy: %v\n%s", err, out)
	}
	if !strings.Contains(out, "not written by vauban") {
		t.Errorf("no foreign warning:\n%s", out)
	}
}

func TestDestroyExistingSecretNotFoundTolerated(t *testing.T) {
	root, f := existingDestroyEnv(t)
	f.secretErr = map[string]error{"secret delete DEPLOY_PORT --repo acme/shop": errors.New("HTTP 404")}
	out, err := execVauban(t, context.Background(), "destroy", "--dir", root, "--confirm", "shop")
	if err != nil {
		t.Fatalf("destroy: %v\n%s", err, out)
	}
}

func TestDestroyPurgeNeedsExisting(t *testing.T) {
	root, fr := destroyEnv(t)
	out, err := execVauban(t, context.Background(), "destroy", "--dir", root, "--confirm", "shop", "--purge")
	wantExit(t, err, 2, "", out)
	if len(fr.Calls) != 0 {
		t.Errorf("calls: %q", callLines(fr))
	}
}

func TestDestroyExistingNoSudoIsNotAbsent(t *testing.T) {
	root, f := existingDestroyEnv(t)
	f.planOut, f.planErr = "", errors.New("exit status 5: cannot act as deploy (sudo -n runuser failed)")
	out, err := execVauban(t, context.Background(), "destroy", "--dir", root, "--confirm", "shop")
	wantExit(t, err, 1, "", out)
	if !strings.Contains(err.Error(), "cannot act as the deploy user") || strings.Contains(out, "nothing on the server to remove") {
		t.Errorf("err = %v\n%s", err, out)
	}
	if f.ranRun || len(f.secrets()) != 0 || !exists(statePath(root)) {
		t.Error("something was done")
	}
	// the same failure during run
	root, f = existingDestroyEnv(t)
	f.runErr = errors.New("exit status 5: cannot act as deploy (sudo -n runuser failed)")
	out, err = execVauban(t, context.Background(), "destroy", "--dir", root, "--confirm", "shop")
	wantExit(t, err, 1, "", out)
	if !strings.Contains(err.Error(), "cannot act as the deploy user") {
		t.Errorf("run err = %v", err)
	}
}

func TestDestroyExistingMissingMarkerMessage(t *testing.T) {
	root, f := existingDestroyEnv(t)
	f.planOut, f.planErr = "VAUBAN_OWNER_CONFLICT -\n", errors.New("exit status 3")
	out, err := execVauban(t, context.Background(), "destroy", "--dir", root, "--confirm", "shop")
	wantExit(t, err, 1, "", out)
	if !strings.Contains(err.Error(), "no .vauban-owner marker") || strings.Contains(err.Error(), "belongs to -") {
		t.Errorf("err = %v", err)
	}
	// the run-time race gives the same message
	root, f = existingDestroyEnv(t)
	f.runOut, f.runErr = "VAUBAN_OWNER_CONFLICT -\n", errors.New("exit status 3")
	_, err = execVauban(t, context.Background(), "destroy", "--dir", root, "--confirm", "shop")
	if err == nil || !strings.Contains(err.Error(), "no .vauban-owner marker") {
		t.Errorf("run err = %v", err)
	}
}

func TestDestroyExistingUnknownSitesWarns(t *testing.T) {
	root, f := existingDestroyEnv(t)
	saveState(t, root, &engine.State{Done: map[string]bool{}, Outputs: engine.Outputs{}})
	b, _ := os.ReadFile("../remote/testdata/audit-nginx.txt")
	f.auditOut = string(b)
	out, err := execVauban(t, context.Background(), "destroy", "--dir", root, "--confirm", "shop")
	if err != nil {
		t.Fatalf("destroy: %v\n%s", err, out)
	}
	if got := scriptArgs(f.call("run")); got[2] != "-" || got[5] != "-" {
		t.Fatalf("run args = %q", got)
	}
	if !strings.Contains(out, "sites dir was unknown") {
		t.Errorf("no warning:\n%s", out)
	}
}
