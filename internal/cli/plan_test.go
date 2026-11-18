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

	"github.com/jean-baptisteAZAN/vauban/internal/cloud"
	"github.com/jean-baptisteAZAN/vauban/internal/config"
	"github.com/jean-baptisteAZAN/vauban/internal/engine"
	"github.com/jean-baptisteAZAN/vauban/internal/module/infra"
)

const planToken = "hcloud-plan-secret"

// planProvider stands in for hetzner without network access.
type planProvider struct{}

func (*planProvider) ID() string            { return "hetzner" }
func (*planProvider) DefaultRegion() string { return "nbg1" }
func (*planProvider) DefaultSize() string   { return "cx23" }
func (*planProvider) CredentialsEnv() ([]string, error) {
	return []string{"HCLOUD_TOKEN=" + planToken}, nil
}
func (*planProvider) CheckAuth(context.Context) error { return nil }
func (*planProvider) Regions(context.Context) ([]cloud.Option, error) {
	return []cloud.Option{{ID: "nbg1", Label: "Nuremberg"}}, nil
}
func (*planProvider) Sizes(context.Context, string) ([]cloud.Option, error) {
	return []cloud.Option{{ID: "cx23", Label: "cx23"}}, nil
}
func (*planProvider) Terraform(cloud.TFInput) ([]byte, error) { return []byte("# tf\n"), nil }

// planEnv copies a fixture into a temp dir with a GitHub origin, isolates
// HOME and installs the fake provider and runners. It returns the project root.
func planEnv(t *testing.T, fixture string) (string, *planProvider) {
	t.Helper()
	t.Setenv("NO_COLOR", "1")
	t.Setenv("HOME", t.TempDir())
	prov := &planProvider{}
	t.Cleanup(cloud.Register(prov))

	pr := &engine.FakeRunner{}
	oldPre := engine.PreflightRunner
	engine.PreflightRunner = pr
	t.Cleanup(func() { engine.PreflightRunner = oldPre })

	root := filepath.Join(t.TempDir(), "shop")
	src := filepath.Join("..", "..", "testdata", fixture)
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		dst := filepath.Join(root, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, ".git/config", "[remote \"origin\"]\n\turl = git@github.com:acme/shop.git\n")
	return root, prov
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func listFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(root, path)
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(files)
	return files
}

func execPlan(t *testing.T, root string, args ...string) (string, error) {
	t.Helper()
	cmd := NewRoot("dev")
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs(append([]string{"plan", "--dir", root}, args...))
	err := cmd.Execute()
	return out.String(), err
}

func saveState(t *testing.T, root string, st *engine.State) {
	t.Helper()
	if err := st.Save(root); err != nil {
		t.Fatal(err)
	}
}

func TestPlanNonTTYGoApp(t *testing.T) {
	root, _ := planEnv(t, "go-app")
	before := listFiles(t, root)

	out, err := execPlan(t, root, "--cloud", "hetzner")
	if err != nil {
		t.Fatalf("plan: %v\n%s", err, out)
	}

	c, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if c.Cloud != "hetzner" || c.AppPort != 8080 || c.Project != "shop" || c.Region != "nbg1" || c.Size != "cx23" {
		t.Errorf("unexpected config %+v", c)
	}

	for _, want := range []string{
		"container\n",
		"+ write Dockerfile",
		"$ tofu -chdir=infra apply",
		"[secret] gh secret set DEPLOY_HOST --repo acme/shop",
		"Terraform state is local (infra/terraform.tfstate). Back it up.",
		"next: vauban apply",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "$ $") {
		t.Errorf("doubled command prompt in:\n%s", out)
	}
	if strings.Contains(out, "\x1b") || strings.Contains(out, "🔑") {
		t.Errorf("plain output has ANSI or emoji:\n%s", out)
	}
	order := []string{"container\n", "ci\n", "infra\n", "deploy\n"}
	last := -1
	for _, h := range order {
		i := strings.Index(out, h)
		if i <= last {
			t.Errorf("heading %q out of order in:\n%s", strings.TrimSpace(h), out)
		}
		last = i
	}

	after := listFiles(t, root)
	want := slices.Sorted(slices.Values(append(slices.Clone(before), config.FileName)))
	if !slices.Equal(after, want) {
		t.Errorf("files after plan:\n%v\nwant:\n%v", after, want)
	}
}

func TestPlanSkipsExistingUnlessForce(t *testing.T) {
	root, _ := planEnv(t, "go-app")
	writeFile(t, root, "Dockerfile", "FROM scratch\n")

	out, err := execPlan(t, root, "--cloud", "hetzner")
	if err != nil {
		t.Fatalf("plan: %v\n%s", err, out)
	}
	if !strings.Contains(out, "· skip Dockerfile (exists)") {
		t.Errorf("missing skip line in:\n%s", out)
	}

	out, err = execPlan(t, root, "--force")
	if err != nil {
		t.Fatalf("plan --force: %v\n%s", err, out)
	}
	if !strings.Contains(out, "+ write Dockerfile") || strings.Contains(out, "skip Dockerfile") {
		t.Errorf("--force should rewrite Dockerfile:\n%s", out)
	}
}

func TestPlanNoConfigExit2(t *testing.T) {
	root, _ := planEnv(t, "go-app")
	out, err := execPlan(t, root)
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 2 {
		t.Fatalf("want ExitError code 2, got %v\n%s", err, out)
	}
	if err.Error() != "no vauban.yaml: pass --cloud or run interactively" {
		t.Errorf("unexpected message %q", err.Error())
	}
	if _, err := os.Stat(filepath.Join(root, config.FileName)); !errors.Is(err, fs.ErrNotExist) {
		t.Error("vauban.yaml written on error")
	}
}

func TestPlanUsesExistingConfig(t *testing.T) {
	root, _ := planEnv(t, "go-app")
	writeFile(t, root, config.FileName, "project: shop\ncloud: hetzner\nregion: fsn1\nsize: cx33\ndomain: shop.example.com\napp_port: 9000\n")

	out, err := execPlan(t, root)
	if err != nil {
		t.Fatalf("plan: %v\n%s", err, out)
	}
	c, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if c.Region != "fsn1" || c.Size != "cx33" || c.AppPort != 9000 || c.Domain != "shop.example.com" {
		t.Errorf("existing config changed: %+v", c)
	}
	if !strings.Contains(out, "deploy domain: shop.example.com") {
		t.Errorf("missing domain in:\n%s", out)
	}
}

func TestPlanShowsDoneSteps(t *testing.T) {
	root, _ := planEnv(t, "go-app")
	saveState(t, root, &engine.State{Done: map[string]bool{"file:Dockerfile": true}})
	before := listFiles(t, root)

	out, err := execPlan(t, root, "--cloud", "hetzner")
	if err != nil {
		t.Fatalf("plan: %v\n%s", err, out)
	}
	if !strings.Contains(out, "✓ write Dockerfile (done)") {
		t.Errorf("missing done line in:\n%s", out)
	}
	after := listFiles(t, root)
	if want := slices.Sorted(slices.Values(append(before, config.FileName))); !slices.Equal(after, want) {
		t.Errorf("plan changed files: %v", after)
	}
}

func staleState() *engine.State {
	return &engine.State{
		Done: map[string]bool{
			"file:Dockerfile": true, "infra:deploy-key": true, "infra:main-tf": true,
			"infra:cloud-init": true, "infra:init": true, "infra:plan": true,
			"infra:apply": true, "infra:output": true, "deploy:domain": true,
			"secret:DEPLOY_HOST": true, "secret:DEPLOY_SSH_KEY": true, "secret:DEPLOY_DOMAIN": true,
		},
		Outputs: engine.Outputs{
			"infra_fingerprint": "hetzner/nbg1/cx23", "rendered_fingerprint": "hetzner/nbg1/cx23",
			"server_ip": "203.0.113.9", "deployed_ip": "198.51.100.1", "deployed_domain": "198-51-100-1.sslip.io",
		},
	}
}

func TestBuildPlanClearsStaleSteps(t *testing.T) {
	root, _ := planEnv(t, "go-app")
	saveState(t, root, staleState())
	c := &config.Config{Project: "shop", Cloud: "hetzner", Region: "fsn1", Size: "cx23", AppPort: 8080}

	steps, st, err := buildPlan(context.Background(), root, c, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) == 0 {
		t.Fatal("no steps")
	}
	for _, id := range []string{"infra:main-tf", "infra:plan", "infra:apply", "infra:output", "deploy:domain", "secret:DEPLOY_HOST", "secret:DEPLOY_DOMAIN"} {
		if st.Done[id] {
			t.Errorf("stale %s still done", id)
		}
	}
	for _, id := range []string{"file:Dockerfile", "infra:deploy-key", "infra:cloud-init", "infra:init", "secret:DEPLOY_SSH_KEY"} {
		if !st.Done[id] {
			t.Errorf("%s should stay done", id)
		}
	}
	disk, err := engine.LoadState(root)
	if err != nil {
		t.Fatal(err)
	}
	if !disk.Done["infra:main-tf"] {
		t.Error("buildPlan saved state")
	}
}

func TestBuildPlanForceClearsFileSteps(t *testing.T) {
	root, _ := planEnv(t, "go-app")
	saveState(t, root, staleState())
	c := &config.Config{Project: "shop", Cloud: "hetzner", Region: "nbg1", Size: "cx23", AppPort: 8080}

	_, st, err := buildPlan(context.Background(), root, c, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"file:Dockerfile", "infra:main-tf", "infra:cloud-init"} {
		if st.Done[id] {
			t.Errorf("--force: %s still done", id)
		}
	}
	if !st.Done["infra:init"] || !st.Done["infra:deploy-key"] {
		t.Error("--force must keep non-file steps done")
	}
}

func TestPlanDriftShowsFingerprints(t *testing.T) {
	root, _ := planEnv(t, "go-app")
	saveState(t, root, staleState())
	writeFile(t, root, "infra/main.tf", infra.Marker+"\n# fingerprint: hetzner/nbg1/cx23\n")
	writeFile(t, root, config.FileName, "project: shop\ncloud: hetzner\nregion: fsn1\nsize: cx23\napp_port: 8080\n")

	out, err := execPlan(t, root)
	if err != nil {
		t.Fatalf("plan: %v\n%s", err, out)
	}
	want := "\ndrift\n" +
		"  infra config changed: hetzner/nbg1/cx23 → hetzner/fsn1/cx23\n" +
		"  apply will show the exact tofu plan and ask for confirmation before destroying or replacing anything\n"
	if !strings.Contains(out, want) {
		t.Errorf("missing drift section %q in:\n%s", want, out)
	}
	if strings.Contains(out, "\x1b") || strings.Contains(out, planToken) {
		t.Errorf("plain drift output has ANSI or token:\n%s", out)
	}
}

func TestPlanNoDriftSection(t *testing.T) {
	root, _ := planEnv(t, "go-app")
	out, err := execPlan(t, root, "--cloud", "hetzner")
	if err != nil {
		t.Fatalf("plan: %v\n%s", err, out)
	}
	if strings.Contains(out, "\ndrift\n") {
		t.Errorf("drift section without drift:\n%s", out)
	}
}

func TestPlanPreflightWarnings(t *testing.T) {
	root, _ := planEnv(t, "go-app")
	engine.PreflightRunner = &engine.FakeRunner{Errors: map[string]error{"gh auth": errors.New("exit 1")}}

	out, err := execPlan(t, root, "--cloud", "hetzner")
	if err != nil {
		t.Fatalf("plan must not fail on preflight: %v\n%s", err, out)
	}
	i := strings.Index(out, "before apply:")
	if i < 0 || !strings.Contains(out[i:], "gh is not authenticated") {
		t.Errorf("missing preflight warnings in:\n%s", out)
	}
}

func TestPlanRejectsBadPort(t *testing.T) {
	root, _ := planEnv(t, "go-app")
	if out, err := execPlan(t, root, "--cloud", "hetzner", "--port", "70000"); err == nil {
		t.Fatalf("want error for port 70000:\n%s", out)
	}
}

func TestDefaultPort(t *testing.T) {
	root, _ := planEnv(t, "node-app")
	out, err := execPlan(t, root, "--cloud", "hetzner")
	if err != nil {
		t.Fatalf("plan: %v\n%s", err, out)
	}
	c, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if c.AppPort != 3000 {
		t.Errorf("node default port = %d, want 3000", c.AppPort)
	}
}
