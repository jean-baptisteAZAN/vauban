// Package infra turns vauban.yaml and a cloud provider into OpenTofu code and
// a hardened cloud-init under infra/, generates the deploy SSH key and runs
// tofu init/apply/output. It never touches Terraform it did not generate.
package infra

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/template"

	"github.com/jean-baptisteAZAN/vauban/internal/cloud"
	"github.com/jean-baptisteAZAN/vauban/internal/config"
	"github.com/jean-baptisteAZAN/vauban/internal/detect"
	"github.com/jean-baptisteAZAN/vauban/internal/engine"
	"github.com/jean-baptisteAZAN/vauban/internal/tools"
)

// Marker is the first line of every infra/main.tf vauban generates. The
// second line records the fingerprint (cloud/region/size) it was rendered for.
const (
	Marker            = "# managed by vauban"
	fingerprintPrefix = "# fingerprint: "
)

const (
	dir       = "infra"
	mainPath  = dir + "/main.tf"
	ciPath    = dir + "/cloud-init.yaml"
	planFile  = "tfplan"
	gitignore = "*.tfstate*\n.terraform/\n" + planFile + "\n"
	maxPlan   = 64 * 1024 // bytes of plan text kept in outputs
)

// ConfirmTitle is what infra:apply asks before a destructive plan.
const ConfirmTitle = "infra changes will destroy or replace resources"

//go:embed cloud-init.yaml.tmpl
var cloudInitSrc string

var cloudInitTmpl = template.Must(template.New("cloud-init").Parse(cloudInitSrc))

// Module implements engine.Module for the cloud server.
type Module struct{}

// New returns the infra module.
func New() *Module { return &Module{} }

func (*Module) ID() string         { return "infra" }
func (*Module) Title() string      { return "infra" }
func (*Module) Requires() []string { return nil }

func (*Module) Detect(p *detect.Project) engine.Status {
	if p.HasTerraform {
		return engine.StatusPresent
	}
	return engine.StatusMissing
}

// Plan returns the infra steps, or nothing when the repo has Terraform of its
// own. Every step is idempotent, so the list is the same whether infra/ is
// new or vauban-managed; infra:main-tf only re-renders an existing file when
// StaleSteps reports drift (or with --force). Changes always go through a
// saved plan: init, plan (saved to infra/tfplan), apply of that plan.
func (*Module) Plan(p *detect.Project, c *config.Config) ([]engine.Step, error) {
	if p.HasTerraform && !IsManaged(p.Root) {
		return nil, nil
	}
	st, err := engine.LoadState(p.Root)
	if err != nil {
		return nil, err
	}
	if applied := st.Outputs["infra_fingerprint"]; applied != "" {
		if old, _, _ := strings.Cut(applied, "/"); old != c.Cloud {
			return nil, fmt.Errorf("changing cloud (%s → %s) is not supported in place: run vauban destroy first, then vauban apply", old, c.Cloud)
		}
	}
	prov, err := cloud.Get(c.Cloud)
	if err != nil {
		return nil, err
	}
	name := keyName(c.Project)
	keyPath, err := DeployKeyPath(c.Project)
	if err != nil {
		return nil, err
	}
	in := cloud.TFInput{
		Name:   c.Project,
		Region: orDefault(c.Region, prov.DefaultRegion()),
		Size:   orDefault(c.Size, prov.DefaultSize()),
	}
	tofu, _ := tools.Tofu()
	return []engine.Step{
		deployKey{project: name, path: keyPath},
		mainTF{prov: prov, in: in, fp: fingerprint(c), keyPath: keyPath, rerender: StaleSteps(st, c) != nil},
		cloudInit{keyPath: keyPath},
		engine.WriteFile{Path: dir + "/.gitignore", Content: []byte(gitignore), Mode: 0o644},
		engine.RunCommand{StepID: "infra:init", Cmd: tofuCmd(tofu, "init", "-input=false")},
		planStep{tofu: tofu, prov: prov},
		applyStep{tofu: tofu, prov: prov},
		engine.RunCommand{StepID: "infra:output", Cmd: tofuCmd(tofu, "output", "-json"), Parse: parseOutput},
	}, nil
}

// StaleSteps returns the step IDs the caller must clear from st.Done before
// Execute, or nil. They are stale when the applied fingerprint differs from
// vauban.yaml (drift), or when the rendered main.tf was never applied (an
// interrupted run), so a resume always re-renders and re-plans before apply.
func StaleSteps(st *engine.State, c *config.Config) []string {
	applied, rendered := st.Outputs["infra_fingerprint"], st.Outputs["rendered_fingerprint"]
	drift := applied != "" && applied != fingerprint(c)
	unapplied := rendered != "" && rendered != applied
	if !drift && !unapplied {
		return nil
	}
	return []string{"infra:main-tf", "infra:plan", "infra:apply", "infra:output"}
}

// fingerprint is cloud/region/size with provider defaults filled in.
func fingerprint(c *config.Config) string {
	region, size := c.Region, c.Size
	if prov, err := cloud.Get(c.Cloud); err == nil {
		region, size = orDefault(region, prov.DefaultRegion()), orDefault(size, prov.DefaultSize())
	}
	return c.Cloud + "/" + region + "/" + size
}

// headLines returns up to the first two lines of infra/main.tf.
func headLines(root string) ([]string, error) {
	f, err := os.Open(filepath.Join(root, mainPath))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	for len(lines) < 2 && sc.Scan() {
		lines = append(lines, strings.TrimRight(sc.Text(), "\r"))
	}
	return lines, sc.Err()
}

// IsManaged reports whether root/infra/main.tf exists and was generated by vauban.
func IsManaged(root string) bool {
	lines, err := headLines(root)
	return err == nil && len(lines) > 0 && lines[0] == Marker
}

var errNoFingerprint = errors.New(mainPath + " has no vauban fingerprint line; rerun with --force to regenerate it")

// readFingerprint returns the fingerprint recorded in infra/main.tf.
func readFingerprint(root string) (string, error) {
	lines, err := headLines(root)
	if err != nil {
		return "", err
	}
	if len(lines) == 2 && lines[0] == Marker {
		if fp, ok := strings.CutPrefix(lines[1], fingerprintPrefix); ok && fp != "" {
			return fp, nil
		}
	}
	return "", errNoFingerprint
}

func tofuCmd(tofu string, args ...string) engine.Cmd {
	return engine.Cmd{Name: tofu, Args: append([]string{"-chdir=" + dir}, args...)}
}

func cmdLine(c engine.Cmd) string { return strings.Join(append([]string{c.Name}, c.Args...), " ") }

func parseOutput(b []byte) (engine.Outputs, error) {
	var raw map[string]struct {
		Value any `json:"value"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("parse tofu output: %w", err)
	}
	ip, _ := raw["server_ip"].Value.(string)
	if ip == "" {
		return nil, errors.New("tofu output: server_ip is missing")
	}
	out := engine.Outputs{"server_ip": ip}
	if ip6, _ := raw["server_ipv6"].Value.(string); ip6 != "" {
		out["server_ipv6"] = ip6
	}
	return out, nil
}

// runWithCreds runs cmd from env.Root with the provider credentials, read
// only now so plan output never needs (or shows) them.
func runWithCreds(ctx context.Context, env *engine.Env, prov cloud.Provider, cmd engine.Cmd) ([]byte, error) {
	creds, err := prov.CredentialsEnv()
	if err != nil {
		return nil, err
	}
	cmd.Dir = env.Root
	cmd.Env = creds
	return env.Run.Run(ctx, cmd)
}

// planStep saves a plan to infra/tfplan and reports whether it destroys or
// replaces anything. Outputs tofu_plan (human text, capped) and
// plan_destructive ("true"/"false").
type planStep struct {
	tofu string
	prov cloud.Provider
}

func (planStep) ID() string   { return "infra:plan" }
func (planStep) Kind() string { return engine.KindCommand }
func (p planStep) cmds() (plan, show engine.Cmd) {
	return tofuCmd(p.tofu, "plan", "-input=false", "-no-color", "-out="+planFile),
		tofuCmd(p.tofu, "show", "-json", planFile)
}
func (p planStep) Describe() string {
	plan, show := p.cmds()
	return "$ " + cmdLine(plan) + " && " + cmdLine(show)
}

func (p planStep) Apply(ctx context.Context, env *engine.Env) (engine.Outputs, error) {
	plan, show := p.cmds()
	text, err := runWithCreds(ctx, env, p.prov, plan)
	if err != nil {
		return nil, err
	}
	js, err := runWithCreds(ctx, env, p.prov, show)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		ResourceChanges []struct {
			Change struct {
				Actions []string `json:"actions"`
			} `json:"change"`
		} `json:"resource_changes"`
	}
	if err := json.Unmarshal(js, &parsed); err != nil {
		return nil, fmt.Errorf("parse tofu show: %w", err)
	}
	destructive := false
	for _, rc := range parsed.ResourceChanges {
		if slices.Contains(rc.Change.Actions, "delete") {
			destructive = true
		}
	}
	return engine.Outputs{"tofu_plan": truncate(string(text)), "plan_destructive": strconv.FormatBool(destructive)}, nil
}

// truncate caps plan text at maxPlan bytes.
func truncate(s string) string {
	if len(s) <= maxPlan {
		return s
	}
	const suffix = "\n… (truncated)\n"
	return strings.ToValidUTF8(s[:maxPlan-len(suffix)], "") + suffix
}

// applyStep applies exactly the saved plan. A destructive (or unknown) plan
// needs env.Confirm; without approval nothing runs and ErrNotConfirmed is returned.
// It records the fingerprint of the main.tf on disk as infra_fingerprint.
type applyStep struct {
	tofu string
	prov cloud.Provider
}

func (applyStep) ID() string         { return "infra:apply" }
func (applyStep) Kind() string       { return engine.KindCommand }
func (a applyStep) cmd() engine.Cmd  { return tofuCmd(a.tofu, "apply", "-input=false", planFile) }
func (a applyStep) Describe() string { return "$ " + cmdLine(a.cmd()) }

func (a applyStep) Apply(ctx context.Context, env *engine.Env) (engine.Outputs, error) {
	fp, err := readFingerprint(env.Root)
	if err != nil {
		return nil, err
	}
	// Fail closed: anything but an explicit "false" needs approval.
	if env.In["plan_destructive"] != "false" {
		if env.Confirm == nil {
			return nil, engine.ErrNotConfirmed
		}
		ok, err := env.Confirm(ConfirmTitle, env.In["tofu_plan"])
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, engine.ErrNotConfirmed
		}
	}
	if _, err := runWithCreds(ctx, env, a.prov, a.cmd()); err != nil {
		return nil, err
	}
	return engine.Outputs{"infra_fingerprint": fp}, nil
}

// deployKey creates the project's passphrase-less deploy key pair unless it
// exists. It outputs the key path and public key, never the private key.
type deployKey struct{ project, path string }

func (d deployKey) ID() string { return "infra:deploy-key" }
func (deployKey) Kind() string { return engine.KindCommand }
func (d deployKey) Describe() string {
	return `deploy key: $ ssh-keygen -t ed25519 -N "" -C vauban-` + d.project + " -f " + d.path
}

func (d deployKey) Apply(ctx context.Context, env *engine.Env) (engine.Outputs, error) {
	if _, err := os.Stat(d.path); errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(d.path), 0o700); err != nil {
			return nil, err
		}
		if _, err := env.Run.Run(ctx, engine.Cmd{
			Name: "ssh-keygen",
			Args: []string{"-t", "ed25519", "-N", "", "-C", "vauban-" + d.project, "-f", d.path},
		}); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	pub, err := readPub(d.path + ".pub")
	if err != nil {
		return nil, err
	}
	return engine.Outputs{"deploy_key_path": d.path, "deploy_pubkey": pub}, nil
}

// deployPubkey returns the deploy public key from earlier outputs, or from disk.
func deployPubkey(env *engine.Env, keyPath string) (string, error) {
	if pub := strings.TrimSpace(env.In["deploy_pubkey"]); pub != "" {
		return pub, nil
	}
	return readPub(keyPath + ".pub")
}

func readPub(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read deploy public key: %w", err)
	}
	pub := strings.TrimSpace(string(b))
	if pub == "" {
		return "", fmt.Errorf("deploy public key %s is empty", path)
	}
	return pub, nil
}

// mainTF renders infra/main.tf at Apply time, once the deploy key exists:
// marker, fingerprint line, provider Terraform. It keeps an existing file
// unless Force or rerender (drift). Outputs rendered_fingerprint, the
// fingerprint of the main.tf on disk.
type mainTF struct {
	prov     cloud.Provider
	in       cloud.TFInput
	fp       string
	keyPath  string
	rerender bool
}

func (m mainTF) ID() string       { return "infra:main-tf" }
func (mainTF) Kind() string       { return engine.KindWrite }
func (m mainTF) Describe() string { return "write " + mainPath }

func (m mainTF) Apply(_ context.Context, env *engine.Env) (engine.Outputs, error) {
	skip, err := skipExisting(env, mainPath, m.rerender)
	if err != nil {
		return nil, err
	}
	if skip {
		// Fail (not Done) so a --force rerun regenerates the file.
		fp, err := readFingerprint(env.Root)
		if err != nil {
			return nil, err
		}
		return engine.Outputs{"rendered_fingerprint": fp}, nil
	}
	pub, err := deployPubkey(env, m.keyPath)
	if err != nil {
		return nil, err
	}
	in := m.in
	in.SSHPublicKeys = []string{pub}
	tf, err := m.prov.Terraform(in)
	if err != nil {
		return nil, err
	}
	content := append([]byte(Marker+"\n"+fingerprintPrefix+m.fp+"\n"), tf...)
	if err := write(env.Root, mainPath, content); err != nil {
		return nil, err
	}
	return engine.Outputs{"rendered_fingerprint": m.fp}, nil
}

// cloudInit renders infra/cloud-init.yaml at Apply time with the deploy key
// and the author's personal key (if any) for user deploy.
type cloudInit struct{ keyPath string }

func (cloudInit) ID() string       { return "infra:cloud-init" }
func (cloudInit) Kind() string     { return engine.KindWrite }
func (cloudInit) Describe() string { return "write " + ciPath }

func (c cloudInit) Apply(_ context.Context, env *engine.Env) (engine.Outputs, error) {
	if skip, err := skipExisting(env, ciPath, false); skip || err != nil {
		return nil, err
	}
	pub, err := deployPubkey(env, c.keyPath)
	if err != nil {
		return nil, err
	}
	keys := []string{pub}
	if a := authorKey(); a != "" && a != pub {
		keys = append(keys, a)
	}
	b, err := renderCloudInit(keys)
	if err != nil {
		return nil, err
	}
	return nil, write(env.Root, ciPath, b)
}

// authorKey returns the author's personal public key: ~/.ssh/id_ed25519.pub,
// else ~/.ssh/id_rsa.pub, else "".
func authorKey() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	for _, n := range []string{"id_ed25519.pub", "id_rsa.pub"} {
		if b, err := os.ReadFile(filepath.Join(home, ".ssh", n)); err == nil {
			if k := strings.TrimSpace(string(b)); k != "" {
				return k
			}
		}
	}
	return ""
}

func renderCloudInit(keys []string) ([]byte, error) {
	quoted := make([]string, len(keys))
	for i, k := range keys {
		if strings.ContainsAny(k, "\r\n") {
			return nil, fmt.Errorf("ssh public key %d spans several lines", i+1)
		}
		b, err := json.Marshal(k) // a JSON string is a valid YAML double-quoted scalar
		if err != nil {
			return nil, err
		}
		quoted[i] = string(b)
	}
	var buf bytes.Buffer
	if err := cloudInitTmpl.Execute(&buf, struct{ Keys []string }{quoted}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// skipExisting reports whether rel exists and must be kept (no Force, no override).
func skipExisting(env *engine.Env, rel string, override bool) (bool, error) {
	_, err := os.Stat(filepath.Join(env.Root, rel))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return false, err
	case env.Force || override:
		return false, nil
	}
	if env.Log != nil {
		fmt.Fprintf(env.Log, "skip (exists) %s\n", rel)
	}
	return true, nil
}

func write(root, rel string, content []byte) error {
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return err
	}
	return os.Chmod(path, 0o644)
}

// DeployKeyPath is the project's deploy private key: ~/.ssh/vauban_<project>_ed25519.
func DeployKeyPath(project string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("infra: home directory: %w", err)
	}
	return filepath.Join(home, ".ssh", "vauban_"+keyName(project)+"_ed25519"), nil
}

var keyNameCleaner = regexp.MustCompile(`[^a-z0-9-]+`)

// keyName makes the project name safe for a file name.
func keyName(project string) string {
	n := strings.Trim(keyNameCleaner.ReplaceAllString(strings.ToLower(project), "-"), "-")
	if n == "" {
		return "vauban"
	}
	return n
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
