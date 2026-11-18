package infra

import (
	"bytes"
	"context"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/jean-baptisteAZAN/vauban/internal/cloud"
	"github.com/jean-baptisteAZAN/vauban/internal/config"
	"github.com/jean-baptisteAZAN/vauban/internal/detect"
	"github.com/jean-baptisteAZAN/vauban/internal/engine"
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

func TestPlanStepIDs(t *testing.T) {
	_, _, root := setup(t)
	steps, err := New().Plan(project(t, root), cfg())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"infra:deploy-key", "infra:main-tf", "infra:cloud-init", "file:infra/.gitignore", "infra:init", "infra:apply", "infra:output"}
	if got := stepIDs(steps); !reflect.DeepEqual(got, want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
}

func TestPlanDescribe(t *testing.T) {
	_, home, root := setup(t)
	steps, err := New().Plan(project(t, root), cfg())
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(home, ".ssh", "vauban_shop_ed25519")
	tofu := steps[4].(engine.RunCommand).Cmd.Name
	want := []string{
		`$ ssh-keygen -t ed25519 -N "" -C vauban-shop -f ` + keyPath,
		"write infra/main.tf",
		"write infra/cloud-init.yaml",
		"write infra/.gitignore",
		"$ " + tofu + " -chdir=infra init -input=false",
		"$ " + tofu + " -chdir=infra apply -auto-approve -input=false",
		"$ " + tofu + " -chdir=infra output -json",
	}
	for i, s := range steps {
		if !strings.HasSuffix(s.Describe(), want[i]) {
			t.Errorf("step %s Describe = %q, want suffix %q", s.ID(), s.Describe(), want[i])
		}
		if strings.Contains(s.Describe(), token) {
			t.Errorf("step %s leaks token", s.ID())
		}
	}
	if strings.Contains(steps[0].Describe(), "~") {
		t.Error("deploy key path not expanded")
	}
	gi := steps[3].(engine.WriteFile)
	if string(gi.Content) != "*.tfstate*\n.terraform/\n" {
		t.Errorf(".gitignore = %q", gi.Content)
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

func TestPlanManaged(t *testing.T) {
	_, _, root := setup(t)
	if err := os.MkdirAll(filepath.Join(root, "infra"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "infra", "main.tf"), []byte("# managed by vauban\nterraform {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	steps, err := New().Plan(project(t, root), cfg())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := stepIDs(steps), []string{"infra:init", "infra:apply", "infra:output"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
}

func TestOutputParse(t *testing.T) {
	_, _, root := setup(t)
	steps, err := New().Plan(project(t, root), cfg())
	if err != nil {
		t.Fatal(err)
	}
	out := steps[6].(engine.RunCommand)
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

func TestExecuteFresh(t *testing.T) {
	fp, home, root := setup(t)
	writeKey(t, home, "id_ed25519.pub", authorPub)
	steps, err := New().Plan(project(t, root), cfg())
	if err != nil {
		t.Fatal(err)
	}
	run := &keygenRunner{FakeRunner: engine.FakeRunner{Responses: map[string][]byte{}}}
	st, err := engine.LoadState(root)
	if err != nil {
		t.Fatal(err)
	}
	tofu := steps[4].(engine.RunCommand).Cmd.Name
	run.Responses[tofu+" -chdir=infra"] = []byte(outputJSON)
	if err := engine.Execute(context.Background(), steps, st, &engine.Env{Root: root, Run: run}); err != nil {
		t.Fatal(err)
	}

	keyPath := filepath.Join(home, ".ssh", "vauban_shop_ed25519")
	var names [][]string
	for _, c := range run.Calls {
		names = append(names, append([]string{c.Name}, c.Args...))
	}
	want := [][]string{
		{"ssh-keygen", "-t", "ed25519", "-N", "", "-C", "vauban-shop", "-f", keyPath},
		{tofu, "-chdir=infra", "init", "-input=false"},
		{tofu, "-chdir=infra", "apply", "-auto-approve", "-input=false"},
		{tofu, "-chdir=infra", "output", "-json"},
	}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("calls = %q\nwant %q", names, want)
	}
	if !slices.Contains(run.Calls[2].Env, "HCLOUD_TOKEN="+token) {
		t.Errorf("apply env = %v, want HCLOUD_TOKEN", run.Calls[2].Env)
	}
	if len(run.Calls[1].Env) != 0 || len(run.Calls[3].Env) != 0 {
		t.Errorf("credentials passed to init/output")
	}

	// provider receives only the deploy key
	if !reflect.DeepEqual(fp.got.SSHPublicKeys, []string{deployPub}) {
		t.Errorf("SSHPublicKeys = %q", fp.got.SSHPublicKeys)
	}
	if fp.got.Name != "shop" || fp.got.Region != "nbg1" || fp.got.Size != "cx23" {
		t.Errorf("TFInput = %+v", *fp.got)
	}
	mainTF, err := os.ReadFile(filepath.Join(root, "infra", "main.tf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(mainTF), "# managed by vauban\n") || !strings.Contains(string(mainTF), deployPub) {
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
		"deploy_key_path":   keyPath,
		"deploy_pubkey":     deployPub,
		"infra_fingerprint": "hetzner/nbg1/cx23",
		"server_ip":         "203.0.113.7",
		"server_ipv6":       "2001:db8::1",
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
	_, _, root := setup(t)
	steps, err := New().Plan(project(t, root), cfg())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "infra"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"main.tf", "cloud-init.yaml"} {
		if err := os.WriteFile(filepath.Join(root, "infra", name), []byte("keep"), 0o644); err != nil {
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
		if string(b) != "keep" {
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
		if string(b) == "keep" {
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
		"/etc/apt/apt.conf.d/52vauban-unattended-upgrades": {`Unattended-Upgrade::Automatic-Reboot "true";`, `Unattended-Upgrade::Automatic-Reboot-Time "04:00";`},
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

func TestDrift(t *testing.T) {
	_, home, root := setup(t)
	writeKey(t, home, "vauban_shop_ed25519", "PRIVATE")
	writeKey(t, home, "vauban_shop_ed25519.pub", deployPub)
	if err := os.MkdirAll(filepath.Join(root, "infra"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "infra", "main.tf"), []byte("# managed by vauban\nold\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := &engine.State{
		Done: map[string]bool{
			"infra:deploy-key": true, "infra:main-tf": true, "infra:cloud-init": true, "file:infra/.gitignore": true,
			"infra:init": true, "infra:apply": true, "infra:output": true,
		},
		Outputs: engine.Outputs{"infra_fingerprint": "hetzner/nbg1/cx23", "deploy_pubkey": deployPub},
	}
	if err := st.Save(root); err != nil {
		t.Fatal(err)
	}

	if got := StaleSteps(st, cfg()); got != nil {
		t.Fatalf("no drift: StaleSteps = %v", got)
	}
	steps, err := New().Plan(project(t, root), cfg())
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(stepIDs(steps), "infra:plan") {
		t.Fatal("infra:plan without drift")
	}

	c := cfg()
	c.Size = "cx33"
	stale := StaleSteps(st, c)
	if want := []string{"infra:main-tf", "infra:plan", "infra:apply", "infra:output"}; !reflect.DeepEqual(stale, want) {
		t.Fatalf("StaleSteps = %v, want %v", stale, want)
	}
	steps, err = New().Plan(project(t, root), c)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := stepIDs(steps), []string{"infra:main-tf", "infra:init", "infra:plan", "infra:apply", "infra:output"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("drift ids = %v, want %v", got, want)
	}
	tofu := steps[1].(engine.RunCommand).Cmd.Name
	if d := steps[2].Describe(); !strings.HasSuffix(d, "$ "+tofu+" -chdir=infra plan -no-color -input=false") {
		t.Errorf("plan Describe = %q", d)
	}

	for _, id := range stale {
		delete(st.Done, id)
	}
	run := &engine.FakeRunner{Responses: map[string][]byte{tofu + " -chdir=infra": []byte(outputJSON)}}
	if err := engine.Execute(context.Background(), steps, st, &engine.Env{Root: root, Run: run}); err != nil {
		t.Fatal(err)
	}
	var verbs []string
	for _, call := range run.Calls {
		verbs = append(verbs, call.Args[1])
	}
	if want := []string{"plan", "apply", "output"}; !reflect.DeepEqual(verbs, want) {
		t.Fatalf("ran %v, want %v", verbs, want)
	}
	if !slices.Contains(run.Calls[0].Env, "HCLOUD_TOKEN="+token) {
		t.Error("plan lacks credentials")
	}
	mainTF, _ := os.ReadFile(filepath.Join(root, "infra", "main.tf"))
	if !strings.HasPrefix(string(mainTF), "# managed by vauban\n") || !strings.Contains(string(mainTF), "cx33") {
		t.Errorf("main.tf not re-rendered: %s", mainTF)
	}
	if st.Outputs["infra_fingerprint"] != "hetzner/nbg1/cx33" {
		t.Errorf("fingerprint = %q", st.Outputs["infra_fingerprint"])
	}
	if st.Outputs["tofu_plan"] != outputJSON {
		t.Errorf("tofu_plan = %q", st.Outputs["tofu_plan"])
	}
	if StaleSteps(st, c) != nil {
		t.Error("still stale after apply")
	}
}
