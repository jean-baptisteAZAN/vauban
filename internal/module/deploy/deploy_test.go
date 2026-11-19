package deploy

import (
	"context"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/jean-baptisteAZAN/vauban/internal/config"
	"github.com/jean-baptisteAZAN/vauban/internal/detect"
	"github.com/jean-baptisteAZAN/vauban/internal/engine"
)

var update = flag.Bool("update", false, "regenerate golden files")

func goldenPath(name string) string {
	return filepath.Join("..", "..", "..", "testdata", "golden", "deploy", name)
}

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := goldenPath(name)
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

func goApp(t *testing.T) *detect.Project {
	t.Helper()
	p, err := detect.Scan(filepath.Join("..", "..", "..", "testdata", "go-app"))
	if err != nil {
		t.Fatal(err)
	}
	p.GitHubRepo = "Acme/Widget"
	return p
}

// unknownStack has no CI workflow and none the ci module would generate.
func unknownStack(t *testing.T) *detect.Project {
	return &detect.Project{Root: t.TempDir(), GitHubRepo: "Acme/Widget"}
}

func cfg() *config.Config {
	return &config.Config{Project: "widget", Cloud: "hetzner", AppPort: 8080}
}

func plan(t *testing.T, p *detect.Project, c *config.Config) []engine.Step {
	t.Helper()
	steps, err := New().Plan(p, c)
	if err != nil {
		t.Fatal(err)
	}
	return steps
}

func file(t *testing.T, steps []engine.Step, path string) string {
	t.Helper()
	for _, s := range steps {
		if w, ok := s.(engine.WriteFile); ok && w.Path == path {
			return string(w.Content)
		}
	}
	t.Fatalf("no WriteFile %s", path)
	return ""
}

func TestModuleMeta(t *testing.T) {
	m := New()
	if m.ID() != "deploy" || m.Title() != "deploy" {
		t.Errorf("id/title = %q/%q", m.ID(), m.Title())
	}
	if want := []string{"container", "ci", "infra", "server"}; !reflect.DeepEqual(m.Requires(), want) {
		t.Errorf("requires = %v", m.Requires())
	}
	if m.Detect(&detect.Project{}) != engine.StatusMissing {
		t.Error("empty project not missing")
	}
	if m.Detect(&detect.Project{Workflows: []string{"deploy.yml"}}) != engine.StatusPresent {
		t.Error("deploy.yml not present")
	}
}

func TestPlanSteps(t *testing.T) {
	steps := plan(t, goApp(t), cfg())
	var ids []string
	for _, s := range steps {
		ids = append(ids, s.ID())
	}
	want := []string{
		"deploy:mode", "file:deploy/compose.yaml", "file:deploy/Caddyfile", "file:.github/workflows/deploy.yml",
		"deploy:domain", "secret:DEPLOY_HOST", "secret:DEPLOY_SSH_KEY", "secret:DEPLOY_DOMAIN",
	}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids = %v", ids)
	}
	secrets := map[string]engine.SetSecret{}
	for _, s := range steps {
		if ss, ok := s.(engine.SetSecret); ok {
			secrets[ss.Name] = ss
		}
	}
	for name, want := range map[string]engine.SetSecret{
		"DEPLOY_HOST":    {Repo: "Acme/Widget", Name: "DEPLOY_HOST", FromOutput: "server_ip"},
		"DEPLOY_SSH_KEY": {Repo: "Acme/Widget", Name: "DEPLOY_SSH_KEY", FromFile: "$deploy_key_path"},
		"DEPLOY_DOMAIN":  {Repo: "Acme/Widget", Name: "DEPLOY_DOMAIN", FromOutput: "domain"},
	} {
		if secrets[name] != want {
			t.Errorf("%s = %#v", name, secrets[name])
		}
	}
	for _, s := range steps {
		if d := s.Describe(); strings.Contains(d, "BEGIN") {
			t.Errorf("describe leaks key material: %s", d)
		}
	}
}

func TestPlanNeedsGitHubRemote(t *testing.T) {
	p := goApp(t)
	p.GitHubRepo = ""
	if _, err := New().Plan(p, cfg()); err == nil || !strings.Contains(err.Error(), "deploy needs a GitHub remote") {
		t.Errorf("err = %v", err)
	}
}

func TestGoldenFiles(t *testing.T) {
	steps := plan(t, goApp(t), cfg())
	compose := file(t, steps, "deploy/compose.yaml")
	checkGolden(t, "compose.yaml", compose)
	caddy := file(t, steps, "deploy/Caddyfile")
	checkGolden(t, "Caddyfile", caddy)
	if caddy != "{$DOMAIN} {\n\treverse_proxy app:{$APP_PORT}\n}\n" {
		t.Errorf("Caddyfile = %q", caddy)
	}

	var c struct {
		Services map[string]struct {
			Image       string            `yaml:"image"`
			Restart     string            `yaml:"restart"`
			Ports       []string          `yaml:"ports"`
			EnvFile     any               `yaml:"env_file"`
			Environment map[string]string `yaml:"environment"`
			Volumes     []string          `yaml:"volumes"`
		} `yaml:"services"`
		Volumes map[string]any `yaml:"volumes"`
	}
	if err := yaml.Unmarshal([]byte(compose), &c); err != nil {
		t.Fatalf("compose yaml: %v", err)
	}
	caddySvc, app := c.Services["caddy"], c.Services["app"]
	if caddySvc.Image != "caddy:2" || app.Image != "ghcr.io/acme/widget:${IMAGE_TAG:?IMAGE_TAG must be set}" {
		t.Errorf("images = %q, %q", caddySvc.Image, app.Image)
	}
	if !reflect.DeepEqual(caddySvc.Ports, []string{"80:80", "443:443", "443:443/udp"}) {
		t.Errorf("ports = %v", caddySvc.Ports)
	}
	if len(app.Ports) != 0 || app.EnvFile != ".env" {
		t.Errorf("app = %#v", app)
	}
	if caddySvc.Restart != "unless-stopped" || app.Restart != "unless-stopped" {
		t.Error("restart policy")
	}
	if _, ok := caddySvc.Environment["DOMAIN"]; !ok {
		t.Error("caddy DOMAIN env")
	}
	if _, ok := caddySvc.Environment["APP_PORT"]; !ok {
		t.Error("caddy APP_PORT env")
	}
	if app.Environment["PORT"] != "${APP_PORT}" { // the app must listen on 0.0.0.0:$PORT
		t.Errorf("app PORT env = %q", app.Environment["PORT"])
	}
	for _, v := range []string{"caddy_data", "caddy_config"} {
		if _, ok := c.Volumes[v]; !ok {
			t.Errorf("volume %s not declared", v)
		}
	}
}

type workflow struct {
	Name        string         `yaml:"name"`
	On          map[string]any `yaml:"on"`
	Concurrency struct {
		Group            string `yaml:"group"`
		CancelInProgress bool   `yaml:"cancel-in-progress"`
	} `yaml:"concurrency"`
	Permissions map[string]string `yaml:"permissions"`
	Jobs        map[string]struct {
		If    string `yaml:"if"`
		Steps []struct {
			ID   string            `yaml:"id"`
			If   string            `yaml:"if"`
			Uses string            `yaml:"uses"`
			With map[string]string `yaml:"with"`
			Env  map[string]string `yaml:"env"`
			Run  string            `yaml:"run"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func parseWorkflow(t *testing.T, s string) workflow {
	t.Helper()
	var w workflow
	if err := yaml.Unmarshal([]byte(s), &w); err != nil {
		t.Fatalf("invalid yaml: %v\n%s", err, s)
	}
	return w
}

const pushGuard = "github.event.workflow_run.conclusion == 'success' && " +
	"github.event.workflow_run.event == 'push' && " +
	"github.event.workflow_run.head_repository.full_name == github.repository"

// TestForkPRCannotDeploy: a pull request (even from a fork's own main) runs
// CI with its own workflow file and so controls the CI conclusion; deploy
// must only follow push runs of this repository.
func TestForkPRCannotDeploy(t *testing.T) {
	w := parseWorkflow(t, file(t, plan(t, goApp(t), cfg()), ".github/workflows/deploy.yml"))
	guard := w.Jobs["deploy"].If
	for _, want := range []string{
		"github.event.workflow_run.event == 'push'",
		"github.event.workflow_run.head_repository.full_name == github.repository",
		"github.event.workflow_run.conclusion == 'success'",
	} {
		if !strings.Contains(guard, want) {
			t.Errorf("guard %q lacks %q", guard, want)
		}
	}
	if strings.Contains(guard, "||") {
		t.Errorf("guard has an alternative: %q", guard)
	}
}

// TestDeploySkipsUnlessTip: steps that touch the server only run when the
// commit is still the tip of main.
func TestDeploySkipsUnlessTip(t *testing.T) {
	for _, p := range []*detect.Project{goApp(t), unknownStack(t)} {
		w := parseWorkflow(t, file(t, plan(t, p, cfg()), ".github/workflows/deploy.yml"))
		var gated int
		for _, s := range w.Jobs["deploy"].Steps {
			if strings.Contains(s.Run, "scp ") || s.Env["SECRETS_JSON"] != "" {
				if s.If != "steps.tip.outputs.current == 'true'" {
					t.Errorf("step not gated on tip: if=%q", s.If)
				}
				gated++
			}
		}
		if gated != 2 {
			t.Errorf("gated steps = %d", gated)
		}
	}
}

func TestDeployWorkflow(t *testing.T) {
	for _, tc := range []struct {
		name, golden string
		p            func(*testing.T) *detect.Project
		workflowRun  bool
	}{
		{"workflow_run", "deploy-workflow-run.yml", goApp, true},
		{"push", "deploy-push.yml", unknownStack, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := file(t, plan(t, tc.p(t), cfg()), ".github/workflows/deploy.yml")
			checkGolden(t, tc.golden, got)
			w := parseWorkflow(t, got)
			if w.Name != "deploy" {
				t.Errorf("name = %q", w.Name)
			}
			if w.Concurrency.Group != "deploy" || w.Concurrency.CancelInProgress {
				t.Errorf("concurrency = %#v", w.Concurrency)
			}
			if w.Permissions["packages"] != "write" || w.Permissions["contents"] != "read" || len(w.Permissions) != 2 {
				t.Errorf("permissions = %v", w.Permissions)
			}
			job, ok := w.Jobs["deploy"]
			if !ok {
				t.Fatal("no deploy job")
			}
			_, hasRun := w.On["workflow_run"]
			_, hasPush := w.On["push"]
			if hasRun != tc.workflowRun || hasPush == tc.workflowRun {
				t.Errorf("on = %v", w.On)
			}
			sha := "${{ github.sha }}"
			if tc.workflowRun {
				sha = "${{ github.event.workflow_run.head_sha }}"
				if job.If != pushGuard {
					t.Errorf("if = %q", job.If)
				}
				trig := w.On["workflow_run"].(map[string]any)
				if !reflect.DeepEqual(trig["workflows"], []any{"ci"}) ||
					!reflect.DeepEqual(trig["branches"], []any{"main"}) ||
					!reflect.DeepEqual(trig["types"], []any{"completed"}) {
					t.Errorf("workflow_run = %v", trig)
				}
				if job.Steps[0].With["ref"] != sha {
					t.Errorf("checkout ref = %q", job.Steps[0].With["ref"])
				}
			} else if job.If != "" {
				t.Errorf("push variant has guard %q", job.If)
			}
			uses := map[string]bool{}
			for _, s := range job.Steps {
				uses[s.Uses] = true
				if strings.HasPrefix(s.Uses, "docker/build-push-action") {
					if s.With["push"] != "true" ||
						!strings.Contains(s.With["tags"], "ghcr.io/acme/widget:latest") ||
						!strings.Contains(s.With["tags"], "ghcr.io/acme/widget:"+sha) {
						t.Errorf("build-push with = %v", s.With)
					}
				}
				if strings.HasPrefix(s.Uses, "docker/login-action") {
					if s.With["registry"] != "ghcr.io" || s.With["password"] != "${{ secrets.GITHUB_TOKEN }}" {
						t.Errorf("login with = %v", s.With)
					}
				}
			}
			for _, u := range []string{"actions/checkout@v7", "docker/setup-buildx-action@v4", "docker/login-action@v4", "docker/build-push-action@v7"} {
				if !uses[u] {
					t.Errorf("missing %s", u)
				}
			}
			for u := range uses {
				if u != "" && !strings.HasPrefix(u, "actions/") && !strings.HasPrefix(u, "docker/") {
					t.Errorf("third-party action %s", u)
				}
			}
			for _, want := range []string{
				`APP_PORT: "8080"`,
				"ssh-keyscan -H \"$DEPLOY_HOST\"",
				"deploy/compose.yaml deploy/Caddyfile .env",
				"deploy@$DEPLOY_HOST:~/app/",
				"--password-stdin",
				"docker compose pull && docker compose up -d --remove-orphans",
				"IMAGE_TAG: " + sha,
				"IMAGE_TAG=%q",
				"DOCKER_CONFIG=$(mktemp -d)",
				`gh api "repos/$REPO/commits/main" --jq .sha`,
				"chmod 600 .env",
			} {
				if !strings.Contains(got, want) {
					t.Errorf("missing %q", want)
				}
			}
			checkSecretsHygiene(t, got, w)
			actionlint(t, got)
		})
	}
}

// checkSecretsHygiene: secrets reach steps only through env:, never echoed
// or put on a command line.
func checkSecretsHygiene(t *testing.T, got string, w workflow) {
	t.Helper()
	for _, line := range strings.Split(got, "\n") {
		l := strings.TrimSpace(line)
		if strings.Contains(l, "secrets.") || strings.Contains(l, "secrets)") {
			if strings.Contains(l, "echo") || strings.Contains(l, "run:") {
				t.Errorf("secret on command line: %s", l)
			}
		}
	}
	if strings.Contains(got, "echo") {
		t.Error("workflow uses echo")
	}
	if strings.Count(got, "toJSON(secrets)") != 1 {
		t.Error("toJSON(secrets) must appear exactly once")
	}
	for _, s := range w.Jobs["deploy"].Steps {
		for _, e := range []string{"${{", "secrets"} {
			if strings.Contains(s.Run, e) {
				t.Errorf("run script contains %q:\n%s", e, s.Run)
			}
		}
		for k, v := range s.Env {
			if v == "${{ toJSON(secrets) }}" && !strings.Contains(s.Run, `startswith("APP_")`) {
				t.Errorf("toJSON(secrets) in %s without APP_ filter", k)
			}
		}
	}
}

func actionlint(t *testing.T, content string) {
	t.Helper()
	bin, err := exec.LookPath("actionlint")
	if err != nil {
		return
	}
	path := filepath.Join(t.TempDir(), "deploy.yml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(bin, path).CombinedOutput(); err != nil {
		t.Errorf("actionlint: %v\n%s", err, out)
	}
}

func TestEnvFileScript(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not on PATH")
	}
	got := file(t, plan(t, goApp(t), cfg()), ".github/workflows/deploy.yml")
	w := parseWorkflow(t, got)
	var script string
	for _, s := range w.Jobs["deploy"].Steps {
		if s.Env["SECRETS_JSON"] != "" {
			script = s.Run
		}
	}
	if script == "" {
		t.Fatal("no .env step")
	}
	dir := t.TempDir()
	run := func(secrets string) (string, error) {
		cmd := exec.Command("bash", "-e", "-o", "pipefail", "-c", script)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "SECRETS_JSON="+secrets)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return string(out), err
		}
		b, err := os.ReadFile(filepath.Join(dir, ".env"))
		return string(b), err
	}
	env, err := run(`{"github_token":"ghs_x","DEPLOY_HOST":"1.2.3.4","DEPLOY_SSH_KEY":"k","APP_DATABASE_URL":"postgres://u:p@h/db?x=$y","APP_MULTI":"a\nb"}`)
	if err != nil {
		t.Fatalf("%v: %s", err, env)
	}
	want := "DATABASE_URL='postgres://u:p@h/db?x=$y'\nMULTI='a\nb'\n"
	if env != want {
		t.Errorf(".env = %q, want %q", env, want)
	}
	st, err := os.Stat(filepath.Join(dir, ".env"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf(".env mode = %v (%v)", st.Mode(), err)
	}
	if out, err := run(`{"APP_BAD":"it's"}`); err == nil {
		t.Error("single quote accepted")
	} else if strings.Contains(out, "it's") || !strings.Contains(out, "APP_BAD") {
		t.Errorf("error output = %q", out)
	}
}

func TestExistingCIWorkflowName(t *testing.T) {
	p := goApp(t)
	p.Root = t.TempDir()
	if err := os.MkdirAll(filepath.Join(p.Root, ".github", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.Root, ".github", "workflows", "ci.yml"), []byte("name: CI Pipeline\non: push\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p.Workflows = []string{"ci.yml"}
	w := parseWorkflow(t, file(t, plan(t, p, cfg()), ".github/workflows/deploy.yml"))
	trig, ok := w.On["workflow_run"].(map[string]any)
	if !ok || !reflect.DeepEqual(trig["workflows"], []any{"CI Pipeline"}) {
		t.Errorf("on = %v", w.On)
	}

	// No name: GitHub names the workflow after its path.
	if err := os.WriteFile(filepath.Join(p.Root, ".github", "workflows", "ci.yml"), []byte("on: push\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w = parseWorkflow(t, file(t, plan(t, p, cfg()), ".github/workflows/deploy.yml"))
	trig, _ = w.On["workflow_run"].(map[string]any)
	if !reflect.DeepEqual(trig["workflows"], []any{".github/workflows/ci.yml"}) {
		t.Errorf("on = %v", w.On)
	}
}

func TestSSLipDomain(t *testing.T) {
	for in, want := range map[string]string{
		"1.2.3.4":     "1-2-3-4.sslip.io",
		"203.0.113.7": "203-0-113-7.sslip.io",
	} {
		if got := SSLipDomain(in); got != want {
			t.Errorf("SSLipDomain(%q) = %q, want %q", in, got, want)
		}
	}
}

func findDomainStep(t *testing.T, steps []engine.Step) engine.Step {
	t.Helper()
	for _, s := range steps {
		if s.ID() == "deploy:domain" {
			return s
		}
	}
	t.Fatal("no deploy:domain step")
	return nil
}

func TestDomainStep(t *testing.T) {
	ctx := context.Background()
	s := findDomainStep(t, plan(t, goApp(t), cfg()))
	if d := s.Describe(); d != "deploy domain: sslip.io domain" {
		t.Errorf("describe before apply = %q", d)
	}
	out, err := s.Apply(ctx, &engine.Env{In: engine.Outputs{"server_ip": "1.2.3.4"}})
	if err != nil {
		t.Fatal(err)
	}
	if d := s.Describe(); d != "deploy domain: 1-2-3-4.sslip.io" {
		t.Errorf("describe after apply = %q, want the resolved domain", d)
	}
	if out["domain"] != "1-2-3-4.sslip.io" || out["deployed_ip"] != "1.2.3.4" || out["deployed_domain"] != "1-2-3-4.sslip.io" {
		t.Errorf("out = %v", out)
	}
	if _, err := s.Apply(ctx, &engine.Env{In: engine.Outputs{}}); err == nil {
		t.Error("no server_ip accepted")
	}

	c := cfg()
	c.Domain = "app.example.com"
	s = findDomainStep(t, plan(t, goApp(t), c))
	if !strings.Contains(s.Describe(), "app.example.com") {
		t.Errorf("describe = %q", s.Describe())
	}
	out, err = s.Apply(ctx, &engine.Env{In: engine.Outputs{"server_ip": "1.2.3.4"}})
	if err != nil {
		t.Fatal(err)
	}
	if out["domain"] != "app.example.com" || out["deployed_ip"] != "1.2.3.4" || out["deployed_domain"] != "app.example.com" {
		t.Errorf("out = %v", out)
	}
}

func TestStaleSteps(t *testing.T) {
	want := []string{"deploy:domain", "secret:DEPLOY_HOST", "secret:DEPLOY_DOMAIN"}
	deployed := engine.Outputs{"server_ip": "1.2.3.4", "deployed_ip": "1.2.3.4", "deployed_domain": "1-2-3-4.sslip.io"}
	with := func(kv ...string) engine.Outputs {
		o := engine.Outputs{}
		for k, v := range deployed {
			o[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			o[kv[i]] = kv[i+1]
		}
		return o
	}
	for _, tc := range []struct {
		name, domain string
		out          engine.Outputs
		want         []string
	}{
		{"fresh", "", engine.Outputs{}, nil},
		{"infra only", "", engine.Outputs{"server_ip": "1.2.3.4"}, nil},
		{"no change", "", deployed, nil},
		{"no change, custom domain", "app.example.com", with("deployed_domain", "app.example.com"), nil},
		{"ip changed", "", with("server_ip", "5.6.7.8"), want},
		{"ip changed, custom domain", "app.example.com", with("server_ip", "5.6.7.8", "deployed_domain", "app.example.com"), want},
		{"domain set", "app.example.com", deployed, want},
		{"domain changed", "new.example.com", with("deployed_domain", "app.example.com"), want},
		{"domain removed", "", with("deployed_domain", "app.example.com"), want},
	} {
		c := cfg()
		c.Domain = tc.domain
		if got := StaleSteps(&engine.State{Outputs: tc.out}, c); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestApplyFlow runs the domain and secret steps end to end with a fake gh.
func TestApplyFlow(t *testing.T) {
	root := t.TempDir()
	key := filepath.Join(t.TempDir(), "id")
	if err := os.WriteFile(key, []byte("PRIVATE-KEY-MATERIAL\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := goApp(t)
	var steps []engine.Step
	for _, s := range plan(t, p, cfg()) {
		if _, ok := s.(engine.WriteFile); !ok {
			steps = append(steps, s)
		}
	}
	fr := &engine.FakeRunner{}
	st := &engine.State{Outputs: engine.Outputs{"server_ip": "1.2.3.4", "deploy_key_path": key}}
	env := &engine.Env{Root: root, Run: fr}
	if err := engine.Execute(context.Background(), steps, st, env); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, c := range fr.Calls {
		b := make([]byte, 64)
		n, _ := c.Stdin.Read(b)
		got[c.Args[2]] = string(b[:n])
		if c.Args[4] != "Acme/Widget" {
			t.Errorf("repo = %v", c.Args)
		}
	}
	if got["DEPLOY_HOST"] != "1.2.3.4" || got["DEPLOY_DOMAIN"] != "1-2-3-4.sslip.io" || got["DEPLOY_SSH_KEY"] != "PRIVATE-KEY-MATERIAL\n" {
		t.Errorf("secrets = %v", got)
	}
	state, err := os.ReadFile(filepath.Join(root, ".vauban", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(state), "PRIVATE-KEY") {
		t.Error("key material in state")
	}
}

func existingCfg() *config.Config {
	return &config.Config{
		Project: "widget", Target: config.TargetExisting, AppPort: 8080, Domain: "app.example.com",
		Server: &config.Server{Host: "203.0.113.7", User: "root"},
	}
}

func TestExistingPlanSteps(t *testing.T) {
	p := goApp(t)
	steps := plan(t, p, existingCfg())
	var ids []string
	for _, s := range steps {
		ids = append(ids, s.ID())
	}
	want := []string{
		"deploy:mode", "file:deploy/compose.yaml", "file:.github/workflows/deploy.yml",
		"deploy:domain", "secret:DEPLOY_HOST", "secret:DEPLOY_SSH_KEY", "secret:DEPLOY_DOMAIN",
		"secret:DEPLOY_KNOWN_HOSTS", "secret:DEPLOY_PORT",
	}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids = %v", ids)
	}
	for _, s := range steps {
		ss, ok := s.(engine.SetSecret)
		if !ok {
			continue
		}
		switch ss.Name {
		case "DEPLOY_KNOWN_HOSTS":
			if ss.FromFile != filepath.Join(p.Root, ".vauban", "known_hosts") || ss.FromOutput != "" {
				t.Errorf("known_hosts = %#v", ss)
			}
		case "DEPLOY_PORT":
			if ss.FromOutput != "server_port" {
				t.Errorf("port = %#v", ss)
			}
		}
	}
}

func TestExistingGoldenFiles(t *testing.T) {
	steps := plan(t, goApp(t), existingCfg())
	compose := file(t, steps, "deploy/compose.yaml")
	checkGolden(t, "existing-compose.yaml", compose)
	if !strings.HasPrefix(compose, "# Generated by vauban (target: existing)\n") {
		t.Errorf("compose header: %q", compose)
	}
	var c struct {
		Name     string `yaml:"name"`
		Services map[string]struct {
			Image       string            `yaml:"image"`
			Restart     string            `yaml:"restart"`
			Ports       []string          `yaml:"ports"`
			EnvFile     any               `yaml:"env_file"`
			Environment map[string]string `yaml:"environment"`
			Container   string            `yaml:"container_name"`
			Networks    map[string]struct {
				Aliases []string `yaml:"aliases"`
			} `yaml:"networks"`
		} `yaml:"services"`
		Networks map[string]struct {
			External bool `yaml:"external"`
		} `yaml:"networks"`
	}
	if err := yaml.Unmarshal([]byte(compose), &c); err != nil {
		t.Fatal(err)
	}
	app := c.Services["app"]
	if len(c.Services) != 1 || c.Name != "widget" || app.Restart != "unless-stopped" || app.EnvFile != ".env" ||
		len(app.Ports) != 0 || app.Container != "" || app.Environment["PORT"] == "" ||
		!strings.HasPrefix(app.Image, "ghcr.io/acme/widget:${IMAGE_TAG:?") ||
		!reflect.DeepEqual(app.Networks["vauban"].Aliases, []string{"widget-app"}) || !c.Networks["vauban"].External {
		t.Errorf("compose = %#v", c)
	}
}

func TestExistingWorkflow(t *testing.T) {
	for _, tc := range []struct {
		golden string
		p      func(*testing.T) *detect.Project
	}{
		{"existing-deploy-workflow-run.yml", goApp},
		{"existing-deploy-push.yml", unknownStack},
	} {
		got := file(t, plan(t, tc.p(t), existingCfg()), ".github/workflows/deploy.yml")
		checkGolden(t, tc.golden, got)
		w := parseWorkflow(t, got)
		if !strings.HasPrefix(got, "# Generated by vauban (target: existing)\n") {
			t.Errorf("header: %q", got[:60])
		}
		if w.Jobs["deploy"].Steps == nil {
			t.Fatal("no steps")
		}
		for _, bad := range []string{"ssh-keyscan", "cloud-init", "mkdir -p ~/apps", "Caddyfile", "DOMAIN="} {
			if strings.Contains(got, bad) {
				t.Errorf("workflow contains %q", bad)
			}
		}
		for _, want := range []string{
			"StrictHostKeyChecking=yes", `-p "$DEPLOY_PORT"`, `-P "$DEPLOY_PORT"`,
			"DEPLOY_KNOWN_HOSTS: ${{ secrets.DEPLOY_KNOWN_HOSTS }}", "~/.ssh/known_hosts",
			"project='widget'", `deploy/compose.yaml .env "deploy@$DEPLOY_HOST:apps/$project/"`,
			"test -d ~/apps/$project", "--password-stdin", "DOCKER_CONFIG=$(mktemp -d)",
			"IMAGE_TAG=%q", `gh api "repos/$REPO/commits/main" --jq .sha`, "chmod 600 .env",
			"docker compose pull && docker compose up -d --remove-orphans",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q", want)
			}
		}
		if strings.Contains(got, "StrictHostKeyChecking=accept-new") || strings.Contains(got, "StrictHostKeyChecking=no") {
			t.Error("weak host key checking")
		}
		if tc.golden == "existing-deploy-workflow-run.yml" && w.Jobs["deploy"].If != pushGuard {
			t.Errorf("if = %q", w.Jobs["deploy"].If)
		}
		checkSecretsHygiene(t, got, w)
		actionlint(t, got)
	}
}

func TestModeGuard(t *testing.T) {
	cloud := plan(t, goApp(t), cfg())
	existing := plan(t, goApp(t), existingCfg())
	run := func(t *testing.T, steps []engine.Step, root string, force bool) error {
		t.Helper()
		_, err := steps[0].Apply(context.Background(), &engine.Env{Root: root, Force: force})
		return err
	}
	write := func(t *testing.T, root, path, content string) {
		t.Helper()
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"deploy/compose.yaml", ".github/workflows/deploy.yml"} {
		t.Run(path, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, path, file(t, cloud, path))
			err := run(t, existing, root, false)
			if err == nil || err.Error() != "deploy files were generated for target: cloud; rerun with --force to regenerate" {
				t.Errorf("cloud->existing err = %v", err)
			}
			if err := run(t, existing, root, true); err != nil {
				t.Errorf("force: %v", err)
			}
			if err := run(t, cloud, root, false); err != nil {
				t.Errorf("same mode: %v", err)
			}
			root = t.TempDir()
			write(t, root, path, file(t, existing, path))
			err = run(t, cloud, root, false)
			if err == nil || !strings.Contains(err.Error(), "generated for target: existing;") {
				t.Errorf("existing->cloud err = %v", err)
			}
			if err := run(t, existing, root, false); err != nil {
				t.Errorf("same mode: %v", err)
			}
		})
	}
	root := t.TempDir()
	write(t, root, "deploy/compose.yaml", "services: {}\n")
	if err := run(t, existing, root, false); err != nil {
		t.Errorf("hand-written file: %v", err)
	}
	if err := run(t, existing, t.TempDir(), false); err != nil {
		t.Errorf("no files: %v", err)
	}
}

func TestExistingKnownHostsSecretOnStdin(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".vauban"), 0o755); err != nil {
		t.Fatal(err)
	}
	kh := "[203.0.113.7]:2222 ssh-ed25519 AAAAhostkey\n"
	if err := os.WriteFile(filepath.Join(root, ".vauban", "known_hosts"), []byte(kh), 0o600); err != nil {
		t.Fatal(err)
	}
	p := goApp(t)
	p.Root = root
	var steps []engine.Step
	for _, s := range plan(t, p, existingCfg()) {
		if ss, ok := s.(engine.SetSecret); ok && (ss.Name == "DEPLOY_KNOWN_HOSTS" || ss.Name == "DEPLOY_PORT") {
			steps = append(steps, s)
		}
	}
	fr := &engine.FakeRunner{}
	st := &engine.State{Outputs: engine.Outputs{"server_port": "2222"}}
	if err := engine.Execute(context.Background(), steps, st, &engine.Env{Root: root, Run: fr}); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, c := range fr.Calls {
		b := make([]byte, 256)
		n, _ := c.Stdin.Read(b)
		got[c.Args[2]] = string(b[:n])
	}
	if got["DEPLOY_KNOWN_HOSTS"] != kh || got["DEPLOY_PORT"] != "2222" {
		t.Errorf("secrets = %q", got)
	}
}
