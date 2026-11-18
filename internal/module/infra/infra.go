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
	"strings"
	"text/template"

	"github.com/jean-baptisteAZAN/vauban/internal/cloud"
	"github.com/jean-baptisteAZAN/vauban/internal/config"
	"github.com/jean-baptisteAZAN/vauban/internal/detect"
	"github.com/jean-baptisteAZAN/vauban/internal/engine"
	"github.com/jean-baptisteAZAN/vauban/internal/tools"
)

// Marker is the first line of every infra/main.tf vauban generates.
const Marker = "# managed by vauban"

const (
	dir       = "infra"
	mainPath  = dir + "/main.tf"
	ciPath    = dir + "/cloud-init.yaml"
	gitignore = "*.tfstate*\n.terraform/\n"
)

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

// Plan returns the full setup for a repo without Terraform, only the tofu
// steps (plus drift handling) when infra/ is vauban-managed, and nothing when
// the repo has Terraform of its own.
func (*Module) Plan(p *detect.Project, c *config.Config) ([]engine.Step, error) {
	managed := isManaged(p.Root)
	if p.HasTerraform && !managed {
		return nil, nil
	}
	prov, err := cloud.Get(c.Cloud)
	if err != nil {
		return nil, err
	}
	st, err := engine.LoadState(p.Root)
	if err != nil {
		return nil, err
	}
	drift := drifted(st, c)
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("infra: home directory: %w", err)
	}
	name := keyName(c.Project)
	keyPath := filepath.Join(home, ".ssh", "vauban_"+name+"_ed25519")
	in := cloud.TFInput{
		Name:   c.Project,
		Region: orDefault(c.Region, prov.DefaultRegion()),
		Size:   orDefault(c.Size, prov.DefaultSize()),
	}
	tofu, _ := tools.Tofu()
	fp := fingerprint(c)

	mainTF := mainTF{prov: prov, in: in, keyPath: keyPath, rerender: drift}
	initStep := engine.RunCommand{StepID: "infra:init", Cmd: tofuCmd(tofu, "init", "-input=false")}
	planStep := withCreds{prov: prov, RunCommand: engine.RunCommand{
		StepID: "infra:plan",
		Cmd:    tofuCmd(tofu, "plan", "-no-color", "-input=false"),
		Parse:  func(b []byte) (engine.Outputs, error) { return engine.Outputs{"tofu_plan": string(b)}, nil },
	}}
	apply := withCreds{prov: prov, RunCommand: engine.RunCommand{
		StepID: "infra:apply",
		Cmd:    tofuCmd(tofu, "apply", "-auto-approve", "-input=false"),
		Parse:  func([]byte) (engine.Outputs, error) { return engine.Outputs{"infra_fingerprint": fp}, nil },
	}}
	output := engine.RunCommand{StepID: "infra:output", Cmd: tofuCmd(tofu, "output", "-json"), Parse: parseOutput}

	var steps []engine.Step
	switch {
	case !managed:
		steps = []engine.Step{
			deployKey{project: name, path: keyPath},
			mainTF,
			cloudInit{keyPath: keyPath},
			engine.WriteFile{Path: dir + "/.gitignore", Content: []byte(gitignore), Mode: 0o644},
			initStep,
		}
	case drift:
		steps = []engine.Step{mainTF, initStep}
	default:
		steps = []engine.Step{initStep}
	}
	if drift {
		steps = append(steps, planStep)
	}
	return append(steps, apply, output), nil
}

// StaleSteps returns the step IDs the caller must clear from st.Done before
// Execute when vauban.yaml's cloud, region or size no longer match what was
// applied, so main.tf is re-rendered, the change is planned and re-applied.
// It returns nil when there is no drift.
func StaleSteps(st *engine.State, c *config.Config) []string {
	if !drifted(st, c) {
		return nil
	}
	return []string{"infra:main-tf", "infra:plan", "infra:apply", "infra:output"}
}

func drifted(st *engine.State, c *config.Config) bool {
	return st.Done["infra:apply"] && st.Outputs["infra_fingerprint"] != fingerprint(c)
}

// fingerprint is cloud/region/size with provider defaults filled in.
func fingerprint(c *config.Config) string {
	region, size := c.Region, c.Size
	if prov, err := cloud.Get(c.Cloud); err == nil {
		region, size = orDefault(region, prov.DefaultRegion()), orDefault(size, prov.DefaultSize())
	}
	return c.Cloud + "/" + region + "/" + size
}

func isManaged(root string) bool {
	f, err := os.Open(filepath.Join(root, mainPath))
	if err != nil {
		return false
	}
	defer f.Close()
	line, err := bufio.NewReader(f).ReadString('\n')
	if err != nil && line == "" {
		return false
	}
	return strings.TrimRight(line, "\r\n") == Marker
}

func tofuCmd(tofu string, args ...string) engine.Cmd {
	return engine.Cmd{Name: tofu, Args: append([]string{"-chdir=" + dir}, args...)}
}

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

// withCreds runs a tofu command with the provider credentials, read only at
// Apply time. Describe never shows them.
type withCreds struct {
	engine.RunCommand
	prov cloud.Provider
}

func (w withCreds) Apply(ctx context.Context, env *engine.Env) (engine.Outputs, error) {
	creds, err := w.prov.CredentialsEnv()
	if err != nil {
		return nil, err
	}
	rc := w.RunCommand
	rc.Cmd.Env = append(append([]string(nil), rc.Cmd.Env...), creds...)
	return rc.Apply(ctx, env)
}

// deployKey creates the project's passphrase-less deploy key pair unless it
// exists. It outputs the key path and public key, never the private key.
type deployKey struct{ project, path string }

func (d deployKey) ID() string { return "infra:deploy-key" }
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

// mainTF renders infra/main.tf at Apply time, once the deploy key exists. It
// skips an existing file unless Force, or rerender after a config drift.
type mainTF struct {
	prov     cloud.Provider
	in       cloud.TFInput
	keyPath  string
	rerender bool
}

func (m mainTF) ID() string       { return "infra:main-tf" }
func (m mainTF) Describe() string { return "write " + mainPath }

func (m mainTF) Apply(_ context.Context, env *engine.Env) (engine.Outputs, error) {
	if skip, err := skipExisting(env, mainPath, m.rerender); skip || err != nil {
		return nil, err
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
	return nil, write(env.Root, mainPath, append([]byte(Marker+"\n"), tf...))
}

// cloudInit renders infra/cloud-init.yaml at Apply time with the deploy key
// and the author's personal key (if any) for user deploy.
type cloudInit struct{ keyPath string }

func (cloudInit) ID() string       { return "infra:cloud-init" }
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
