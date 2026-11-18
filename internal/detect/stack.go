package detect

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	goDirective    = regexp.MustCompile(`(?m)^go\s+(\d+(?:\.\d+)*)`)
	requiresPython = regexp.MustCompile(`(?m)^\s*requires-python\s*=\s*["']([^"']+)["']`)
	versionNumber  = regexp.MustCompile(`\d+(?:\.\d+)*`)
)

func detectStack(p *Project) {
	r := p.Root
	switch {
	case exists(filepath.Join(r, "go.mod")):
		p.Stack, p.PackageManager = "go", "go"
		if m := goDirective.FindStringSubmatch(readFile(filepath.Join(r, "go.mod"))); m != nil {
			p.LangVersion = m[1]
		}
	case exists(filepath.Join(r, "package.json")):
		detectNode(p)
	case exists(filepath.Join(r, "pyproject.toml")), exists(filepath.Join(r, "requirements.txt")),
		exists(filepath.Join(r, "setup.py")), exists(filepath.Join(r, "Pipfile")):
		detectPython(p)
	}
}

func detectNode(p *Project) {
	r := p.Root
	p.Stack = "node"
	var pkg struct {
		Scripts      map[string]string `json:"scripts"`
		Engines      map[string]string `json:"engines"`
		ESLintConfig json.RawMessage   `json:"eslintConfig"`
	}
	_ = json.Unmarshal([]byte(readFile(filepath.Join(r, "package.json"))), &pkg)
	p.Scripts = pkg.Scripts
	if p.Scripts == nil {
		p.Scripts = map[string]string{}
	}
	// npm init's placeholder ("echo \"Error: no test specified\" && exit 1")
	// is not a test suite.
	if test, ok := p.Scripts["test"]; ok && !strings.Contains(test, "no test specified") {
		p.HasTests = true
	}
	if len(pkg.ESLintConfig) > 0 {
		p.HasLintConfig = true
	}
	switch {
	case exists(filepath.Join(r, "pnpm-lock.yaml")):
		p.PackageManager = "pnpm"
	case exists(filepath.Join(r, "yarn.lock")):
		p.PackageManager = "yarn"
	case exists(filepath.Join(r, "bun.lockb")), exists(filepath.Join(r, "bun.lock")):
		p.PackageManager = "bun"
	default:
		p.PackageManager = "npm"
	}
	if v := pkg.Engines["node"]; v != "" {
		p.LangVersion = versionNumber.FindString(v)
	}
	if p.LangVersion == "" {
		p.LangVersion = versionNumber.FindString(readFile(filepath.Join(r, ".nvmrc")))
	}
}

func detectPython(p *Project) {
	r := p.Root
	p.Stack = "python"
	pyproject := readFile(filepath.Join(r, "pyproject.toml"))
	switch {
	case exists(filepath.Join(r, "uv.lock")):
		p.PackageManager = "uv"
	case exists(filepath.Join(r, "poetry.lock")), strings.Contains(pyproject, "[tool.poetry"):
		p.PackageManager = "poetry"
	default:
		p.PackageManager = "pip"
	}
	if m := requiresPython.FindStringSubmatch(pyproject); m != nil {
		p.LangVersion = versionNumber.FindString(m[1])
	}
	if p.LangVersion == "" {
		p.LangVersion = versionNumber.FindString(readFile(filepath.Join(r, ".python-version")))
	}
}
