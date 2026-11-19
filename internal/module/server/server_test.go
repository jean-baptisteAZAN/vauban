package server

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jean-baptisteAZAN/vauban/internal/config"
	"github.com/jean-baptisteAZAN/vauban/internal/detect"
	"github.com/jean-baptisteAZAN/vauban/internal/engine"
	"github.com/jean-baptisteAZAN/vauban/internal/module/infra"
)

type fakeRunner struct {
	cmds []engine.Cmd
	out  string
	stdi []string
}

func (f *fakeRunner) Run(_ context.Context, c engine.Cmd) ([]byte, error) {
	f.cmds = append(f.cmds, c)
	in := ""
	if c.Stdin != nil {
		buf := new(strings.Builder)
		_, _ = io.Copy(buf, c.Stdin)
		in = buf.String()
	}
	f.stdi = append(f.stdi, in)
	return []byte(f.out), nil
}

func cfg(target string) *config.Config {
	c := &config.Config{Project: "shop", Target: target, Domain: "shop.example.com", AppPort: 3000}
	if target == config.TargetExisting {
		c.Server = &config.Server{Host: "203.0.113.10", Port: 2222, User: "ops", Key: "agent"}
	}
	return c
}

func proj(t *testing.T) *detect.Project {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	return &detect.Project{Root: t.TempDir(), GitHubRepo: "me/shop"}
}

func plan(t *testing.T, target string) ([]engine.Step, *detect.Project) {
	t.Helper()
	p := proj(t)
	steps, err := New().Plan(p, cfg(target))
	if err != nil {
		t.Fatal(err)
	}
	return steps, p
}

func byID(steps []engine.Step, id string) engine.Step {
	for _, s := range steps {
		if s.ID() == id {
			return s
		}
	}
	return nil
}

func TestPlanCloudIsEmpty(t *testing.T) {
	for _, target := range []string{"", config.TargetCloud} {
		if steps, _ := plan(t, target); steps != nil {
			t.Fatalf("target %q: %v", target, steps)
		}
	}
	p := proj(t)
	c := cfg(config.TargetExisting)
	steps, err := infra.New().Plan(p, c)
	if err != nil || steps != nil {
		t.Fatalf("infra for existing: %v %v", steps, err)
	}
}

func TestPlanIDs(t *testing.T) {
	steps, _ := plan(t, config.TargetExisting)
	var ids []string
	for _, s := range steps {
		ids = append(ids, s.ID())
		if s.Describe() == "" {
			t.Fatalf("%s: empty describe", s.ID())
		}
	}
	want := "server:audit server:docker server:deploy-user server:deploy-key server:authorize-key server:network server:caddy server:project server:site"
	if got := strings.Join(ids, " "); got != want {
		t.Fatalf("got %s", got)
	}
	if d := byID(steps, "server:docker").Describe(); !strings.Contains(d, "203.0.113.10") || !strings.Contains(d, "sudo") {
		t.Fatalf("describe: %s", d)
	}
}

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "remote", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAuditOutputs(t *testing.T) {
	steps, p := plan(t, config.TargetExisting)
	r := &fakeRunner{out: readFixture(t, "audit-fresh-ubuntu.txt")}
	out, err := byID(steps, "server:audit").Apply(context.Background(), &engine.Env{Root: p.Root, Run: r, In: engine.Outputs{}})
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{
		"server_ip": "203.0.113.10", "server_port": "2222", "server_user": "ops",
		"server_sites_dir": "/opt/vauban/caddy/sites", "server_caddy_container": "vauban-caddy",
		"server_caddy_mode": "install", "server_sudo": "true", "server_docker": "false",
		"server_compose": "false", "server_deploy_user": "false", "server_docker_group": "false", "server_deploy_docker_group": "false",
	} {
		if out[k] != v {
			t.Errorf("%s = %q, want %q", k, out[k], v)
		}
	}
	if len(r.cmds) != 1 || !strings.Contains(strings.Join(r.cmds[0].Args, " "), "ops@203.0.113.10") {
		t.Fatalf("cmds %v", r.cmds)
	}
}

func TestAuditBlockerStops(t *testing.T) {
	steps, p := plan(t, config.TargetExisting)
	r := &fakeRunner{out: readFixture(t, "audit-nginx.txt")}
	_, err := byID(steps, "server:audit").Apply(context.Background(), &engine.Env{Root: p.Root, Run: r, In: engine.Outputs{}})
	if err == nil {
		t.Fatal("want blocker error")
	}
	if len(r.cmds) != 1 {
		t.Fatalf("calls: %d", len(r.cmds))
	}
}

func TestDockerNoopWhenPresent(t *testing.T) {
	steps, p := plan(t, config.TargetExisting)
	r := &fakeRunner{}
	in := engine.Outputs{"server_docker": "true", "server_compose": "true", "server_sudo": "false"}
	if _, err := byID(steps, "server:docker").Apply(context.Background(), &engine.Env{Root: p.Root, Run: r, In: in}); err != nil {
		t.Fatal(err)
	}
	if len(r.cmds) != 0 {
		t.Fatalf("ssh called: %v", r.cmds)
	}
}

func TestInstallNeedsSudo(t *testing.T) {
	steps, p := plan(t, config.TargetExisting)
	for _, id := range []string{"server:docker", "server:deploy-user"} {
		r := &fakeRunner{}
		in := engine.Outputs{"server_sudo": "false"}
		_, err := byID(steps, id).Apply(context.Background(), &engine.Env{Root: p.Root, Run: r, In: in})
		if err == nil || len(r.cmds) != 0 {
			t.Fatalf("%s: err=%v calls=%d", id, err, len(r.cmds))
		}
	}
	if _, err := byID(steps, "server:docker").Apply(context.Background(), &engine.Env{Root: p.Root, Run: &fakeRunner{}, In: engine.Outputs{"server_sudo": "false"}}); !strings.Contains(err.Error(), "Docker") {
		t.Fatalf("err should name Docker: %v", err)
	}
}

func TestDockerInstallScript(t *testing.T) {
	steps, p := plan(t, config.TargetExisting)
	r := &fakeRunner{}
	in := engine.Outputs{"server_sudo": "true", "server_docker": "false", "server_compose": "false"}
	if _, err := byID(steps, "server:docker").Apply(context.Background(), &engine.Env{Root: p.Root, Run: r, In: in}); err != nil {
		t.Fatal(err)
	}
	s := r.stdi[0]
	for _, w := range []string{"set -eu", "/etc/apt/keyrings/docker.asc", "signed-by", "sudo -n", "DEBIAN_FRONTEND=noninteractive",
		"docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin", "command -v docker"} {
		if !strings.Contains(s, w) {
			t.Errorf("script lacks %q", w)
		}
	}
	if strings.Contains(s, "| sh") || strings.Contains(s, "|sh") {
		t.Error("curl | sh")
	}
}

func TestDeployUser(t *testing.T) {
	steps, p := plan(t, config.TargetExisting)
	st := byID(steps, "server:deploy-user")
	r := &fakeRunner{}
	in := engine.Outputs{"server_deploy_user": "true", "server_deploy_docker_group": "true", "server_docker_group": "false"}
	if _, err := st.Apply(context.Background(), &engine.Env{Root: p.Root, Run: r, In: in}); err != nil || len(r.cmds) != 0 {
		t.Fatalf("noop: %v %d", err, len(r.cmds))
	}
	in = engine.Outputs{"server_sudo": "true", "server_deploy_user": "false"}
	if _, err := st.Apply(context.Background(), &engine.Env{Root: p.Root, Run: r, In: in}); err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"useradd -m -s /bin/bash -G docker deploy", "usermod -aG docker deploy", "sudo -n", "set -eu"} {
		if !strings.Contains(r.stdi[0], w) {
			t.Errorf("script lacks %q", w)
		}
	}
}

func TestDeployKeyID(t *testing.T) {
	steps, _ := plan(t, config.TargetExisting)
	if byID(steps, "server:deploy-key") == nil {
		t.Fatal("missing")
	}
	k, err := infra.NewDeployKey("shop", "x:y")
	if err != nil || k.ID() != "x:y" {
		t.Fatalf("%v %v", k, err)
	}
}

func TestAuthorizeKey(t *testing.T) {
	steps, p := plan(t, config.TargetExisting)
	st := byID(steps, "server:authorize-key")
	r := &fakeRunner{}
	pub := "ssh-ed25519 AAAAC3 vauban-shop"
	env := &engine.Env{Root: p.Root, Run: r, In: engine.Outputs{"deploy_pubkey": pub, "server_sudo": "true"}}
	if _, err := st.Apply(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	args := strings.Join(r.cmds[0].Args, " ")
	if !strings.Contains(args, "'"+pub+"'") || !strings.Contains(args, "'shop'") || strings.Contains(args, "# vauban") {
		t.Fatalf("args: %s", args)
	}
	if !strings.Contains(r.stdi[0], "# vauban:") || !strings.Contains(r.stdi[0], "sudo -n") || !strings.Contains(r.stdi[0], "grep -qxF") {
		t.Fatalf("script:\n%s", r.stdi[0])
	}
}

func TestAuthorizeKeyNeedsSudoUnlessDeploy(t *testing.T) {
	p := proj(t)
	c := cfg(config.TargetExisting)
	steps, err := New().Plan(p, c)
	if err != nil {
		t.Fatal(err)
	}
	r := &fakeRunner{}
	env := &engine.Env{Root: p.Root, Run: r, In: engine.Outputs{"deploy_pubkey": "ssh-ed25519 A k", "server_sudo": "false"}}
	if _, err := byID(steps, "server:authorize-key").Apply(context.Background(), env); err == nil || len(r.cmds) != 0 {
		t.Fatalf("want sudo error, no ssh: %v %d", err, len(r.cmds))
	}
	c.Server.User = "deploy"
	steps, _ = New().Plan(p, c)
	if _, err := byID(steps, "server:authorize-key").Apply(context.Background(), env); err != nil || len(r.cmds) != 1 {
		t.Fatalf("deploy user: %v %d", err, len(r.cmds))
	}
}

func TestAuthorizeKeyReadsPubFromDisk(t *testing.T) {
	steps, p := plan(t, config.TargetExisting)
	path, _ := infra.DeployKeyPath("shop")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".pub", []byte("ssh-ed25519 ZZ vauban-shop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &fakeRunner{}
	env := &engine.Env{Root: p.Root, Run: r, In: engine.Outputs{"server_sudo": "true"}}
	if _, err := byID(steps, "server:authorize-key").Apply(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(r.cmds[0].Args, " "), "ssh-ed25519 ZZ vauban-shop") {
		t.Fatal("pubkey not passed")
	}
}

func TestDeployUserGatedOnDeployGroup(t *testing.T) {
	steps, p := plan(t, config.TargetExisting)
	r := &fakeRunner{}
	// deploy exists but is not in docker; the ssh user is.
	in := engine.Outputs{"server_sudo": "true", "server_deploy_user": "true", "server_docker_group": "true", "server_deploy_docker_group": "false"}
	if _, err := byID(steps, "server:deploy-user").Apply(context.Background(), &engine.Env{Root: p.Root, Run: r, In: in}); err != nil || len(r.cmds) != 1 {
		t.Fatalf("script must run: %v %d", err, len(r.cmds))
	}
}

func dockerArgs(t *testing.T, in engine.Outputs) (*fakeRunner, error) {
	t.Helper()
	steps, p := plan(t, config.TargetExisting)
	r := &fakeRunner{}
	_, err := byID(steps, "server:docker").Apply(context.Background(), &engine.Env{Root: p.Root, Run: r, In: in})
	return r, err
}

func TestDockerModes(t *testing.T) {
	r, err := dockerArgs(t, engine.Outputs{"server_sudo": "true", "server_docker": "true", "server_compose": "false"})
	if err != nil || len(r.cmds) != 1 || !strings.HasSuffix(strings.Join(r.cmds[0].Args, " "), "sh -s -- 'compose'") {
		t.Fatalf("compose mode: %v %v", err, r.cmds)
	}
	r, err = dockerArgs(t, engine.Outputs{"server_sudo": "true", "server_docker": "false"})
	if err != nil || !strings.HasSuffix(strings.Join(r.cmds[0].Args, " "), "sh -s -- 'full'") {
		t.Fatalf("full mode: %v %v", err, r.cmds)
	}
}

func TestDockerScriptSafety(t *testing.T) {
	for _, w := range []string{
		`grep -rqs download.docker.com /etc/apt/sources.list /etc/apt/sources.list.d/`,
		`mode=compose`, `--no-upgrade docker-compose-plugin`, `docker-compose-v2`,
		`NEEDRESTART_MODE=l`, `</dev/null`, `--no-upgrade ca-certificates curl`,
		`if [ "$(id -u)" = 0 ]; then SUDO=""; else SUDO="sudo -n"; fi`,
	} {
		if !strings.Contains(dockerScript, w) {
			t.Errorf("docker script lacks %q", w)
		}
	}
	// compose-only branch must exit before any engine package is named.
	pre, _, _ := strings.Cut(dockerScript, "if ! has_docker_source; then\n\t# shellcheck")
	if i := strings.Index(pre, "apt install -y docker-ce"); i >= 0 {
		t.Error("engine install reachable from compose mode")
	}
	// the key and list are only written when no source exists.
	if !strings.Contains(dockerScript, "if ! has_docker_source; then\n\t# shellcheck disable=SC1091") {
		t.Error("key/list not guarded by the source check")
	}
}

func TestScriptsHandleRootWithoutSudo(t *testing.T) {
	for name, s := range map[string]string{"deploy-user": deployUserScript, "authorize-key": authorizeScript} {
		if !strings.Contains(s, `if [ "$(id -u)" = 0 ]; then SUDO=""; else SUDO="sudo -n"; fi`) {
			t.Errorf("%s: no SUDO switch", name)
		}
	}
	if !strings.Contains(authorizeScript, "$SUDO runuser -u deploy --") || !strings.Contains(authorizeScript, "grep -qxF -e") {
		t.Error("authorize script: runuser / grep -e missing")
	}
}

func TestAuthorizeKeyRejectsBadPubkeys(t *testing.T) {
	steps, p := plan(t, config.TargetExisting)
	for _, pub := range []string{"ssh-ed25519 A k\nssh-ed25519 B k", "not a key", "ssh-ed25519 A\rk"} {
		r := &fakeRunner{}
		env := &engine.Env{Root: p.Root, Run: r, In: engine.Outputs{"deploy_pubkey": pub, "server_sudo": "true"}}
		if _, err := byID(steps, "server:authorize-key").Apply(context.Background(), env); err == nil || len(r.cmds) != 0 {
			t.Errorf("%q accepted", pub)
		}
	}
	// empty in env falls back to disk, which is absent here.
	r := &fakeRunner{}
	env := &engine.Env{Root: p.Root, Run: r, In: engine.Outputs{"server_sudo": "true"}}
	if _, err := byID(steps, "server:authorize-key").Apply(context.Background(), env); err == nil || len(r.cmds) != 0 {
		t.Error("empty pubkey accepted")
	}
}

func TestDetectPresent(t *testing.T) {
	if New().Detect(&detect.Project{}) != engine.StatusPresent {
		t.Error("detect must not report missing")
	}
}

func TestDestroyScriptShape(t *testing.T) {
	s := DestroyScript()
	for _, bad := range []string{"network rm", "userdel", "apt-get", "docker stop", "docker rm ", "docker restart"} {
		if strings.Contains(s, bad) {
			t.Errorf("destroy script contains %q", bad)
		}
	}
	down, dir, keys := strings.Index(s, "down -v"), strings.Index(s, `rm -rf -- "$dir"`), strings.LastIndex(s, "authorized_keys")
	if down < 0 || dir < down || keys < dir {
		t.Errorf("order wrong: down %d, rm dir %d, authorized_keys %d", down, dir, keys)
	}
}
