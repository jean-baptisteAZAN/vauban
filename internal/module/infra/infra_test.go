package infra

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/jean-baptisteAZAN/vauban/internal/cloud"
	"github.com/jean-baptisteAZAN/vauban/internal/config"
	"github.com/jean-baptisteAZAN/vauban/internal/detect"
	"github.com/jean-baptisteAZAN/vauban/internal/engine"
	"github.com/jean-baptisteAZAN/vauban/internal/tools"
)

var update = flag.Bool("update", false, "regenerate golden files")

const (
	deployPub = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDeployDeployDeployDeployDeployDeployDeploy vauban-shop"
	authorPub = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAuthorAuthorAuthorAuthorAuthorAuthorAuthor me@laptop"
	token     = "hcloud-secret-token"
)

// fakeProvider stands in for hetzner without network access.
type fakeProvider struct{ got *cloud.TFInput }

func (*fakeProvider) ID() string            { return "hetzner" }
func (*fakeProvider) DefaultRegion() string { return "nbg1" }
func (*fakeProvider) DefaultSize() string   { return "cx23" }
func (*fakeProvider) CredentialsEnv() ([]string, error) {
	return []string{"HCLOUD_TOKEN=" + token}, nil
}
func (*fakeProvider) CheckAuth(context.Context) error { return nil }
func (*fakeProvider) Regions(context.Context) ([]cloud.Option, error) {
	return nil, nil
}
func (*fakeProvider) Sizes(context.Context, string) ([]cloud.Option, error) {
	return nil, nil
}
func (*fakeProvider) Quota(context.Context, string, string, bool) (cloud.QuotaStatus, error) {
	return cloud.QuotaStatus{OK: true}, nil
}
func (f *fakeProvider) Terraform(in cloud.TFInput) ([]byte, error) {
	if f.got != nil {
		*f.got = in
	}
	return []byte("resource \"x\" \"y\" { size = \"" + in.Size + "\" keys = " + strings.Join(in.SSHPublicKeys, ",") + " }\n"), nil
}

// setup installs the fake provider, points HOME at a temp dir and returns a
// fresh project root.
func setup(t *testing.T) (*fakeProvider, string, string) {
	t.Helper()
	fp := &fakeProvider{got: &cloud.TFInput{}}
	t.Cleanup(cloud.Register(fp))
	home := t.TempDir()
	t.Setenv("HOME", home)
	return fp, home, t.TempDir()
}

func cfg() *config.Config {
	return &config.Config{Project: "shop", Cloud: "hetzner", Region: "nbg1", Size: "cx23", AppPort: 8080}
}

func project(t *testing.T, root string) *detect.Project {
	t.Helper()
	p, err := detect.Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func stepIDs(steps []engine.Step) []string {
	out := make([]string, len(steps))
	for i, s := range steps {
		out[i] = s.ID()
	}
	return out
}

func writeKey(t *testing.T, home, name, content string) string {
	t.Helper()
	dir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// keygenRunner fakes ssh-keygen by creating the key pair it was asked for and
// records every command like engine.FakeRunner.
type keygenRunner struct {
	engine.FakeRunner
}

func (r *keygenRunner) Run(ctx context.Context, cmd engine.Cmd) ([]byte, error) {
	if cmd.Name == "ssh-keygen" {
		path := cmd.Args[len(cmd.Args)-1]
		if err := os.WriteFile(path, []byte("PRIVATE KEY MATERIAL\n"), 0o600); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path+".pub", []byte(deployPub+"\n"), 0o644); err != nil {
			return nil, err
		}
	}
	return r.FakeRunner.Run(ctx, cmd)
}

const outputJSON = `{
  "server_ip": {"sensitive": false, "type": "string", "value": "203.0.113.7"},
  "server_ipv6": {"sensitive": false, "type": "string", "value": "2001:db8::1"}
}`

func TestModuleMeta(t *testing.T) {
	m := New()
	if m.ID() != "infra" || m.Title() != "infra" || len(m.Requires()) != 0 {
		t.Fatalf("meta = %q %q %v", m.ID(), m.Title(), m.Requires())
	}
}

func TestDetect(t *testing.T) {
	_, _, root := setup(t)
	if got := New().Detect(project(t, root)); got != engine.StatusMissing {
		t.Fatalf("empty: %v", got)
	}
	if err := os.WriteFile(filepath.Join(root, "main.tf"), []byte("# mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := New().Detect(project(t, root)); got != engine.StatusPresent {
		t.Fatalf("with tf: %v", got)
	}
}

func TestPlanUserTerraformUntouched(t *testing.T) {
	_, _, root := setup(t)
	if err := os.MkdirAll(filepath.Join(root, "infra"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "infra", "main.tf"), []byte("# my own infra\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	steps, err := New().Plan(project(t, root), cfg())
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 0 {
		t.Fatalf("steps = %v, want none", stepIDs(steps))
	}
}

func TestOutputParse(t *testing.T) {
	_, _, root := setup(t)
	steps, err := New().Plan(project(t, root), cfg())
	if err != nil {
		t.Fatal(err)
	}
	out := steps[7].(engine.RunCommand)
	got, err := out.Parse([]byte(outputJSON))
	if err != nil {
		t.Fatal(err)
	}
	if want := (engine.Outputs{"server_ip": "203.0.113.7", "server_ipv6": "2001:db8::1"}); !reflect.DeepEqual(got, want) {
		t.Fatalf("outputs = %v, want %v", got, want)
	}
	if _, err := out.Parse([]byte(`{}`)); err == nil {
		t.Fatal("missing server_ip: want error")
	}
	if _, err := out.Parse([]byte(`nope`)); err == nil {
		t.Fatal("bad json: want error")
	}
}

func TestDeployKeyExistsIsNoop(t *testing.T) {
	_, home, root := setup(t)
	path := writeKey(t, home, "vauban_shop_ed25519", "PRIVATE")
	writeKey(t, home, "vauban_shop_ed25519.pub", deployPub)
	steps, err := New().Plan(project(t, root), cfg())
	if err != nil {
		t.Fatal(err)
	}
	run := &engine.FakeRunner{}
	out, err := steps[0].Apply(context.Background(), &engine.Env{Root: root, Run: run, In: engine.Outputs{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Calls) != 0 {
		t.Fatalf("ran %v", run.Calls)
	}
	if out["deploy_key_path"] != path || out["deploy_pubkey"] != deployPub {
		t.Fatalf("outputs = %v", out)
	}
}

func TestFilesSkipIfExists(t *testing.T) {
	// a kept main.tf must still carry its header for the fingerprint
	const keep = "# managed by vauban\n# fingerprint: hetzner/nbg1/cx23\nkeep\n"
	_, _, root := setup(t)
	steps, err := New().Plan(project(t, root), cfg())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "infra"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"main.tf", "cloud-init.yaml"} {
		if err := os.WriteFile(filepath.Join(root, "infra", name), []byte(keep), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var log bytes.Buffer
	env := &engine.Env{Root: root, Run: &engine.FakeRunner{}, In: engine.Outputs{"deploy_pubkey": deployPub}, Log: &log}
	for _, s := range steps[1:3] {
		if _, err := s.Apply(context.Background(), env); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"main.tf", "cloud-init.yaml"} {
		b, _ := os.ReadFile(filepath.Join(root, "infra", name))
		if string(b) != keep {
			t.Errorf("%s overwritten without force", name)
		}
	}
	if !strings.Contains(log.String(), "skip (exists) infra/main.tf") || !strings.Contains(log.String(), "skip (exists) infra/cloud-init.yaml") {
		t.Errorf("log = %q", log.String())
	}
	env.Force = true
	for _, s := range steps[1:3] {
		if _, err := s.Apply(context.Background(), env); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"main.tf", "cloud-init.yaml"} {
		b, _ := os.ReadFile(filepath.Join(root, "infra", name))
		if string(b) == keep {
			t.Errorf("%s not overwritten with force", name)
		}
	}
}

func TestAuthorKeyFallbackRSA(t *testing.T) {
	_, home, _ := setup(t)
	rsa := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQCrsa me@old"
	writeKey(t, home, "id_rsa.pub", rsa)
	if got := authorKey(); got != rsa {
		t.Fatalf("authorKey = %q", got)
	}
	writeKey(t, home, "id_ed25519.pub", authorPub)
	if got := authorKey(); got != authorPub {
		t.Fatalf("authorKey = %q, want ed25519 preferred", got)
	}
}

func TestCloudInitGolden(t *testing.T) {
	got, err := renderCloudInit([]string{deployPub, authorPub})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("..", "..", "..", "testdata", "golden", "infra", "cloud-init.yaml")
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("mismatch with %s:\n%s", path, got)
	}
	if !bytes.HasPrefix(got, []byte("#cloud-config\n")) {
		t.Error("missing #cloud-config header")
	}
	if !bytes.Contains(got, []byte("bypass ufw")) {
		t.Error("missing comment on Docker ports bypassing ufw")
	}

	var doc struct {
		PackageUpdate  bool     `yaml:"package_update"`
		PackageUpgrade bool     `yaml:"package_upgrade"`
		Packages       []string `yaml:"packages"`
		Users          []struct {
			Name   string   `yaml:"name"`
			Groups string   `yaml:"groups"`
			Shell  string   `yaml:"shell"`
			Sudo   any      `yaml:"sudo"`
			Keys   []string `yaml:"ssh_authorized_keys"`
		} `yaml:"users"`
		WriteFiles []struct {
			Path    string `yaml:"path"`
			Content string `yaml:"content"`
		} `yaml:"write_files"`
		Runcmd []string `yaml:"runcmd"`
	}
	if err := yaml.Unmarshal(got, &doc); err != nil {
		t.Fatalf("invalid YAML: %v", err)
	}
	if !doc.PackageUpdate || !doc.PackageUpgrade {
		t.Error("package_update/upgrade not set")
	}
	for _, p := range []string{"ufw", "unattended-upgrades", "fail2ban", "ca-certificates", "curl"} {
		if !slices.Contains(doc.Packages, p) {
			t.Errorf("package %s missing", p)
		}
	}
	if len(doc.Users) != 1 {
		t.Fatalf("users = %+v", doc.Users)
	}
	u := doc.Users[0]
	if u.Name != "deploy" || u.Groups != "docker" || u.Shell != "/bin/bash" || u.Sudo != nil {
		t.Errorf("deploy user = %+v", u)
	}
	if !reflect.DeepEqual(u.Keys, []string{deployPub, authorPub}) {
		t.Errorf("keys = %q", u.Keys)
	}
	files := map[string]string{}
	for _, f := range doc.WriteFiles {
		files[f.Path] = f.Content
	}
	mustContain := map[string][]string{
		"/etc/ssh/sshd_config.d/10-vauban.conf":            {"PermitRootLogin no", "PasswordAuthentication no", "KbdInteractiveAuthentication no"},
		"/etc/apt/apt.conf.d/52vauban-unattended-upgrades": {`Unattended-Upgrade::Automatic-Reboot "true";`, `Unattended-Upgrade::Automatic-Reboot-Time "04:00";`, `Unattended-Upgrade::Origins-Pattern { "origin=Docker"; };`},
		"/etc/docker/daemon.json":                          {`{"log-driver":"json-file","log-opts":{"max-size":"10m","max-file":"3"}}`},
		"/etc/systemd/system/docker-prune.service":         {"docker image prune -af --filter until=168h"},
		"/etc/systemd/system/docker-prune.timer":           {"OnCalendar=daily"},
	}
	for path, subs := range mustContain {
		c, ok := files[path]
		if !ok {
			t.Errorf("write_files lacks %s", path)
			continue
		}
		for _, s := range subs {
			if !strings.Contains(c, s) {
				t.Errorf("%s lacks %q", path, s)
			}
		}
	}
	run := strings.Join(doc.Runcmd, "\n")
	for _, s := range []string{
		"https://download.docker.com/linux/debian/gpg",
		"Suites: trixie",
		"docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin",
		"ufw default deny incoming", "ufw default allow outgoing",
		"ufw allow 22/tcp", "ufw allow 80/tcp", "ufw allow 443/tcp", "ufw allow 443/udp", "ufw --force enable",
		"systemctl enable --now docker-prune.timer",
		"install -d -o deploy -g deploy -m 0755 /home/deploy/app",
	} {
		if !strings.Contains(run, s) {
			t.Errorf("runcmd lacks %q", s)
		}
	}

	if _, err := exec.LookPath("cloud-init"); err != nil {
		t.Log("cloud-init not installed: schema check skipped")
		return
	}
	if out, err := exec.Command("cloud-init", "schema", "--config-file", path).CombinedOutput(); err != nil {
		t.Errorf("cloud-init schema: %v\n%s", err, out)
	}
}

func TestCloudInitRejectsMultilineKey(t *testing.T) {
	if _, err := renderCloudInit([]string{"ssh-ed25519 AAAA\nruncmd: [evil]"}); err == nil {
		t.Fatal("want error for multi-line key")
	}
}

func tofuName() string { n, _ := tools.Tofu(); return n }

const (
	planText       = "Plan: 3 to add, 0 to change, 0 to destroy.\n"
	safePlanJSON   = `{"resource_changes":[{"change":{"actions":["create"]}},{"change":{"actions":["no-op"]}}]}`
	replacePlanJSN = `{"resource_changes":[{"change":{"actions":["update"]}},{"change":{"actions":["delete","create"]}}]}`
)

// newRunner fakes ssh-keygen and answers tofu plan/show/output.
func newRunner(showJSON string) *keygenRunner {
	tf := tofuName() + " -chdir=infra "
	return &keygenRunner{FakeRunner: engine.FakeRunner{
		Responses: map[string][]byte{
			tf + "plan -input=false -no-color -out=tfplan": []byte(planText),
			tf + "show -json tfplan":                       []byte(showJSON),
			tf + "output -json":                            []byte(outputJSON),
		},
		Errors: map[string]error{},
	}}
}

// apply mimics the CLI: load state, plan, clear stale steps, execute.
func apply(t *testing.T, root string, c *config.Config, run engine.Runner, confirm func(string, string) (bool, error)) (*engine.State, error) {
	t.Helper()
	return applyWith(t, root, c, run, confirm, false)
}

// applyWith is apply with --force.
func applyWith(t *testing.T, root string, c *config.Config, run engine.Runner, confirm func(string, string) (bool, error), force bool) (*engine.State, error) {
	t.Helper()
	st, err := engine.LoadState(root)
	if err != nil {
		t.Fatal(err)
	}
	steps, err := New().Plan(project(t, root), c)
	if err != nil {
		return st, err
	}
	for _, id := range StaleSteps(root, st, c) {
		delete(st.Done, id)
	}
	return st, engine.Execute(context.Background(), steps, st, &engine.Env{Root: root, Run: run, Confirm: confirm, Force: force})
}

// tofuVerbs lists the tofu subcommands run, in order.
func tofuVerbs(calls []engine.Cmd) []string {
	var v []string
	for _, c := range calls {
		if c.Name == tofuName() {
			v = append(v, c.Args[1])
		}
	}
	return v
}

func findCall(t *testing.T, calls []engine.Cmd, verb string) engine.Cmd {
	t.Helper()
	for _, c := range calls {
		if c.Name == tofuName() && c.Args[1] == verb {
			return c
		}
	}
	t.Fatalf("no tofu %s call", verb)
	return engine.Cmd{}
}

func writeMainTF(t *testing.T, root, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "infra"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "infra", "main.tf"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

var fullIDs = []string{"infra:deploy-key", "infra:main-tf", "infra:cloud-init", "file:infra/.gitignore", "infra:init", "infra:plan", "infra:apply", "infra:output"}

func TestPlanStepIDs(t *testing.T) {
	_, _, root := setup(t)
	steps, err := New().Plan(project(t, root), cfg())
	if err != nil {
		t.Fatal(err)
	}
	if got := stepIDs(steps); !reflect.DeepEqual(got, fullIDs) {
		t.Fatalf("ids = %v, want %v", got, fullIDs)
	}
}

func TestPlanManagedSameSteps(t *testing.T) {
	_, _, root := setup(t)
	writeMainTF(t, root, "# managed by vauban\n# fingerprint: hetzner/nbg1/cx23\nterraform {}\n")
	steps, err := New().Plan(project(t, root), cfg())
	if err != nil {
		t.Fatal(err)
	}
	if got := stepIDs(steps); !reflect.DeepEqual(got, fullIDs) {
		t.Fatalf("ids = %v, want %v", got, fullIDs)
	}
}

func TestPlanDescribe(t *testing.T) {
	_, home, root := setup(t)
	steps, err := New().Plan(project(t, root), cfg())
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(home, ".ssh", "vauban_shop_ed25519")
	tf := tofuName()
	want := []string{
		`$ ssh-keygen -t ed25519 -N "" -C vauban-shop -f ` + keyPath,
		"write infra/main.tf",
		"write infra/cloud-init.yaml",
		"write infra/.gitignore",
		"$ " + tf + " -chdir=infra init -input=false",
		"$ " + tf + " -chdir=infra plan -input=false -no-color -out=tfplan && " + tf + " -chdir=infra show -json tfplan",
		"$ " + tf + " -chdir=infra apply -input=false tfplan",
		"$ " + tf + " -chdir=infra output -json",
	}
	for i, s := range steps {
		if !strings.HasSuffix(s.Describe(), want[i]) {
			t.Errorf("step %s Describe = %q, want suffix %q", s.ID(), s.Describe(), want[i])
		}
		if strings.Contains(s.Describe(), token) || strings.Contains(s.Describe(), "auto-approve") {
			t.Errorf("step %s Describe = %q", s.ID(), s.Describe())
		}
	}
	if strings.Contains(steps[0].Describe(), "~") {
		t.Error("deploy key path not expanded")
	}
	gi := steps[3].(engine.WriteFile)
	if string(gi.Content) != "*.tfstate*\n.terraform/\ntfplan\n" {
		t.Errorf(".gitignore = %q", gi.Content)
	}
}

func TestExecuteFresh(t *testing.T) {
	fp, home, root := setup(t)
	writeKey(t, home, "id_ed25519.pub", authorPub)
	run := newRunner(safePlanJSON)
	st, err := apply(t, root, cfg(), run, nil)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(home, ".ssh", "vauban_shop_ed25519")
	tf := tofuName()
	var names [][]string
	for _, c := range run.Calls {
		names = append(names, append([]string{c.Name}, c.Args...))
	}
	want := [][]string{
		{"ssh-keygen", "-t", "ed25519", "-N", "", "-C", "vauban-shop", "-f", keyPath},
		{tf, "-chdir=infra", "init", "-input=false"},
		{tf, "-chdir=infra", "plan", "-input=false", "-no-color", "-out=tfplan"},
		{tf, "-chdir=infra", "show", "-json", "tfplan"},
		{tf, "-chdir=infra", "apply", "-input=false", "tfplan"},
		{tf, "-chdir=infra", "output", "-json"},
	}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("calls = %q\nwant %q", names, want)
	}
	for i, c := range run.Calls {
		hasTok := slices.Contains(c.Env, "HCLOUD_TOKEN="+token)
		if wantTok := i >= 2 && i <= 4; hasTok != wantTok {
			t.Errorf("call %v: credentials = %v, want %v", c.Args, hasTok, wantTok)
		}
	}
	if !reflect.DeepEqual(fp.got.SSHPublicKeys, []string{deployPub}) {
		t.Errorf("SSHPublicKeys = %q", fp.got.SSHPublicKeys)
	}
	mainTF, err := os.ReadFile(filepath.Join(root, "infra", "main.tf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(mainTF), "# managed by vauban\n# fingerprint: hetzner/nbg1/cx23\n") || !strings.Contains(string(mainTF), deployPub) {
		t.Errorf("main.tf = %s", mainTF)
	}
	ci, err := os.ReadFile(filepath.Join(root, "infra", "cloud-init.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ci), deployPub) || !strings.Contains(string(ci), authorPub) {
		t.Errorf("cloud-init lacks keys:\n%s", ci)
	}
	wantOut := engine.Outputs{
		"deploy_key_path":      keyPath,
		"deploy_pubkey":        deployPub,
		"rendered_fingerprint": "hetzner/nbg1/cx23",
		"tofu_plan":            planText,
		"plan_destructive":     "false",
		"infra_fingerprint":    "hetzner/nbg1/cx23",
		"server_ip":            "203.0.113.7",
		"server_ipv6":          "2001:db8::1",
	}
	if !reflect.DeepEqual(st.Outputs, wantOut) {
		t.Errorf("outputs = %v\nwant %v", st.Outputs, wantOut)
	}
	stateFile, err := os.ReadFile(filepath.Join(root, ".vauban", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stateFile, []byte(token)) || bytes.Contains(stateFile, []byte("PRIVATE KEY")) {
		t.Error("state leaks a secret")
	}
	if got := StaleSteps(root, st, cfg()); got != nil {
		t.Errorf("stale after clean run: %v", got)
	}

	// A second run changes nothing.
	run2 := newRunner(safePlanJSON)
	if _, err := apply(t, root, cfg(), run2, nil); err != nil {
		t.Fatal(err)
	}
	if len(run2.Calls) != 0 {
		t.Errorf("second run ran %v", tofuVerbs(run2.Calls))
	}
}

// (a) drift, plan fails, rerun still plans before apply.
func TestResumeDriftPlanFails(t *testing.T) {
	_, home, root := setup(t)
	writeKey(t, home, "vauban_shop_ed25519", "PRIVATE")
	writeKey(t, home, "vauban_shop_ed25519.pub", deployPub)
	if _, err := apply(t, root, cfg(), newRunner(safePlanJSON), nil); err != nil {
		t.Fatal(err)
	}

	c := cfg()
	c.Size = "cx33"
	st, err := engine.LoadState(root)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := StaleSteps(root, st, c), []string{"infra:main-tf", "infra:plan", "infra:apply", "infra:output"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("StaleSteps = %v, want %v", got, want)
	}

	run := newRunner(safePlanJSON)
	run.Errors[tofuName()+" -chdir=infra plan -input=false -no-color -out=tfplan"] = errors.New("api down")
	if _, err := apply(t, root, c, run, nil); err == nil || !strings.Contains(err.Error(), "infra:plan") {
		t.Fatalf("err = %v, want infra:plan failure", err)
	}
	if got := tofuVerbs(run.Calls); !reflect.DeepEqual(got, []string{"plan"}) {
		t.Fatalf("first drift run ran %v, want only plan", got)
	}
	mainTF, _ := os.ReadFile(filepath.Join(root, "infra", "main.tf"))
	if !strings.Contains(string(mainTF), "# fingerprint: hetzner/nbg1/cx33\n") {
		t.Fatalf("main.tf not re-rendered: %s", mainTF)
	}

	// Resume: plan must run again before apply.
	run = newRunner(safePlanJSON)
	st, err = apply(t, root, c, run, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := tofuVerbs(run.Calls); !reflect.DeepEqual(got, []string{"plan", "show", "apply", "output"}) {
		t.Fatalf("resume ran %v", got)
	}
	if st.Outputs["infra_fingerprint"] != "hetzner/nbg1/cx33" {
		t.Errorf("fingerprint = %q", st.Outputs["infra_fingerprint"])
	}
	if StaleSteps(root, st, c) != nil {
		t.Error("still stale after apply")
	}
}

// Drift where apply fails after a successful plan: resume re-plans.
func TestResumeDriftApplyFails(t *testing.T) {
	_, home, root := setup(t)
	writeKey(t, home, "vauban_shop_ed25519", "PRIVATE")
	writeKey(t, home, "vauban_shop_ed25519.pub", deployPub)
	if _, err := apply(t, root, cfg(), newRunner(safePlanJSON), nil); err != nil {
		t.Fatal(err)
	}
	c := cfg()
	c.Size = "cx33"
	run := newRunner(safePlanJSON)
	run.Errors[tofuName()+" -chdir=infra apply -input=false tfplan"] = errors.New("boom")
	if _, err := apply(t, root, c, run, nil); err == nil {
		t.Fatal("want apply error")
	}
	run = newRunner(safePlanJSON)
	if _, err := apply(t, root, c, run, nil); err != nil {
		t.Fatal(err)
	}
	if got := tofuVerbs(run.Calls); !reflect.DeepEqual(got, []string{"plan", "show", "apply", "output"}) {
		t.Fatalf("resume ran %v", got)
	}
}

// (b) managed mode, empty state: deploy key output, fingerprint from main.tf.
func TestManagedEmptyState(t *testing.T) {
	_, home, root := setup(t)
	writeKey(t, home, "vauban_shop_ed25519", "PRIVATE")
	writeKey(t, home, "vauban_shop_ed25519.pub", deployPub)
	const body = "# managed by vauban\n# fingerprint: hetzner/nbg1/cx23\nterraform {}\n"
	writeMainTF(t, root, body)
	run := newRunner(safePlanJSON)
	st, err := apply(t, root, cfg(), run, nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.Outputs["deploy_pubkey"] != deployPub || st.Outputs["deploy_key_path"] == "" {
		t.Errorf("deploy key outputs = %v", st.Outputs)
	}
	if st.Outputs["infra_fingerprint"] != "hetzner/nbg1/cx23" {
		t.Errorf("infra_fingerprint = %q, want the one in main.tf", st.Outputs["infra_fingerprint"])
	}
	mainTF, _ := os.ReadFile(filepath.Join(root, "infra", "main.tf"))
	if string(mainTF) != body {
		t.Errorf("main.tf rewritten: %s", mainTF)
	}
	for _, f := range []string{"cloud-init.yaml", ".gitignore"} {
		if _, err := os.Stat(filepath.Join(root, "infra", f)); err != nil {
			t.Errorf("%s not written: %v", f, err)
		}
	}
}

// Managed main.tf rendered for another config with no applied fingerprint
// (e.g. kept after vauban destroy): re-rendered for vauban.yaml, even across clouds.
func TestManagedEmptyStateRerendersMismatch(t *testing.T) {
	_, home, root := setup(t)
	writeKey(t, home, "vauban_shop_ed25519", "PRIVATE")
	writeKey(t, home, "vauban_shop_ed25519.pub", deployPub)
	writeMainTF(t, root, "# managed by vauban\n# fingerprint: digitalocean/fra1/s-1vcpu-1gb\nterraform {}\n")
	c := cfg()
	c.Size = "cx33"
	st, err := engine.LoadState(root)
	if err != nil {
		t.Fatal(err)
	}
	if StaleSteps(root, st, c) == nil {
		t.Error("want stale steps: main.tf on disk differs from vauban.yaml")
	}
	if st, err = apply(t, root, c, newRunner(safePlanJSON), nil); err != nil {
		t.Fatal(err)
	}
	mainTF, _ := os.ReadFile(filepath.Join(root, "infra", "main.tf"))
	if !strings.HasPrefix(string(mainTF), "# managed by vauban\n# fingerprint: hetzner/nbg1/cx33\n") {
		t.Errorf("main.tf not re-rendered: %s", mainTF)
	}
	if st.Outputs["infra_fingerprint"] != "hetzner/nbg1/cx33" {
		t.Errorf("infra_fingerprint = %q", st.Outputs["infra_fingerprint"])
	}
	if StaleSteps(root, st, c) != nil {
		t.Error("still stale after apply")
	}
}

func TestApplyRejectsMainTFWithoutFingerprint(t *testing.T) {
	_, home, root := setup(t)
	writeKey(t, home, "vauban_shop_ed25519", "PRIVATE")
	writeKey(t, home, "vauban_shop_ed25519.pub", deployPub)
	writeMainTF(t, root, "# managed by vauban\nterraform {}\n")
	run := newRunner(safePlanJSON)
	st, err := apply(t, root, cfg(), run, nil)
	want := "infra:main-tf: infra/main.tf has no vauban fingerprint line; rerun with --force to regenerate it"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if st.Done["infra:main-tf"] {
		t.Error("infra:main-tf marked done")
	}
	if len(tofuVerbs(run.Calls)) != 0 {
		t.Errorf("tofu ran: %v", tofuVerbs(run.Calls))
	}

	// The advice works: a --force rerun regenerates main.tf and applies it.
	run = newRunner(safePlanJSON)
	st, err = applyWith(t, root, cfg(), run, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	mainTF, _ := os.ReadFile(filepath.Join(root, "infra", "main.tf"))
	if !strings.HasPrefix(string(mainTF), "# managed by vauban\n# fingerprint: hetzner/nbg1/cx23\n") {
		t.Errorf("main.tf not regenerated: %s", mainTF)
	}
	if st.Outputs["rendered_fingerprint"] != "hetzner/nbg1/cx23" || st.Outputs["infra_fingerprint"] != "hetzner/nbg1/cx23" {
		t.Errorf("fingerprints = %q / %q", st.Outputs["rendered_fingerprint"], st.Outputs["infra_fingerprint"])
	}
	if got := tofuVerbs(run.Calls); !reflect.DeepEqual(got, []string{"init", "plan", "show", "apply", "output"}) {
		t.Errorf("ran %v", got)
	}
}

// The confirmation gate fails closed: only plan_destructive == "false" skips it.
func TestApplyGateFailsClosed(t *testing.T) {
	_, _, root := setup(t)
	writeMainTF(t, root, "# managed by vauban\n# fingerprint: hetzner/nbg1/cx23\nterraform {}\n")
	steps, err := New().Plan(project(t, root), cfg())
	if err != nil {
		t.Fatal(err)
	}
	ap := steps[6]
	if ap.ID() != "infra:apply" {
		t.Fatalf("step 6 = %s", ap.ID())
	}
	for _, in := range []engine.Outputs{{}, {"plan_destructive": ""}, {"plan_destructive": "maybe"}, {"plan_destructive": "TRUE"}} {
		run := &engine.FakeRunner{}
		_, err := ap.Apply(context.Background(), &engine.Env{Root: root, Run: run, In: in})
		if !errors.Is(err, engine.ErrNotConfirmed) {
			t.Errorf("In %v: err = %v, want ErrNotConfirmed", in, err)
		}
		if len(run.Calls) != 0 {
			t.Errorf("In %v: ran %v", in, run.Calls)
		}
	}
	run := &engine.FakeRunner{}
	if _, err := ap.Apply(context.Background(), &engine.Env{Root: root, Run: run, In: engine.Outputs{"plan_destructive": "false"}}); err != nil {
		t.Fatal(err)
	}
	if len(run.Calls) != 1 {
		t.Fatalf("calls = %v", run.Calls)
	}
}

// (c) fresh run where cloud-init fails; rerun writes cloud-init and .gitignore.
func TestResumeCloudInitFails(t *testing.T) {
	_, home, root := setup(t)
	writeKey(t, home, "id_ed25519.pub", "ssh-ed25519 AAAA one\nssh-ed25519 BBBB two")
	run := newRunner(safePlanJSON)
	if _, err := apply(t, root, cfg(), run, nil); err == nil || !strings.Contains(err.Error(), "infra:cloud-init") {
		t.Fatalf("err = %v, want infra:cloud-init failure", err)
	}
	if len(tofuVerbs(run.Calls)) != 0 {
		t.Fatalf("tofu ran: %v", tofuVerbs(run.Calls))
	}
	writeKey(t, home, "id_ed25519.pub", authorPub)
	run = newRunner(safePlanJSON)
	if _, err := apply(t, root, cfg(), run, nil); err != nil {
		t.Fatal(err)
	}
	ci, err := os.ReadFile(filepath.Join(root, "infra", "cloud-init.yaml"))
	if err != nil || !strings.Contains(string(ci), authorPub) {
		t.Fatalf("cloud-init.yaml: %v\n%s", err, ci)
	}
	if _, err := os.Stat(filepath.Join(root, "infra", ".gitignore")); err != nil {
		t.Fatal(err)
	}
	if got := tofuVerbs(run.Calls); !reflect.DeepEqual(got, []string{"init", "plan", "show", "apply", "output"}) {
		t.Fatalf("resume ran %v", got)
	}
}

// (d) destructive plan requires confirmation.
func TestDestructivePlanNeedsConfirm(t *testing.T) {
	_, _, root := setup(t)
	run := newRunner(replacePlanJSN)
	st, err := apply(t, root, cfg(), run, nil)
	if !errors.Is(err, engine.ErrNotConfirmed) {
		t.Fatalf("err = %v, want ErrNotConfirmed", err)
	}
	if slices.Contains(tofuVerbs(run.Calls), "apply") {
		t.Fatal("apply ran without confirmation")
	}
	if st.Outputs["plan_destructive"] != "true" {
		t.Errorf("plan_destructive = %q", st.Outputs["plan_destructive"])
	}

	refuse := func(string, string) (bool, error) { return false, nil }
	run = newRunner(replacePlanJSN)
	if _, err := apply(t, root, cfg(), run, refuse); !errors.Is(err, engine.ErrNotConfirmed) {
		t.Fatalf("refused: err = %v", err)
	}
	if slices.Contains(tofuVerbs(run.Calls), "apply") {
		t.Fatal("apply ran after refusal")
	}

	var gotTitle, gotDetails string
	accept := func(title, details string) (bool, error) {
		gotTitle, gotDetails = title, details
		return true, nil
	}
	run = newRunner(replacePlanJSN)
	if _, err := apply(t, root, cfg(), run, accept); err != nil {
		t.Fatal(err)
	}
	if gotTitle != "infra changes will destroy or replace resources" || gotDetails != planText {
		t.Errorf("Confirm(%q, %q)", gotTitle, gotDetails)
	}
	ap := findCall(t, run.Calls, "apply")
	if !reflect.DeepEqual(ap.Args, []string{"-chdir=infra", "apply", "-input=false", "tfplan"}) {
		t.Errorf("apply args = %v", ap.Args)
	}
	if got := tofuVerbs(run.Calls); !reflect.DeepEqual(got, []string{"plan", "show", "apply", "output"}) {
		t.Errorf("ran %v (apply must follow a fresh plan)", got)
	}
}

func TestPlanParseErrors(t *testing.T) {
	_, _, root := setup(t)
	run := newRunner("not json")
	if _, err := apply(t, root, cfg(), run, nil); err == nil || !strings.Contains(err.Error(), "infra:plan") {
		t.Fatalf("err = %v, want infra:plan parse failure", err)
	}
}

func TestPlanTextTruncated(t *testing.T) {
	big := strings.Repeat("x", 70*1024)
	got := truncate(big)
	if len(got) > 64*1024 || !strings.HasSuffix(got, "(truncated)\n") {
		t.Fatalf("len = %d, suffix %q", len(got), got[len(got)-20:])
	}
	// a multibyte rune straddling the cut must not leave invalid UTF-8
	for off := 0; off < 24; off++ {
		s := strings.Repeat("x", 64*1024-off) + strings.Repeat("é", 4096)
		got := truncate(s)
		if !utf8.ValidString(got) || len(got) > 64*1024 || !strings.HasSuffix(got, "(truncated)\n") {
			t.Fatalf("off %d: valid=%v len=%d", off, utf8.ValidString(got), len(got))
		}
	}
	if truncate("short") != "short" {
		t.Fatal("short text changed")
	}
}

// (e) changing cloud is refused.
func TestCloudChangeRefused(t *testing.T) {
	_, _, root := setup(t)
	st := &engine.State{Done: map[string]bool{"infra:apply": true}, Outputs: engine.Outputs{"infra_fingerprint": "digitalocean/fra1/s-1vcpu-1gb"}}
	if err := st.Save(root); err != nil {
		t.Fatal(err)
	}
	_, err := New().Plan(project(t, root), cfg())
	want := "changing cloud (digitalocean → hetzner) is not supported in place: run vauban destroy first, then vauban apply"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

// The on-disk main.tf also counts while servers are applied.
func TestCloudChangeRefusedFromDisk(t *testing.T) {
	_, _, root := setup(t)
	st := &engine.State{Done: map[string]bool{"infra:apply": true}, Outputs: engine.Outputs{"infra_fingerprint": "hetzner/nbg1/cx23"}}
	if err := st.Save(root); err != nil {
		t.Fatal(err)
	}
	writeMainTF(t, root, "# managed by vauban\n# fingerprint: digitalocean/fra1/s-1vcpu-1gb\nterraform {}\n")
	_, err := New().Plan(project(t, root), cfg())
	want := "changing cloud (digitalocean → hetzner) is not supported in place: run vauban destroy first, then vauban apply"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}
