// Package detect inspects a repository on disk (filesystem only, no
// subprocesses, no network) to work out its stack and existing tooling.
package detect

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Stack is the detected language stack: "go", "node", "python" or "" (unknown).
type Stack string

// Project describes a scanned repository.
type Project struct {
	Root, Name     string // Name = base dir name, lowercased, [a-z0-9-]
	Stack          Stack
	PackageManager string // go | npm | pnpm | yarn | bun | uv | poetry | pip
	LangVersion    string // "" if unknown
	Scripts        map[string]string
	HasDockerfile  bool
	ExposedPort    int
	Workflows      []string
	HasTerraform   bool
	HasLintConfig  bool
	HasTests       bool
	IsGitRepo      bool
	GitHubRepo     string
}

var nameCleaner = regexp.MustCompile(`[^a-z0-9]+`)

func projectName(root string) string {
	n := nameCleaner.ReplaceAllString(strings.ToLower(filepath.Base(root)), "-")
	return strings.Trim(n, "-")
}

// Scan inspects root and returns what it finds.
func Scan(root string) (*Project, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if st, err := os.Stat(abs); err != nil {
		return nil, err
	} else if !st.IsDir() {
		return nil, &os.PathError{Op: "scan", Path: abs, Err: os.ErrInvalid}
	}
	p := &Project{Root: abs, Name: projectName(abs)}

	detectStack(p)
	detectDocker(p)
	detectWorkflows(p)
	p.HasTerraform = hasGlob(abs, "*.tf") || hasGlob(filepath.Join(abs, "infra"), "*.tf")
	detectGit(p)
	p.HasLintConfig = p.HasLintConfig || detectLint(abs)
	if p.Stack != "node" { // node tests run through the test script only
		p.HasTests = p.HasTests || detectTests(abs)
	}
	return p, nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func hasGlob(dir, pattern string) bool {
	m, _ := filepath.Glob(filepath.Join(dir, pattern))
	return len(m) > 0
}

func readFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

var dockerExpose = regexp.MustCompile(`(?im)^\s*EXPOSE\s+(\d+)`)

func detectDocker(p *Project) {
	data, err := os.ReadFile(filepath.Join(p.Root, "Dockerfile"))
	if err != nil {
		return
	}
	p.HasDockerfile = true
	if m := dockerExpose.FindSubmatch(data); m != nil {
		p.ExposedPort, _ = strconv.Atoi(string(m[1]))
	}
}

func detectWorkflows(p *Project) {
	entries, err := os.ReadDir(filepath.Join(p.Root, ".github", "workflows"))
	if err != nil {
		return
	}
	for _, e := range entries {
		ext := filepath.Ext(e.Name())
		if !e.IsDir() && (ext == ".yml" || ext == ".yaml") {
			p.Workflows = append(p.Workflows, e.Name())
		}
	}
	sort.Strings(p.Workflows)
}

func detectLint(root string) bool {
	for _, pat := range []string{
		".golangci.yml", ".golangci.yaml", ".golangci.toml", ".golangci.json",
		"ruff.toml", ".ruff.toml",
		".eslintrc", ".eslintrc.*", "eslint.config.*", "biome.json", "biome.jsonc",
	} {
		if hasGlob(root, pat) {
			return true
		}
	}
	return strings.Contains(readFile(filepath.Join(root, "pyproject.toml")), "[tool.ruff")
}

var skipDirs = map[string]bool{"node_modules": true, "vendor": true, ".git": true, ".venv": true, "venv": true}

func detectTests(root string) bool {
	if hasGlob(root, "*_test.go") || hasGlob(root, "test_*.py") || hasGlob(root, "*_test.py") {
		return true
	}
	for _, d := range []string{"tests", "test", "__tests__"} {
		if st, err := os.Stat(filepath.Join(root, d)); err == nil && st.IsDir() {
			return true
		}
	}
	// one level deep for Go packages
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if e.IsDir() && !skipDirs[e.Name()] && !strings.HasPrefix(e.Name(), ".") {
			if hasGlob(filepath.Join(root, e.Name()), "*_test.go") {
				return true
			}
		}
	}
	return false
}
