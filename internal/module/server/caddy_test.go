package server

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jean-baptisteAZAN/vauban/internal/config"
	"github.com/jean-baptisteAZAN/vauban/internal/engine"
)

var update = flag.Bool("update", false, "regenerate golden files")

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("..", "..", "..", "testdata", "golden", "server", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("mismatch with %s:\n%s", path, got)
	}
}

func TestGoldenCompose(t *testing.T) {
	golden(t, "caddy-compose.yaml", RenderCompose())
	for _, w := range []string{"image: caddy:2", "container_name: vauban-caddy", "restart: unless-stopped",
		`"80:80"`, `"443:443"`, `"443:443/udp"`, "./Caddyfile:/etc/caddy/Caddyfile:ro", "./sites:/etc/caddy/sites:ro",
		"caddy_data:", "caddy_config:", "external: true"} {
		if !strings.Contains(RenderCompose(), w) {
			t.Errorf("compose lacks %q", w)
		}
	}
}

func TestGoldenSite(t *testing.T) {
	got := RenderSite("shop", "shop.example.com", 3000)
	golden(t, "site.caddy", got)
	if got != "shop.example.com {\n\treverse_proxy shop-app:3000\n}\n" {
		t.Fatalf("site: %q", got)
	}
}

// failRunner fails any call whose script contains failOn, returning errOut.
type failRunner struct {
	fakeRunner
	failOn string
	out    string
	err    error
}

func (f *failRunner) Run(ctx context.Context, c engine.Cmd) ([]byte, error) {
	_, _ = f.fakeRunner.Run(ctx, c)
	last := strings.Join(c.Args, " ")
	if f.failOn != "" && strings.Contains(last, f.failOn) {
		return []byte(f.out), f.err
	}
	return nil, nil
}

func stepEnv(t *testing.T, c *config.Config, id string, in engine.Outputs, r engine.Runner) (engine.Outputs, error) {
	t.Helper()
	p := proj(t)
	steps, err := New().Plan(p, c)
	if err != nil {
		t.Fatal(err)
	}
	st := byID(steps, id)
	if st == nil {
		t.Fatalf("no step %s", id)
	}
	if st.Describe() == "" {
		t.Fatal("empty describe")
	}
	return st.Apply(context.Background(), &engine.Env{Root: p.Root, Run: r, In: in})
}

func TestPlanIncludesCaddySteps(t *testing.T) {
	steps, _ := plan(t, config.TargetExisting)
	var ids []string
	for _, s := range steps {
		ids = append(ids, s.ID())
	}
	want := "server:audit server:docker server:deploy-user server:deploy-key server:authorize-key server:network server:caddy server:project server:site"
	if got := strings.Join(ids, " "); got != want {
		t.Fatalf("got %s", got)
	}
	if ProjectsDir != "apps" {
		t.Fatal("ProjectsDir")
	}
}

func TestAuditFailsWithGuideOnUserNeedsSetup(t *testing.T) {
	steps, p := plan(t, config.TargetExisting)
	r := &fakeRunner{out: readFixture(t, "audit-user-caddy-baked.txt")}
	_, err := byID(steps, "server:audit").Apply(context.Background(), &engine.Env{Root: p.Root, Run: r, In: engine.Outputs{}})
	if err == nil || !strings.Contains(err.Error(), "not set up for vauban") {
		t.Fatalf("want guide error: %v", err)
	}
	if len(r.cmds) != 1 {
		t.Fatalf("calls %d", len(r.cmds))
	}
}

func TestNetwork(t *testing.T) {
	r := &fakeRunner{}
	if _, err := stepEnv(t, cfg(config.TargetExisting), "server:network", engine.Outputs{"server_sudo": "true"}, r); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(strings.Join(r.cmds[0].Args, " "), "sh -s -- 'sudo'") {
		t.Fatalf("args %v", r.cmds[0].Args)
	}
	for _, w := range []string{"docker network inspect vauban", "docker network create vauban", "set -eu", "runuser -u deploy --"} {
		if !strings.Contains(r.stdi[0], w) {
			t.Errorf("script lacks %q", w)
		}
	}
	// no sudo, in docker group: direct.
	r = &fakeRunner{}
	if _, err := stepEnv(t, cfg(config.TargetExisting), "server:network", engine.Outputs{"server_sudo": "false", "server_docker_group": "true"}, r); err != nil ||
		!strings.HasSuffix(strings.Join(r.cmds[0].Args, " "), "sh -s -- 'direct'") {
		t.Fatalf("direct: %v %v", err, r.cmds)
	}
	// no sudo, not in docker group: error, no ssh.
	r = &fakeRunner{}
	if _, err := stepEnv(t, cfg(config.TargetExisting), "server:network", engine.Outputs{"server_sudo": "false"}, r); err == nil || len(r.cmds) != 0 {
		t.Fatalf("want error, no ssh: %v %d", err, len(r.cmds))
	}
	// no sudo, ssh user is deploy: direct.
	c := cfg(config.TargetExisting)
	c.Server.User = "deploy"
	r = &fakeRunner{}
	if _, err := stepEnv(t, c, "server:network", engine.Outputs{"server_sudo": "false"}, r); err != nil || len(r.cmds) != 1 {
		t.Fatalf("deploy: %v", err)
	}
}

func TestCaddyNoops(t *testing.T) {
	for _, mode := range []string{"vauban-ready", "user-ready"} {
		r := &fakeRunner{}
		if _, err := stepEnv(t, cfg(config.TargetExisting), "server:caddy", engine.Outputs{"server_caddy_mode": mode, "server_sudo": "false"}, r); err != nil || len(r.cmds) != 0 {
			t.Fatalf("%s: %v %d", mode, err, len(r.cmds))
		}
	}
}

func TestCaddyUserNeedsSetupReturnsGuide(t *testing.T) {
	r := &fakeRunner{}
	in := engine.Outputs{"server_caddy_mode": "user-needs-setup", "server_sudo": "true", "server_caddy_guide": "GUIDE TEXT: mount sites"}
	_, err := stepEnv(t, cfg(config.TargetExisting), "server:caddy", in, r)
	if err == nil || !strings.Contains(err.Error(), "GUIDE TEXT: mount sites") || len(r.cmds) != 0 {
		t.Fatalf("err=%v calls=%d", err, len(r.cmds))
	}
}

func TestCaddyBlockedAndUnknown(t *testing.T) {
	for _, mode := range []string{"blocked", "", "weird"} {
		r := &fakeRunner{}
		if _, err := stepEnv(t, cfg(config.TargetExisting), "server:caddy", engine.Outputs{"server_caddy_mode": mode, "server_sudo": "true"}, r); err == nil || len(r.cmds) != 0 {
			t.Fatalf("%q: %v %d", mode, err, len(r.cmds))
		}
	}
}

func TestCaddyInstall(t *testing.T) {
	r := &fakeRunner{}
	if _, err := stepEnv(t, cfg(config.TargetExisting), "server:caddy", engine.Outputs{"server_caddy_mode": "install", "server_sudo": "false"}, r); err == nil || len(r.cmds) != 0 {
		t.Fatalf("needs sudo: %v %d", err, len(r.cmds))
	}
	if _, err := stepEnv(t, cfg(config.TargetExisting), "server:caddy", engine.Outputs{"server_caddy_mode": "install", "server_sudo": "false"}, r); !strings.Contains(err.Error(), "Caddy") {
		t.Fatalf("err should name Caddy: %v", err)
	}
	if _, err := stepEnv(t, cfg(config.TargetExisting), "server:caddy", engine.Outputs{"server_caddy_mode": "install", "server_sudo": "true"}, r); err != nil {
		t.Fatal(err)
	}
	args := r.cmds[0].Args
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "'"+base64.StdEncoding.EncodeToString([]byte(RenderCompose()))+"'") ||
		!strings.Contains(joined, "'"+base64.StdEncoding.EncodeToString([]byte("import /etc/caddy/sites/*.caddy\n"))+"'") {
		t.Fatalf("args: %s", joined)
	}
	s := r.stdi[0]
	for _, w := range []string{"set -eu", `SUDO="sudo -n"`, "base64 -d", "docker compose -p vauban-caddy", "/opt/vauban/caddy",
		"install -d -o deploy", "changed by hand", "cmp -s"} {
		if !strings.Contains(s, w) {
			t.Errorf("script lacks %q", w)
		}
	}
	if strings.Contains(s, "services:") || strings.Contains(s, "restart") {
		t.Error("compose content in script text")
	}
}

func TestCaddyInstallHandEditedError(t *testing.T) {
	r := &failRunner{failOn: "sh -s", out: "", err: errors.New("exit 7: vauban-caddy config was changed by hand: x")}
	_, err := stepEnv(t, cfg(config.TargetExisting), "server:caddy", engine.Outputs{"server_caddy_mode": "install", "server_sudo": "true"}, r)
	if err == nil || !strings.Contains(err.Error(), "changed by hand") {
		t.Fatal(err)
	}
}

func TestProjectCreates(t *testing.T) {
	r := &fakeRunner{}
	if _, err := stepEnv(t, cfg(config.TargetExisting), "server:project", engine.Outputs{"server_sudo": "true"}, r); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(strings.Join(r.cmds[0].Args, " "), "sh -s -- 'sudo' 'shop' 'me/shop'") {
		t.Fatalf("args %v", r.cmds[0].Args)
	}
	for _, w := range []string{"mktemp -d \"$apps/.vauban-new.XXXXXX\"", "mv -T \"$new\" \"$dir\"", "[ -e \"$dir\" ] || [ -L \"$dir\" ]", "chmod 750 \"$new\"", ".vauban-owner", "-L ", "chmod 644", "runuser -u deploy --", "VAUBAN_OWNER_CONFLICT"} {
		if !strings.Contains(r.stdi[0], w) {
			t.Errorf("script lacks %q", w)
		}
	}
	if strings.Contains(r.stdi[0], "mkdir -p \"$dir\"") || strings.Contains(r.stdi[0], "me/shop") {
		t.Error("project mkdir -p or slug in script text")
	}
}

func TestProjectOwnerMismatch(t *testing.T) {
	r := &failRunner{failOn: "'shop'", out: "VAUBAN_OWNER_CONFLICT acme/other\n", err: errors.New("ssh: exit status 3")}
	_, err := stepEnv(t, cfg(config.TargetExisting), "server:project", engine.Outputs{"server_sudo": "true"}, r)
	if err == nil || !strings.Contains(err.Error(), "acme/other") || !strings.Contains(err.Error(), "shop") {
		t.Fatalf("err: %v", err)
	}
	r = &failRunner{failOn: "'shop'", out: "VAUBAN_OWNER_CONFLICT -\n", err: errors.New("exit 3")}
	_, err = stepEnv(t, cfg(config.TargetExisting), "server:project", engine.Outputs{"server_sudo": "true"}, r)
	if err == nil || !strings.Contains(err.Error(), "without a .vauban-owner") {
		t.Fatalf("err: %v", err)
	}
}

func TestProjectNeedsSudoUnlessDeploy(t *testing.T) {
	r := &fakeRunner{}
	if _, err := stepEnv(t, cfg(config.TargetExisting), "server:project", engine.Outputs{"server_sudo": "false"}, r); err == nil || len(r.cmds) != 0 {
		t.Fatalf("want error: %v", err)
	}
	c := cfg(config.TargetExisting)
	c.Server.User = "deploy"
	if _, err := stepEnv(t, c, "server:project", engine.Outputs{"server_sudo": "false"}, r); err != nil || len(r.cmds) != 1 {
		t.Fatalf("deploy: %v", err)
	}
}

func siteIn() engine.Outputs {
	return engine.Outputs{"server_sudo": "true", "server_sites_dir": "/opt/vauban/caddy/sites", "server_caddy_container": "vauban-caddy", "server_caddy_mode": "install"}
}

func TestSiteWritesValidatesReloads(t *testing.T) {
	r := &fakeRunner{}
	if _, err := stepEnv(t, cfg(config.TargetExisting), "server:site", siteIn(), r); err != nil {
		t.Fatal(err)
	}
	if len(r.cmds) != 4 {
		t.Fatalf("calls %d", len(r.cmds))
	}
	for i, act := range []string{"'write'", "'validate'", "'commit'", "'reload'"} {
		if !strings.Contains(strings.Join(r.cmds[i].Args, " "), "sh -s -- "+act+" 'sudo' '/opt/vauban/caddy/sites' 'shop' 'me/shop' 'vauban-caddy'") {
			t.Errorf("call %d args: %v", i, r.cmds[i].Args)
		}
	}
	w := strings.Join(r.cmds[0].Args, " ")
	b64 := base64.StdEncoding.EncodeToString([]byte("shop.example.com {\n\treverse_proxy shop-app:3000\n}\n"))
	if !strings.Contains(w, "'"+b64+"'") {
		t.Fatalf("site content not passed: %s", w)
	}
	for _, s := range r.stdi {
		if strings.Contains(s, "shop.example.com") || strings.Contains(s, "shop-app") || strings.Contains(s, "me/shop") {
			t.Error("domain/project in script text")
		}
	}
	for _, w := range []string{"caddy validate --config /etc/caddy/Caddyfile", "caddy reload --config /etc/caddy/Caddyfile", "mktemp", "mv -fT", "sha256sum", ".vauban-site", "VAUBAN_SITE_FOREIGN", "[ ! -s \"$dec\" ]", "trap cleanup", "tail -n 20", "base64 -d", "set -eu", "test -L \"$file\""} {
		if !strings.Contains(siteScript, w) {
			t.Errorf("site script lacks %q", w)
		}
	}
	if strings.Contains(siteScript, "docker restart") || strings.Contains(siteScript, "compose") {
		t.Error("site script touches caddy container lifecycle")
	}
}

func TestSiteValidateFailureRemovesFileNoReload(t *testing.T) {
	r := &failRunner{failOn: "'validate'", err: errors.New("ssh: exit status 6: Error: adapting config: unexpected token")}
	_, err := stepEnv(t, cfg(config.TargetExisting), "server:site", siteIn(), r)
	if err == nil || !strings.Contains(err.Error(), "Caddy config invalid after adding shop.example.com") || !strings.Contains(err.Error(), "unexpected token") {
		t.Fatalf("err: %v", err)
	}
	var acts []string
	for _, c := range r.cmds {
		j := strings.Join(c.Args, " ")
		for _, a := range []string{"'write'", "'validate'", "'rollback'", "'commit'", "'reload'"} {
			if strings.Contains(j, "sh -s -- "+a) {
				acts = append(acts, a)
			}
		}
	}
	if strings.Join(acts, " ") != "'write' 'validate' 'rollback'" {
		t.Fatalf("calls: %v", acts)
	}
}

func TestSiteReloadFailureKeepsFile(t *testing.T) {
	r := &failRunner{failOn: "'reload'", err: errors.New("exit 1: boom")}
	if _, err := stepEnv(t, cfg(config.TargetExisting), "server:site", siteIn(), r); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err: %v", err)
	}
}

func TestSiteTrimsLongValidateOutput(t *testing.T) {
	var lines []string
	for i := 0; i < 50; i++ {
		lines = append(lines, "line"+string(rune('A'+i%26)))
	}
	r := &failRunner{failOn: "'validate'", err: errors.New(strings.Join(lines, "\n"))}
	_, err := stepEnv(t, cfg(config.TargetExisting), "server:site", siteIn(), r)
	if err == nil || strings.Count(err.Error(), "\n") > 22 {
		t.Fatalf("not trimmed: %v", err)
	}
}

func TestSiteOwnerMismatch(t *testing.T) {
	r := &failRunner{failOn: "'write'", out: "VAUBAN_OWNER_CONFLICT acme/other\n", err: errors.New("exit 3")}
	_, err := stepEnv(t, cfg(config.TargetExisting), "server:site", siteIn(), r)
	if err == nil || !strings.Contains(err.Error(), "acme/other") || len(r.cmds) != 1 {
		t.Fatalf("err=%v calls=%d", err, len(r.cmds))
	}
}

func TestSiteRejectsBadInputs(t *testing.T) {
	for name, mut := range map[string]func(engine.Outputs){
		"relative":   func(o engine.Outputs) { o["server_sites_dir"] = "sites" },
		"newline":    func(o engine.Outputs) { o["server_sites_dir"] = "/a\n/b" },
		"empty":      func(o engine.Outputs) { o["server_sites_dir"] = "" },
		"container":  func(o engine.Outputs) { o["server_caddy_container"] = "-rm" },
		"needsSetup": func(o engine.Outputs) { o["server_caddy_mode"] = "user-needs-setup" },
		"nosudo":     func(o engine.Outputs) { o["server_sudo"] = "false" },
	} {
		in := siteIn()
		mut(in)
		r := &fakeRunner{}
		if _, err := stepEnv(t, cfg(config.TargetExisting), "server:site", in, r); err == nil || len(r.cmds) != 0 {
			t.Errorf("%s: err=%v calls=%d", name, err, len(r.cmds))
		}
	}
	c := cfg(config.TargetExisting)
	c.Domain = "x.com {\n\timport evil"
	r := &fakeRunner{}
	if _, err := stepEnv(t, c, "server:site", siteIn(), r); err == nil || len(r.cmds) != 0 {
		t.Errorf("bad domain accepted: %v", err)
	}
}

func TestNewScriptsHandleRootWithoutSudo(t *testing.T) {
	for name, s := range map[string]string{"network": networkScript, "caddy": caddyInstallScript, "project": projectScript, "site": siteScript} {
		if !strings.Contains(s, `if [ "$(id -u)" = 0 ]; then SUDO=""; else SUDO="sudo -n"; fi`) || !strings.Contains(s, "set -eu") {
			t.Errorf("%s: no SUDO switch / set -eu", name)
		}
	}
}

func TestSiteForeignFileRefused(t *testing.T) {
	r := &failRunner{failOn: "'write'", out: "VAUBAN_SITE_FOREIGN\n", err: errors.New("exit 8")}
	_, err := stepEnv(t, cfg(config.TargetExisting), "server:site", siteIn(), r)
	if err == nil || !strings.Contains(err.Error(), "/opt/vauban/caddy/sites/shop.caddy already exists and was not written by vauban") {
		t.Fatalf("err: %v", err)
	}
	if len(r.cmds) != 1 {
		t.Fatalf("only the write call expected, got %d (no rollback/remove of a foreign file)", len(r.cmds))
	}
}

func TestSiteRollbackCarriesContentForHashCheck(t *testing.T) {
	r := &failRunner{failOn: "'validate'", err: errors.New("exit 6: bad")}
	_, _ = stepEnv(t, cfg(config.TargetExisting), "server:site", siteIn(), r)
	b := b64(RenderSite("shop", "shop.example.com", 3000))
	last := strings.Join(r.cmds[len(r.cmds)-1].Args, " ")
	if !strings.Contains(last, "'rollback'") || !strings.Contains(last, "'"+b+"'") {
		t.Fatalf("rollback args: %s", last)
	}
	for _, w := range []string{`mv -fT "$bak" "$file"`, `writer rm -f "$file"`, `want=$(sha256sum`} {
		if !strings.Contains(siteScript, w) {
			t.Errorf("rollback lacks %q", w)
		}
	}
}

func TestSiteScriptHashGatesOverwrite(t *testing.T) {
	// the foreign check precedes the backup and the write.
	i := strings.Index(siteScript, "is_ours_file; then")
	j := strings.Index(siteScript, `writer cp -p "$file" "$bak"`)
	k := strings.Index(siteScript, `writer mv -fT "$tmp" "$file"`)
	if i < 0 || j < i || k < j {
		t.Fatalf("order: %d %d %d", i, j, k)
	}
	if !strings.Contains(siteScript, `[ "$(hash_of "$file")" = "$rec" ]`) || !strings.Contains(siteScript, `getent passwd deploy`) || !strings.Contains(siteScript, "deploy user does not exist") {
		t.Error("hash comparison / home check missing")
	}
}

func TestOwnerErrorStripsControlChars(t *testing.T) {
	r := &failRunner{failOn: "'shop'", out: "VAUBAN_OWNER_CONFLICT evil\x1b[31m/x\r\n", err: errors.New("exit 3")}
	_, err := stepEnv(t, cfg(config.TargetExisting), "server:project", engine.Outputs{"server_sudo": "true"}, r)
	if err == nil || strings.ContainsAny(err.Error(), "\x1b\r") || !strings.Contains(err.Error(), "evil[31m/x") {
		t.Fatalf("err: %q", err)
	}
}

func TestNetworkRaceSafe(t *testing.T) {
	if !strings.Contains(networkScript, "docker network create vauban >/dev/null 2>&1 || as_deploy docker network inspect vauban >/dev/null") {
		t.Error("create-then-inspect missing")
	}
}
