# vauban MVP Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A Go CLI that detects a repo, reports what is missing to ship it, and sets up CI, container, OpenTofu infra (Scaleway / DigitalOcean / Hetzner VM + Caddy) and the GitHub wiring in one `apply`.

**Architecture:** Pure detection (`internal/detect`) produces a `Project`; modules (`internal/module/*`) turn `Project` + `Config` into `Step`s (write file, run command, set secret); an engine orders modules, renders the plan, executes steps with resumable state. External work is delegated to `tofu`, `gh`, `ssh-keygen`; cloud SDKs only validate credentials and list regions/sizes.

**Tech Stack:** Go 1.27; cobra v1.10.2 + charm.land/fang/v2 v2.0.1; charm.land/huh/v2 v2.0.3; charm.land/lipgloss/v2 v2.1.1; charm.land/bubbletea/v2 v2.1.2; scaleway-sdk-go v1.39.0; godo v1.243.0; hcloud-go/v2 v2.53.0; gopkg.in/yaml.v3; OpenTofu providers scaleway/scaleway ~> 2.92, digitalocean/digitalocean ~> 2.108, hetznercloud/hcloud ~> 1.70.

**Spec:** `docs/superpowers/specs/2026-11-18-vauban-design.md`

## Global Constraints

- Module path `github.com/<author>/vauban` (builder: read from `gh api user -q .login`); binary name `vauban`.
- Latest library versions at build time (`go get <mod>@latest`), go directive `go 1.27`.
- Never overwrite an existing file unless `--force`; skipped files are reported as `skip (exists)`.
- No secret value is ever printed, logged, or written to the repo or `.vauban/state.json`.
- Every file written and every command run must appear in `plan` output before `apply` does it.
- Non-TTY or `NO_COLOR` set → plain uncoloured output, no interactive prompts; `apply` without TTY requires `--yes`.
- VM: Debian stable, user `deploy`, `PermitRootLogin no`, `PasswordAuthentication no`, ports 22/80/443 only (ufw + cloud firewall).
- Soft targets: `vauban doctor` < 50 ms, binary < 20 MB (measured in Task 15).
- macOS + Linux only.

## Review Focus

1. Repo is not a git repo or has no GitHub remote → `doctor` says so; `plan` fails before any change with "deploy needs a GitHub remote (git remote add origin …)". Test in Task 2 + Task 12.
2. `apply` interrupted (Ctrl-C or failure during `tofu apply`) → rerun skips completed steps and resumes. Test in Task 4.
3. Non-TTY use (pipe, CI) → no prompt hangs; `apply` without `--yes` exits 2 with a message. Test in Task 13.
4. Unknown stack (no go.mod / package.json / pyproject.toml / requirements.txt) → `doctor` prints "stack: unknown"; `plan` errors "unsupported stack" unless a Dockerfile already exists (then container/ci build use it). Test in Task 2 + Task 6.
5. Missing tool (`tofu`/`terraform`, `gh`, `ssh-keygen`) or missing cloud credentials → preflight fails before the first step runs, naming what is missing. Test in Task 12.

---

### Task 1: Scaffold, root command, UI theme

**Files:**
- Create: `go.mod`, `main.go`, `internal/cli/root.go`, `internal/cli/version.go`, `internal/ui/theme.go`, `internal/ui/theme_test.go`, `.gitignore`, `Makefile`
- Test: `internal/cli/root_test.go`

**Interfaces:**
- Produces: `cli.NewRoot(version string) *cobra.Command` (subcommands registered in later tasks via `root.AddCommand`); `cli.Execute(ctx, version)` uses `fang.Execute`; `ui.Styled() bool` (false when `NO_COLOR` set or stdout not a TTY); `ui.Title, ui.OK, ui.Warn, ui.Err, ui.Muted lipgloss.Style`; `ui.Mark(status string) string` returning `✓` / `✗` / `◐` (plain `[ok]`/`[missing]`/`[partial]` when `!Styled()`).
- Global flags on root: `--dir string` (default "."), `--yes bool`, `--force bool`.

- [ ] **Step 1:** `go mod init`, `go get` cobra, fang, lipgloss latest. `.gitignore`: `/dist`, `/vauban`, `.vauban/`.
- [ ] **Step 2: Failing test** `TestVersionCommand`: execute root with args `version`, assert output contains `vauban` and the injected version `"1.2.3"`. `TestMarkPlain`: with `NO_COLOR=1`, `ui.Mark("missing") == "[missing]"`.
- [ ] **Step 3:** Run `go test ./...` → FAIL (undefined).
- [ ] **Step 4:** Implement. `main.go` sets `version` via `-ldflags "-X main.version=..."`, default `"dev"`. Lipgloss palette: builder's call (stone/amber feel to echo fortifications).
- [ ] **Step 5:** `go test ./...` → PASS; `go build -o vauban . && ./vauban --help` shows styled help.
- [ ] **Step 6:** Commit `feat: scaffold vauban CLI with fang root and version`.

### Task 2: Detection

**Files:**
- Create: `internal/detect/project.go`, `internal/detect/stack.go`, `internal/detect/git.go`
- Create fixtures: `testdata/go-app/` (go.mod go 1.27, main.go HTTP on :8080, main_test.go), `testdata/node-app/` (package.json with `lint`/`test`/`start` scripts, pnpm-lock.yaml, index.js), `testdata/python-app/` (pyproject.toml, uv.lock, app.py, tests/test_app.py), `testdata/empty/` (README only), `testdata/dockerized/` (Dockerfile with `EXPOSE 4000`, .github/workflows/ci.yml, infra/main.tf)
- Test: `internal/detect/project_test.go`

**Interfaces:**
- Produces:
```go
type Stack string // "go" | "node" | "python" | "" (unknown)
type Project struct {
    Root, Name     string   // Name = base dir name, lowercased, [a-z0-9-]
    Stack          Stack
    PackageManager string   // go | npm | pnpm | yarn | bun | uv | poetry | pip
    LangVersion    string   // from go.mod go directive, package.json engines.node / .nvmrc, pyproject requires-python / .python-version; "" if unknown
    Scripts        map[string]string // package.json scripts (node only)
    HasDockerfile  bool
    ExposedPort    int      // first EXPOSE in Dockerfile, 0 if none
    Workflows      []string // file names in .github/workflows
    HasTerraform   bool     // any *.tf in root or infra/
    HasLintConfig  bool     // .golangci.yml/.yaml, ruff in pyproject or ruff.toml, eslint/biome config
    HasTests       bool     // *_test.go, tests/ or test_*.py, package.json "test" script
    IsGitRepo      bool
    GitHubRepo     string   // "owner/repo" parsed from origin (https or ssh form), "" otherwise
}
func Scan(root string) (*Project, error)
```

- [ ] **Step 1: Failing tests** (table-driven over fixtures): go-app → `Stack:"go", PackageManager:"go", LangVersion:"1.27", HasTests:true`; node-app → `"node","pnpm"`, `Scripts["lint"]` non-empty; python-app → `"python","uv"`, HasTests; empty → `Stack:""`; dockerized → `HasDockerfile, ExposedPort:4000, Workflows:["ci.yml"], HasTerraform:true`. `TestParseGitHubRemote`: `git@github.com:a/b.git`, `https://github.com/a/b`, `https://github.com/a/b.git` → `"a/b"`; `https://gitlab.com/a/b` → `""`.
- [ ] **Step 2:** `go test ./internal/detect/` → FAIL.
- [ ] **Step 3:** Implement with `os`/`filepath` only; git remote read by parsing `.git/config` (no `git` exec, keeps doctor fast).
- [ ] **Step 4:** Tests PASS.
- [ ] **Step 5:** Commit `feat(detect): scan stack, tooling and git remote`.

### Task 3: Config (`vauban.yaml`)

**Files:**
- Create: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces:
```go
type Config struct {
    Project string `yaml:"project"`
    Cloud   string `yaml:"cloud"`   // scaleway | digitalocean | hetzner
    Region  string `yaml:"region"`
    Size    string `yaml:"size"`
    Domain  string `yaml:"domain"`  // "" → sslip.io derived after apply
    AppPort int    `yaml:"app_port"`
}
const FileName = "vauban.yaml"
func Load(root string) (*Config, error) // returns (nil, fs.ErrNotExist) wrapped when absent
func (c *Config) Save(root string) error
func (c *Config) Validate() error
```

- [ ] **Step 1: Failing tests:** round-trip Save→Load equal; Load on dir without file → `errors.Is(err, fs.ErrNotExist)`; Validate rejects `Cloud:"aws"` ("unsupported cloud \"aws\""), `AppPort:0`, `AppPort:70000`, empty Project.
- [ ] **Step 2:** FAIL. **Step 3:** implement with yaml.v3. **Step 4:** PASS.
- [ ] **Step 5:** Commit `feat(config): vauban.yaml load/save/validate`.

### Task 4: Engine — steps, runner, state, execution

**Files:**
- Create: `internal/engine/engine.go`, `internal/engine/steps.go`, `internal/engine/runner.go`, `internal/engine/state.go`
- Test: `internal/engine/engine_test.go`, `internal/engine/steps_test.go`

**Interfaces:**
- Consumes: `detect.Project`, `config.Config`.
- Produces:
```go
type Outputs map[string]string
type Status int // StatusPresent, StatusMissing, StatusPartial
type Module interface {
    ID() string
    Title() string                       // human label for doctor
    Detect(p *detect.Project) Status
    Requires() []string                  // module IDs that must run first
    Plan(p *detect.Project, c *config.Config) ([]Step, error)
}
type Step interface {
    ID() string                          // stable, unique: "<module>:<thing>"
    Describe() string                    // one line for plan output
    Apply(ctx context.Context, env *Env) (Outputs, error)
}
type Env struct {
    Root   string
    Force  bool
    Run    Runner
    In     Outputs  // accumulated non-secret outputs of previous steps
    Log    io.Writer
}
type Runner interface {
    Run(ctx context.Context, cmd Cmd) ([]byte, error) // returns stdout
}
type Cmd struct { Dir, Name string; Args, Env []string; Stdin io.Reader }
type ExecRunner struct{}                 // os/exec implementation
type FakeRunner struct { Calls []Cmd; Responses map[string][]byte } // key: Name+" "+first arg

// built-in step kinds
type WriteFile struct { Path string; Content []byte; Mode fs.FileMode } // ID "file:<Path>"; skips if exists && !Force
type RunCommand struct { StepID, Label string; Cmd Cmd; Parse func([]byte) (Outputs, error) }
type SetSecret struct { Repo, Name, FromOutput, FromFile string } // value from Env.In[FromOutput] or file contents; runs `gh secret set NAME --repo R` with value on stdin

type State struct { Done map[string]bool `json:"done"`; Outputs Outputs `json:"outputs"` }
func LoadState(root string) (*State, error) // .vauban/state.json, empty State if absent
func (s *State) Save(root string) error     // creates .vauban/ (0700), file 0600

func Order(mods []Module) ([]Module, error)  // topological; error on cycle or unknown requirement
func Execute(ctx context.Context, steps []Step, st *State, env *Env) error
```
- `Execute`: for each step, skip if `st.Done[id]`; else Apply, merge outputs into `st.Outputs` and `env.In`, mark done, `st.Save` **after every step**; on error return wrapped `fmt.Errorf("%s: %w", id, err)` with state already saved. `WriteFile` that skipped an existing file is still marked done.
- Outputs marked secret never enter `Outputs`; secrets only travel via files (`FromFile`) — enforced by convention, see Task 11.

- [ ] **Step 1: Failing tests:**
  - `TestOrder`: modules a←b←c given as [c,a,b] → [a,b,c]; cycle → error containing "cycle".
  - `TestExecuteResume`: 3 fake steps, second fails first run → state.Done has only step1, file `.vauban/state.json` exists; second run with fixed step → step1 not re-applied (call counter == 1), all done.
  - `TestWriteFileSkipsExisting`: existing file untouched without Force, overwritten with Force; new file created with given Mode.
  - `TestSetSecretUsesStdin`: FakeRunner records `gh secret set DEPLOY_HOST --repo a/b`, Stdin content == "1.2.3.4" from `In["server_ip"]`; value absent from `Describe()`.
  - `TestStateFileMode`: saved file mode 0600.
- [ ] **Step 2:** FAIL. **Step 3:** implement. **Step 4:** PASS.
- [ ] **Step 5:** Commit `feat(engine): steps, runner, resumable execution`.

### Task 5: `doctor` command

**Files:**
- Create: `internal/cli/doctor.go`, `internal/ui/report.go`, `internal/tools/tools.go`
- Test: `internal/cli/doctor_test.go`, `internal/tools/tools_test.go`

**Interfaces:**
- Consumes: `detect.Scan`, `engine.Module` list from `module.All()` (Task 6 onward registers modules; until then `All()` returns nil — create `internal/module/registry.go` with `func All() []engine.Module`).
- Produces: `tools.Check() []tools.Tool` with `type Tool struct { Name string; Found bool; Path, Hint string }` for `tofu` (alt `terraform`), `gh`, `docker`, `ssh-keygen`; `tools.Tofu() (string, bool)` returns `tofu` or `terraform`. Uses `exec.LookPath` only (no subprocess).
- Root command with no subcommand runs doctor.

Report layout (plain mode shown; styled mode = same with lipgloss):
```
vauban · <name>
stack     go (go 1.27)
github    owner/repo
modules   [ok] container  [missing] ci  [missing] infra  [missing] deploy
tools     [ok] gh  [ok] tofu  [missing] docker → https://docs.docker.com/get-docker/
next      vauban plan
```

- [ ] **Step 1: Failing tests:** doctor on `testdata/empty` with NO_COLOR contains `stack     unknown` and `github    none`; doctor on `testdata/dockerized` contains `[ok] container` (after Task 6 exists, keep assertion in Task 6). `TestToolsTofuFallback`: PATH with only a fake `terraform` executable → `Tofu()` returns `"terraform", true`.
- [ ] **Step 2:** FAIL. **Step 3:** implement. **Step 4:** PASS.
- [ ] **Step 5:** `go build && time ./vauban --dir testdata/go-app` → record wall time (< 50 ms target).
- [ ] **Step 6:** Commit `feat: doctor command`.

### Task 6: `container` module

**Files:**
- Create: `internal/module/container/container.go`, `internal/module/container/templates/{go,node,python}.Dockerfile`, `.../dockerignore`
- Test: `internal/module/container/container_test.go`, golden files `testdata/golden/container/*.Dockerfile`

**Interfaces:**
- Consumes: engine types.
- Produces: module ID `"container"`, Requires none. Plan → `WriteFile{"Dockerfile"}`, `WriteFile{".dockerignore"}`. Status Present if `HasDockerfile`.

- [ ] **Step 1: Spike (time-box 15 min):** check whether `docker init` accepts non-interactive input (flags or stdin). Record finding in a comment at the top of `container.go`. Expected: not usable → embedded templates (proceed with them). If it is usable, still ship templates as fallback and add a `RunCommand` path only when `docker` is present.
- [ ] **Step 2: Failing golden tests:** for each stack fixture, rendered Dockerfile equals golden. Requirements pinned: multi-stage; final stage non-root; Go → `gcr.io/distroless/static-debian12:nonroot`, `CGO_ENABLED=0`; Node → `node:<LangVersion or 22>-slim`, install with detected PM (`corepack enable` for pnpm/yarn), `CMD` = `start` script; Python → `python:<LangVersion or 3.13>-slim`, uv via `ghcr.io/astral-sh/uv` copy when PM=uv, else pip; every Dockerfile has `EXPOSE <AppPort>`. Unknown stack + no Dockerfile → `Plan` returns error `unsupported stack: add a Dockerfile and rerun`.
- [ ] **Step 3:** FAIL. **Step 4:** implement with `embed` + `text/template`. `-update` flag regenerates goldens. **Step 5:** PASS.
- [ ] **Step 6:** Register in `module.All()`. Commit `feat(module): container`.

### Task 7: `ci` module

**Files:**
- Create: `internal/module/ci/ci.go`, `internal/module/ci/templates/{go,node,python}.yml`, `.../golangci.yml`, `.../ruff.toml`
- Test: `internal/module/ci/ci_test.go`, goldens `testdata/golden/ci/*`

**Interfaces:**
- Produces: module ID `"ci"`, Requires `["container"]`. Plan → `WriteFile{".github/workflows/ci.yml"}` + default lint config when `!HasLintConfig` (Go → `.golangci.yml`, Python → `ruff.toml`; Node → no file, emits warning line in Describe: `node: no lint config, lint step skipped`). Status Present if any workflow file name contains `ci`.
- Workflow `ci.yml` (name: `ci`, on push + pull_request): jobs `lint`, `test` (only if `HasTests`), `build` (`docker build .`, no push). Uses latest major tags of `actions/checkout`, `actions/setup-go` / `setup-node` / `astral-sh/setup-uv` / `setup-python`, `golangci/golangci-lint-action` (builder: check current major at build time).

- [ ] **Step 1: Failing golden tests** per stack fixture; node fixture lint uses `pnpm run lint`; fixture without tests → no `test` job.
- [ ] **Step 2:** FAIL. **Step 3:** implement. **Step 4:** PASS. Also validate generated YAML parses (yaml.v3 unmarshal) in test.
- [ ] **Step 5:** Register; commit `feat(module): ci workflow and default lint config`.

### Task 8: Cloud providers — interface + Hetzner

**Files:**
- Create: `internal/cloud/cloud.go`, `internal/cloud/hetzner.go`, `internal/cloud/tf/hetzner.tf.tmpl`
- Test: `internal/cloud/hetzner_test.go`, golden `testdata/golden/infra/hetzner.tf`

**Interfaces:**
- Produces:
```go
type Option struct{ ID, Label string }
type TFInput struct {
    Name, Region, Size string
    SSHPublicKeys []string // deploy key + author key
}
type Provider interface {
    ID() string                                   // "hetzner"
    DefaultRegion() string
    DefaultSize() string
    CredentialsEnv() ([]string, error)           // env vars for tofu (e.g. HCLOUD_TOKEN=…), read from env or the cloud CLI config file; error names what is missing
    CheckAuth(ctx context.Context) error          // cheap authenticated API call
    Regions(ctx context.Context) ([]Option, error)
    Sizes(ctx context.Context, region string) ([]Option, error)
    Terraform(in TFInput) ([]byte, error)         // provider block + ssh key(s) + server + firewall; outputs server_ip, server_ipv6
}
func Get(id string) (Provider, error)
func IDs() []string // sorted
```
- Hetzner: token from `HCLOUD_TOKEN` or active context in `~/.config/hcloud/cli.toml`. Defaults: location `nbg1`, smallest current shared x86 type (builder verifies via `Sizes`; spec suggests CX22-class). Server image `debian-13` (or current Debian stable name), `user_data = file("${path.module}/cloud-init.yaml")`, `hcloud_firewall` rules 22/80/443 tcp in, attached to server.
- Every generated tf includes `terraform { required_providers { … version = "~> <major.minor>" } }` with versions from the Tech Stack line.

- [ ] **Step 1: Failing tests:** golden `Terraform()` output; `CredentialsEnv` with `HCLOUD_TOKEN` unset and fake HOME without config → error contains `HCLOUD_TOKEN`; with temp `cli.toml` → `HCLOUD_TOKEN=<token>` returned. API calls tested with `httptest.Server` via hcloud-go `WithEndpoint`.
- [ ] **Step 2:** FAIL. **Step 3:** implement. **Step 4:** PASS. `tofu fmt -check` and `tofu validate` (after `tofu init -backend=false` in temp dir) on the golden file pass.
- [ ] **Step 5:** Commit `feat(cloud): provider interface and hetzner`.

### Task 9: DigitalOcean provider

**Files:** Create `internal/cloud/digitalocean.go`, `internal/cloud/tf/digitalocean.tf.tmpl`; test `internal/cloud/digitalocean_test.go`, golden `testdata/golden/infra/digitalocean.tf`.

**Interfaces:** Implements `cloud.Provider` with ID `"digitalocean"`. Token `DIGITALOCEAN_TOKEN` or `access-token` from doctl config (`~/Library/Application Support/doctl/config.yaml` on macOS, `~/.config/doctl/config.yaml` on Linux). Defaults region `fra1`, size `s-1vcpu-1gb`, image `debian-13-x64` (verify current slug). Resources: `digitalocean_ssh_key`, `digitalocean_droplet` (user_data), `digitalocean_firewall` 22/80/443 in + all out.

- [ ] Same step cycle as Task 8 (golden, credentials error names `DIGITALOCEAN_TOKEN`, httptest via godo `SetBaseURL`, tofu validate). Commit `feat(cloud): digitalocean`.

### Task 10: Scaleway provider

**Files:** Create `internal/cloud/scaleway.go`, `internal/cloud/tf/scaleway.tf.tmpl`; test `internal/cloud/scaleway_test.go`, golden `testdata/golden/infra/scaleway.tf`.

**Interfaces:** Implements `cloud.Provider` with ID `"scaleway"`. Credentials `SCW_ACCESS_KEY`, `SCW_SECRET_KEY`, `SCW_DEFAULT_PROJECT_ID` or active profile of `~/.config/scw/config.yaml` (use `scw.LoadConfig()` from the SDK). Region = zone, default `fr-par-1`; size = smallest current general instance type (verify via `Sizes`). Resources: `scaleway_iam_ssh_key`, `scaleway_instance_ip`, `scaleway_instance_security_group` (inbound default drop, accept 22/80/443), `scaleway_instance_server` (image debian stable label, `user_data = { cloud-init = file(...) }`).

- [ ] Same step cycle (credentials error names `SCW_ACCESS_KEY`). Commit `feat(cloud): scaleway`.

### Task 11: `infra` module + cloud-init

**Files:**
- Create: `internal/module/infra/infra.go`, `internal/module/infra/cloud-init.yaml.tmpl`
- Test: `internal/module/infra/infra_test.go`, golden `testdata/golden/infra/cloud-init.yaml`

**Interfaces:**
- Consumes: `cloud.Get`, `cloud.TFInput`, engine steps.
- Produces: module ID `"infra"`, Requires none. Status Present if `HasTerraform` (then Plan returns no file steps, only `tofu` steps if `infra/` contains vauban-generated marker comment `# managed by vauban`).
- Plan steps, in order:
  1. `RunCommand` `infra:deploy-key`: `ssh-keygen -t ed25519 -N "" -C vauban-<project> -f ~/.ssh/vauban_<project>_ed25519` — skipped (no-op Apply) if file exists. Output `deploy_key_path`, `deploy_pubkey` (public key content is not secret).
  2. `WriteFile infra/main.tf` (from provider `Terraform`, first line `# managed by vauban`), `infra/cloud-init.yaml`, `infra/.gitignore` (`*.tfstate*`, `.terraform/`).
  3. `RunCommand infra:init` → `tofu -chdir=infra init -input=false`.
  4. `RunCommand infra:apply` → `tofu -chdir=infra apply -auto-approve -input=false` with `Cmd.Env` = provider `CredentialsEnv()`.
  5. `RunCommand infra:output` → `tofu -chdir=infra output -json`, Parse → `server_ip`, `server_ipv6`.
- Public keys are inlined in `main.tf`. Because the deploy key only exists after step 1, `main.tf` is written by a custom step `infra:main-tf` that renders during Apply from `env.In["deploy_pubkey"]` plus `~/.ssh/id_ed25519.pub` if present (same skip-if-exists rule as `WriteFile`).
- cloud-init content (pinned): create user `deploy` (groups docker, shell bash, ssh_authorized_keys), packages `ufw`, `unattended-upgrades`, `fail2ban`; Docker Engine + compose plugin from Docker's official apt repo; `/etc/ssh/sshd_config.d/10-vauban.conf` with `PermitRootLogin no`, `PasswordAuthentication no`; ufw default deny incoming, allow 22,80,443; unattended-upgrades with `Automatic-Reboot "true"` at `"04:00"`; `/etc/docker/daemon.json` `{"log-driver":"json-file","log-opts":{"max-size":"10m","max-file":"3"}}`; systemd timer `docker-prune.timer` daily running `docker image prune -af --filter until=168h`; `mkdir -p /home/deploy/app` owned by deploy.
- Drift: when `.vauban/state.json` has `infra:apply` done and `vauban.yaml` cloud/region/size differ from `st.Outputs["infra_fingerprint"]`, Plan inserts `RunCommand infra:plan` (`tofu plan -no-color`) and marks `infra:main-tf`, `infra:apply` not done; engine exposes the `tofu plan` output for the CLI to colour `destroy`/`replace` lines red. `infra:apply` outputs `infra_fingerprint = cloud/region/size`.

- [ ] **Step 1: Failing tests:** golden cloud-init; Plan step IDs for hetzner config equal `["infra:deploy-key","infra:main-tf","file:infra/cloud-init.yaml","file:infra/.gitignore","infra:init","infra:apply","infra:output"]`; `infra:output` Parse on sample `tofu output -json` returns `server_ip`; FakeRunner apply passes `HCLOUD_TOKEN=` in Cmd.Env; drift test: changed size → plan contains `infra:plan` and `infra:apply` is re-run.
- [ ] **Step 2:** FAIL. **Step 3:** implement. **Step 4:** PASS. `cloud-init schema --config-file` on golden if `cloud-init` available (skip otherwise).
- [ ] **Step 5:** Register; commit `feat(module): infra with hardened cloud-init`.

### Task 12: `deploy` module (Caddy + workflow + secrets) and preflight

**Files:**
- Create: `internal/module/deploy/deploy.go`, `internal/module/deploy/templates/{compose.yaml,Caddyfile,deploy.yml}`, `internal/engine/preflight.go`
- Test: `internal/module/deploy/deploy_test.go`, `internal/engine/preflight_test.go`, goldens `testdata/golden/deploy/*`

**Interfaces:**
- Consumes: `infra` outputs `server_ip`, `deploy_key_path`; `detect.Project.GitHubRepo`.
- Produces: module ID `"deploy"`, Requires `["container","ci","infra"]`. `engine.Preflight(p *detect.Project, c *config.Config, mods []Module) error`.
- Plan steps:
  1. `WriteFile deploy/compose.yaml`: services `caddy` (`caddy:2`, ports `80:80`, `443:443`, `443:443/udp`, volumes `caddy_data`, `caddy_config`, `./Caddyfile:/etc/caddy/Caddyfile:ro`, env `DOMAIN`, `APP_PORT`) and `app` (`image: ghcr.io/<owner>/<repo>:latest`, `env_file: .env`, no `ports`), both `restart: unless-stopped`.
  2. `WriteFile deploy/Caddyfile`: `{$DOMAIN} {\n\treverse_proxy app:{$APP_PORT}\n}`.
  3. `WriteFile .github/workflows/deploy.yml`: trigger `workflow_run` on workflow `ci` (types completed, branches main) with job guard `if: github.event.workflow_run.conclusion == 'success'`, or `push: branches [main]` when no CI workflow is known; `permissions: packages: write, contents: read`; steps: checkout, `docker/login-action` to ghcr with `GITHUB_TOKEN`, `docker/build-push-action` push tags `latest` + `${{ github.sha }}`; write `.env` from all secrets prefixed `APP_` (prefix stripped) using `toJSON(secrets)` + `jq`, mode 0600; `scp` `deploy/compose.yaml`, `deploy/Caddyfile`, `.env` to `deploy@$DEPLOY_HOST:~/app/`; ssh: `cd ~/app && echo $GHCR_TOKEN | docker login ghcr.io -u $ACTOR --password-stdin && DOMAIN=… APP_PORT=… docker compose pull && docker compose up -d --remove-orphans`. SSH key from secret `DEPLOY_SSH_KEY`, host key via `ssh-keyscan`. Use plain `ssh`/`scp` (no third-party SSH actions).
  4. `SetSecret DEPLOY_HOST` FromOutput `server_ip`; `SetSecret DEPLOY_SSH_KEY` FromFile `deploy_key_path`; `SetSecret DEPLOY_DOMAIN` FromOutput `domain` (custom step computes `c.Domain` or `<ip-with-dashes>.sslip.io`, output `domain`); `APP_PORT` written into deploy.yml as literal from `c.AppPort`.
- Preflight checks, all before any step, returning one error listing every problem: GitHubRepo non-empty ("deploy needs a GitHub remote (git remote add origin …)"); `gh auth status` succeeds; `tools.Tofu()` found; `ssh-keygen` found; provider `CredentialsEnv()` ok and `CheckAuth` ok; `c.Validate()` ok.

- [ ] **Step 1: Failing tests:** goldens for compose/Caddyfile/deploy.yml (both trigger variants); `sslip` domain for `1.2.3.4` → `1-2-3-4.sslip.io`; Preflight on project without remote and missing tofu → error contains both messages; deploy.yml parses as YAML and contains no `secrets.DEPLOY_SSH_KEY` echo.
- [ ] **Step 2:** FAIL. **Step 3:** implement. **Step 4:** PASS.
- [ ] **Step 5:** Register; commit `feat(module): caddy deploy workflow and github wiring`.

### Task 13: `plan` command (interactive choices + rendering)

**Files:**
- Create: `internal/cli/plan.go`, `internal/ui/forms.go`, `internal/ui/plan.go`
- Test: `internal/cli/plan_test.go`

**Interfaces:**
- Consumes: `config.Load/Save`, `cloud.Get/IDs`, `module.All`, `engine.Order`, `engine.Preflight`.
- Produces: `cli.buildPlan(ctx, root string, c *config.Config, force bool) ([]engine.Step, error)` reused by apply; `ui.AskConfig(p *detect.Project, existing *config.Config, regions/sizes funcs) (*config.Config, error)` (huh form: cloud select, region select, size select, domain input with placeholder "empty = <ip>.sslip.io", port input defaulting to `ExposedPort` or 8080/3000/8000).
- Behaviour: if `vauban.yaml` exists and no `--reconfigure` flag → use it without prompts. Without TTY and no `vauban.yaml` → use flags `--cloud --region --size --domain --port` + provider defaults; missing `--cloud` → exit 2 "no vauban.yaml: pass --cloud or run interactively". Saves `vauban.yaml`. Prints steps grouped by module: `+ write Dockerfile`, `· skip Dockerfile (exists)`, `$ tofu -chdir=infra apply …`, `🔑 gh secret set DEPLOY_HOST`. Prints local-state warning: "Terraform state is local (infra/terraform.tfstate). Back it up." Nothing executed.

- [ ] **Step 1: Failing tests:** non-TTY plan on go-app fixture copy with `--cloud hetzner` and FakeRunner/fake provider → writes `vauban.yaml` with `cloud: hetzner, app_port: 8080`, output contains `+ write Dockerfile` and `gh secret set DEPLOY_HOST`, no files other than `vauban.yaml` created; non-TTY without config/flags → exit code 2.
- [ ] **Step 2:** FAIL. **Step 3:** implement (inject provider registry + runner via package vars for tests). **Step 4:** PASS.
- [ ] **Step 5:** Commit `feat: plan command`.

### Task 14: `apply` and `destroy` commands

**Files:**
- Create: `internal/cli/apply.go`, `internal/cli/destroy.go`, `internal/ui/progress.go`
- Test: `internal/cli/apply_test.go`, `internal/cli/destroy_test.go`

**Interfaces:**
- Consumes: `buildPlan`, `engine.Execute`, `engine.LoadState`, `engine.Preflight`.
- Behaviour:
  - `apply`: preflight → render plan → confirm (huh confirm; `--yes` skips; non-TTY without `--yes` exits 2) → if plan contains drift destroy/replace lines, require typing project name (even with `--yes` unless `--force`) → Execute with a bubbletea spinner per step (plain line per step when !Styled) → success footer:
    ```
    ✓ ready · https://<domain>
    next: git add -A && git commit -m "chore: vauban setup" && git push
    ```
  - `destroy`: typed project-name confirmation (always, `--yes` not enough) → `tofu -chdir=infra destroy -auto-approve` with credentials env → `gh secret delete` DEPLOY_HOST/DEPLOY_SSH_KEY/DEPLOY_DOMAIN → remove `.vauban/state.json`. Leaves generated files and the local key in place, prints their paths.
- Ctrl-C: context cancelled, state already saved by engine, message "interrupted — rerun `vauban apply` to resume".

- [ ] **Step 1: Failing tests:** apply with FakeRunner on fixture copy + `--yes` → all steps done in state, footer contains `sslip.io`; second apply → zero runner calls; non-TTY without `--yes` → exit 2; destroy without typed confirmation in non-TTY → exit 2 and no runner calls.
- [ ] **Step 2:** FAIL. **Step 3:** implement. **Step 4:** PASS.
- [ ] **Step 5:** Commit `feat: apply and destroy commands`.

### Task 15: Release, project CI, README, budget check

**Files:**
- Create: `.goreleaser.yaml`, `.github/workflows/ci.yml`, `.github/workflows/release.yml`, `.golangci.yml`, `README.md`, `LICENSE` (MIT)

**Interfaces:** none new.

- [ ] **Step 1:** goreleaser v2 config: darwin/linux × amd64/arm64, `CGO_ENABLED=0`, `-s -w -X main.version={{.Version}}`, Homebrew tap `<author>/homebrew-tap` (builder: leave `skip_upload: auto` until the tap repo exists).
- [ ] **Step 2:** Project CI: `go test ./... -race`, golangci-lint, build. Release workflow on tag `v*`.
- [ ] **Step 3:** Measure: `go build -ldflags "-s -w" && ls -lh vauban` (< 20 MB) and `hyperfine './vauban --dir testdata/go-app'` or `time` (< 50 ms). If over budget, record numbers in README "Performance" section and open a follow-up note; do not block.
- [ ] **Step 4:** README: one-line pitch, GIF placeholder (vhs tape `docs/demo.tape` optional), install, quickstart (`vauban` → `vauban plan` → `vauban apply`), supported clouds/stacks, what gets created, security defaults, destroy.
- [ ] **Step 5:** `goreleaser check` passes; `go test ./...` PASS. Commit `chore: release pipeline and README`.

### Task 16: Manual end-to-end (one real cloud)

- [ ] **Step 1:** Create a scratch GitHub repo from `testdata/go-app`, run `vauban plan --cloud hetzner` then `vauban apply`, push, wait for deploy workflow, `curl https://<ip>.sslip.io` returns app response.
- [ ] **Step 2:** `vauban apply` again → no changes. `vauban destroy` → server gone, secrets removed.
- [ ] **Step 3:** Note any fixes as follow-up commits; repeat once for DigitalOcean and Scaleway when credentials are available.
