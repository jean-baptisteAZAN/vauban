// Package ci generates the GitHub Actions ci workflow and, when the repo has
// none, a default lint config.
package ci

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/jean-baptisteAZAN/vauban/internal/config"
	"github.com/jean-baptisteAZAN/vauban/internal/detect"
	"github.com/jean-baptisteAZAN/vauban/internal/engine"
)

//go:embed templates/*
var templates embed.FS

// WorkflowPath is where the workflow is written. Its name: is "ci", which the
// deploy workflow triggers on via workflow_run.
const WorkflowPath = ".github/workflows/ci.yml"

const nodeLintWarning = "node: no lint config, lint step skipped"

// Module implements engine.Module for CI.
type Module struct{}

// New returns the ci module.
func New() *Module { return &Module{} }

func (*Module) ID() string         { return "ci" }
func (*Module) Title() string      { return "ci" }
func (*Module) Requires() []string { return []string{"container"} }

func (*Module) Detect(p *detect.Project) engine.Status {
	for _, w := range p.Workflows {
		if strings.Contains(w, "ci") {
			return engine.StatusPresent
		}
	}
	return engine.StatusMissing
}

type data struct {
	Lint, Test bool
	// node
	PM, Node, Cache, Install string
	PMPinned                 bool
	// python
	UV, Poetry, Ruff, Pytest bool
	Reqs, Pkg                bool // pip: requirements.txt, an installable project
	Python                   string
}

func (*Module) Plan(p *detect.Project, _ *config.Config) ([]engine.Step, error) {
	var d data
	var name, lintFile, lintTpl string
	var warn bool
	switch p.Stack {
	case "go":
		name, d.Test = "go", p.HasTests
		lintFile, lintTpl = ".golangci.yml", "golangci.yml"
	case "node":
		name = "node"
		d.PM = p.PackageManager
		if d.PM == "" {
			d.PM = "npm"
		}
		d.Node = p.LangVersion
		if d.Node == "" {
			d.Node = "lts/*"
		}
		if lock := map[string]string{"npm": "package-lock.json", "pnpm": "pnpm-lock.yaml", "yarn": "yarn.lock"}[d.PM]; lock != "" && exists(p.Root, lock) {
			d.Cache = d.PM
		}
		d.Install = nodeInstall(d.PM, p.Root)
		d.PMPinned = packageManagerPinned(p.Root)
		_, d.Lint = p.Scripts["lint"]
		d.Test = p.HasTests
		warn = !d.Lint
	case "python":
		name = "python"
		d.UV = p.PackageManager == "uv"
		d.Poetry = p.PackageManager == "poetry"
		d.Python = p.LangVersion
		if d.Python == "" {
			d.Python = "3.x"
		}
		d.Test = p.HasTests
		pyproject := readFile(p.Root, "pyproject.toml")
		d.Ruff = strings.Contains(pyproject, "ruff")
		d.Pytest = strings.Contains(pyproject, "pytest")
		d.Reqs = exists(p.Root, "requirements.txt")
		d.Pkg = exists(p.Root, "pyproject.toml") || exists(p.Root, "setup.py")
		lintFile, lintTpl = "ruff.toml", "ruff.toml"
	default:
		return nil, errors.New("unsupported stack: ci supports go, node and python")
	}
	tpl, err := template.ParseFS(templates, "templates/partials.yml", "templates/"+name+".yml")
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, name+".yml", d); err != nil {
		return nil, err
	}
	steps := []engine.Step{engine.WriteFile{Path: WorkflowPath, Content: buf.Bytes(), Mode: 0o644}}
	if lintFile != "" && !p.HasLintConfig {
		cfg, err := templates.ReadFile("templates/" + lintTpl)
		if err != nil {
			return nil, err
		}
		steps = append(steps, engine.WriteFile{Path: lintFile, Content: cfg, Mode: 0o644})
	}
	if warn {
		steps = append(steps, warning{})
	}
	return steps, nil
}

// warning is a no-op step whose plan line tells the user something was skipped.
type warning struct{}

func (warning) ID() string       { return "ci:lint-warning" }
func (warning) Describe() string { return nodeLintWarning }
func (warning) Apply(context.Context, *engine.Env) (engine.Outputs, error) {
	return nil, nil
}

func nodeInstall(pm, root string) string {
	switch pm {
	case "pnpm":
		return "pnpm install --frozen-lockfile"
	case "yarn":
		if exists(root, ".yarnrc.yml") {
			return "yarn install --immutable"
		}
		return "yarn install --frozen-lockfile"
	case "bun":
		return "bun install --frozen-lockfile"
	}
	if exists(root, "package-lock.json") {
		return "npm ci"
	}
	return "npm install"
}

// packageManagerPinned reports whether package.json has a packageManager field
// (pnpm/action-setup reads the pnpm version from it).
func packageManagerPinned(root string) bool {
	var pkg struct {
		PackageManager string `json:"packageManager"`
		DevEngines     struct {
			PackageManager json.RawMessage `json:"packageManager"`
		} `json:"devEngines"`
	}
	_ = json.Unmarshal([]byte(readFile(root, "package.json")), &pkg)
	return pkg.PackageManager != "" || len(pkg.DevEngines.PackageManager) > 0
}

func readFile(root, name string) string {
	b, _ := os.ReadFile(filepath.Join(root, name))
	return string(b)
}

func exists(root, name string) bool {
	_, err := os.Stat(filepath.Join(root, name))
	return err == nil
}
