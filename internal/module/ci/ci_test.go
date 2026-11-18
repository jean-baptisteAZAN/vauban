package ci

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/jean-baptisteAZAN/vauban/internal/config"
	"github.com/jean-baptisteAZAN/vauban/internal/detect"
	"github.com/jean-baptisteAZAN/vauban/internal/engine"
)

var update = flag.Bool("update", false, "regenerate golden files")

func scan(t *testing.T, fixture string) *detect.Project {
	t.Helper()
	p, err := detect.Scan(filepath.Join("..", "..", "..", "testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func plan(t *testing.T, p *detect.Project) []engine.Step {
	t.Helper()
	steps, err := New().Plan(p, &config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return steps
}

func workflow(t *testing.T, steps []engine.Step) string {
	t.Helper()
	w, ok := steps[0].(engine.WriteFile)
	if !ok || w.Path != ".github/workflows/ci.yml" {
		t.Fatalf("step 0 = %#v", steps[0])
	}
	return string(w.Content)
}

type wf struct {
	Name string                    `yaml:"name"`
	On   map[string]any            `yaml:"on"`
	Jobs map[string]map[string]any `yaml:"jobs"`
}

func parse(t *testing.T, s string) wf {
	t.Helper()
	var w wf
	if err := yaml.Unmarshal([]byte(s), &w); err != nil {
		t.Fatalf("invalid yaml: %v\n%s", err, s)
	}
	if w.Name != "ci" {
		t.Errorf("name = %q", w.Name)
	}
	if _, ok := w.On["push"]; !ok {
		t.Error("no push trigger")
	}
	if _, ok := w.On["pull_request"]; !ok {
		t.Error("no pull_request trigger")
	}
	if _, ok := w.Jobs["build"]; !ok {
		t.Error("no build job")
	}
	return w
}

func TestGolden(t *testing.T) {
	for _, tc := range []struct {
		fixture, golden string
		jobs            []string
	}{
		{"go-app", "go", []string{"lint", "test", "build"}},
		{"node-app", "node", []string{"lint", "test", "build"}},
		{"python-app", "python", []string{"lint", "test", "build"}},
	} {
		t.Run(tc.golden, func(t *testing.T) {
			p := scan(t, tc.fixture)
			got := workflow(t, plan(t, p))
			path := filepath.Join("..", "..", "..", "testdata", "golden", "ci", tc.golden+".yml")
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
			w := parse(t, got)
			if len(w.Jobs) != len(tc.jobs) {
				t.Errorf("jobs = %v, want %v", w.Jobs, tc.jobs)
			}
			for _, j := range tc.jobs {
				if _, ok := w.Jobs[j]; !ok {
					t.Errorf("missing job %s", j)
				}
			}
			if !strings.Contains(got, "push: false") {
				t.Error("build pushes")
			}
		})
	}
}

func TestLintConfig(t *testing.T) {
	for fixture, file := range map[string]string{"go-app": ".golangci.yml", "python-app": "ruff.toml"} {
		p := scan(t, fixture)
		steps := plan(t, p)
		if len(steps) != 2 {
			t.Fatalf("%s: %d steps", fixture, len(steps))
		}
		w, ok := steps[1].(engine.WriteFile)
		if !ok || w.Path != file {
			t.Fatalf("%s: step 1 = %#v", fixture, steps[1])
		}
		if file == ".golangci.yml" && !strings.HasPrefix(string(w.Content), "version: \"2\"") {
			t.Error("golangci config is not v2")
		}
		if file == "ruff.toml" && !strings.Contains(string(w.Content), `["E", "F", "I", "UP", "B"]`) {
			t.Error("ruff rules")
		}
		p.HasLintConfig = true
		if got := plan(t, p); len(got) != 1 {
			t.Errorf("%s: existing lint config still planned: %d steps", fixture, len(got))
		}
	}
}

func TestNodeVariants(t *testing.T) {
	p := scan(t, "node-app")
	if got := workflow(t, plan(t, p)); !strings.Contains(got, "run: pnpm run lint") ||
		!strings.Contains(got, "pnpm/action-setup@v6") || !strings.Contains(got, "cache: pnpm") {
		t.Errorf("node workflow:\n%s", got)
	}
	// no lint script: no lint job, warning line
	p.Scripts = map[string]string{"test": "node --test"}
	steps := plan(t, p)
	w := parse(t, workflow(t, steps))
	if _, ok := w.Jobs["lint"]; ok {
		t.Error("lint job without lint script")
	}
	if len(steps) != 2 || steps[1].Describe() != "node: no lint config, lint step skipped" {
		t.Errorf("steps = %#v", steps)
	}
	// no tests: no test job
	p.HasTests = false
	if _, ok := parse(t, workflow(t, plan(t, p))).Jobs["test"]; ok {
		t.Error("test job without tests")
	}
}

func TestGoNoTests(t *testing.T) {
	p := scan(t, "go-app")
	p.HasTests = false
	w := parse(t, workflow(t, plan(t, p)))
	if _, ok := w.Jobs["test"]; ok {
		t.Error("test job without tests")
	}
}

func TestPythonVariants(t *testing.T) {
	p := scan(t, "python-app")
	p.HasTests = false
	if _, ok := parse(t, workflow(t, plan(t, p))).Jobs["test"]; ok {
		t.Error("test job without tests")
	}
	p.HasTests = true
	got := workflow(t, plan(t, p))
	parse(t, got)
	for _, s := range []string{"astral-sh/setup-uv@v10", "uv sync --frozen", "uvx ruff check .", "uv run --with pytest pytest"} {
		if !strings.Contains(got, s) {
			t.Errorf("missing %q in\n%s", s, got)
		}
	}
	// ruff and pytest declared: plain uv run
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "pyproject.toml"), []byte("[dependency-groups]\ndev = [\"ruff\", \"pytest\"]\n"), 0o644)
	q := &detect.Project{Root: dir, Stack: "python", PackageManager: "uv", HasTests: true}
	got = workflow(t, plan(t, q))
	parse(t, got)
	if !strings.Contains(got, "uv run ruff check .") || !strings.Contains(got, "uv run pytest") {
		t.Errorf("uv deps:\n%s", got)
	}
	// pip fallback
	writeFile(t, filepath.Join(dir, "requirements.txt"), []byte("flask\n"), 0o644)
	q.PackageManager = "pip"
	got = workflow(t, plan(t, q))
	parse(t, got)
	for _, s := range []string{"actions/setup-python@v7", "pip install -r requirements.txt", "ruff check .", "run: pytest"} {
		if !strings.Contains(got, s) {
			t.Errorf("pip: missing %q in\n%s", s, got)
		}
	}
}

func TestDetectAndMeta(t *testing.T) {
	m := New()
	if m.ID() != "ci" || m.Title() != "ci" || len(m.Requires()) != 1 || m.Requires()[0] != "container" {
		t.Error("metadata")
	}
	if m.Detect(&detect.Project{}) != engine.StatusMissing {
		t.Error("want missing")
	}
	if m.Detect(&detect.Project{Workflows: []string{"release.yml", "ci.yml"}}) != engine.StatusPresent {
		t.Error("want present")
	}
	if _, err := m.Plan(&detect.Project{}, &config.Config{}); err == nil {
		t.Error("unknown stack should error")
	}
}

func TestNodePackageManagers(t *testing.T) {
	for _, tc := range []struct {
		pm, lock, pkg string
		cache         string // expected cache line, "" = none
		extra         string
		notWant       string
	}{
		{pm: "npm", lock: "package-lock.json", cache: "cache: npm"},
		{pm: "npm", cache: ""},
		{pm: "yarn", lock: "yarn.lock", cache: "cache: yarn"},
		{pm: "yarn", cache: ""},
		{pm: "pnpm", lock: "pnpm-lock.yaml", cache: "cache: pnpm", extra: "pnpm/action-setup@v6"},
		{pm: "pnpm", lock: "pnpm-lock.yaml", pkg: `,"devEngines":{"packageManager":{"name":"pnpm"}}`, cache: "cache: pnpm", notWant: "version: latest"},
		{pm: "bun", lock: "bun.lock", cache: "", extra: "oven-sh/setup-bun@v2", notWant: "npm install -g"},
	} {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "package.json"), []byte(`{"scripts":{"lint":"x","test":"y"}`+tc.pkg+`}`), 0o644)
		if tc.lock != "" {
			writeFile(t, filepath.Join(dir, tc.lock), nil, 0o644)
		}
		p := &detect.Project{Root: dir, Stack: "node", PackageManager: tc.pm, HasTests: true,
			Scripts: map[string]string{"lint": "x", "test": "y"}}
		got := workflow(t, plan(t, p))
		parse(t, got)
		if tc.cache == "" && strings.Contains(got, "cache:") {
			t.Errorf("%s/%q: unexpected cache:\n%s", tc.pm, tc.lock, got)
		}
		if tc.cache != "" && !strings.Contains(got, tc.cache) {
			t.Errorf("%s: missing %q", tc.pm, tc.cache)
		}
		if !strings.Contains(got, tc.pm+" run test") || strings.Contains(got, tc.pm+" test") {
			t.Errorf("%s: test command wrong:\n%s", tc.pm, got)
		}
		if tc.extra != "" && !strings.Contains(got, tc.extra) {
			t.Errorf("%s: missing %q", tc.pm, tc.extra)
		}
		if tc.notWant != "" && strings.Contains(got, tc.notWant) {
			t.Errorf("%s: unexpected %q", tc.pm, tc.notWant)
		}
	}
}

func writeFile(t *testing.T, name string, data []byte, perm os.FileMode) {
	t.Helper()
	if err := os.WriteFile(name, data, perm); err != nil {
		t.Fatal(err)
	}
}

// pip installs the project itself when it is a package; poetry installs
// the dependencies only (--no-root: the app need not be a package) and runs
// the tests in its environment.
func TestPythonPipAndPoetryInstall(t *testing.T) {
	for _, tc := range []struct {
		name, pm  string
		files     []string
		want, not []string
	}{
		{"pyproject only", "pip", []string{"pyproject.toml"},
			[]string{"run: pip install .\n"}, []string{"requirements.txt"}},
		{"setup.py only", "pip", []string{"setup.py"},
			[]string{"run: pip install .\n"}, []string{"requirements.txt"}},
		{"requirements only", "pip", []string{"requirements.txt"},
			[]string{"run: pip install -r requirements.txt\n"}, []string{"pip install .\n"}},
		{"both", "pip", []string{"pyproject.toml", "requirements.txt"},
			[]string{"run: pip install -r requirements.txt\n      - run: pip install .\n"}, nil},
		{"poetry", "poetry", []string{"pyproject.toml", "requirements.txt"},
			[]string{"run: pipx install poetry\n      - run: poetry install --no-root\n", "run: poetry run pip install pytest\n      - run: poetry run pytest\n"},
			[]string{"pip install .", "pip install -r", "run: pytest\n"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range tc.files {
				writeFile(t, filepath.Join(dir, f), []byte("\n"), 0o644)
			}
			p := &detect.Project{Root: dir, Stack: "python", PackageManager: tc.pm, HasTests: true}
			got := workflow(t, plan(t, p))
			parse(t, got)
			for _, s := range tc.want {
				if !strings.Contains(got, s) {
					t.Errorf("missing %q in\n%s", s, got)
				}
			}
			for _, s := range tc.not {
				if strings.Contains(got, s) {
					t.Errorf("unexpected %q in\n%s", s, got)
				}
			}
		})
	}
}
