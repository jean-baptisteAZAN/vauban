package detect

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestScanFixtures(t *testing.T) {
	tests := []struct {
		dir   string
		check func(t *testing.T, p *Project)
	}{
		{"go-app", func(t *testing.T, p *Project) {
			if p.Stack != "go" || p.PackageManager != "go" || p.LangVersion != "1.27" || !p.HasTests {
				t.Errorf("got %+v", p)
			}
		}},
		{"node-app", func(t *testing.T, p *Project) {
			// engines.node ">=20" is a range: use the default version.
			if p.Stack != "node" || p.PackageManager != "pnpm" || p.Scripts["lint"] == "" || !p.HasTests || p.LangVersion != "" {
				t.Errorf("got %+v", p)
			}
		}},
		{"python-app", func(t *testing.T, p *Project) {
			// requires-python ">=3.12" is a range: use the default version.
			if p.Stack != "python" || p.PackageManager != "uv" || !p.HasTests || p.LangVersion != "" {
				t.Errorf("got %+v", p)
			}
		}},
		{"empty", func(t *testing.T, p *Project) {
			if p.Stack != "" {
				t.Errorf("got %+v", p)
			}
		}},
		{"dockerized", func(t *testing.T, p *Project) {
			if !p.HasDockerfile || p.ExposedPort != 4000 || !p.HasTerraform ||
				!reflect.DeepEqual(p.Workflows, []string{"ci.yml"}) {
				t.Errorf("got %+v", p)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.dir, func(t *testing.T) {
			p, err := Scan(filepath.Join("..", "..", "testdata", tc.dir))
			if err != nil {
				t.Fatal(err)
			}
			if p.Name != tc.dir {
				t.Errorf("name = %q", p.Name)
			}
			tc.check(t, p)
		})
	}
}

func TestParseGitHubRemote(t *testing.T) {
	cases := map[string]string{
		"git@github.com:a/b.git":       "a/b",
		"https://github.com/a/b":       "a/b",
		"https://github.com/a/b.git":   "a/b",
		"https://gitlab.com/a/b":       "",
		"ssh://git@github.com/a/b.git": "a/b",
	}
	for in, want := range cases {
		if got := parseGitHubRemote(in); got != want {
			t.Errorf("%q = %q, want %q", in, got, want)
		}
	}
}

func TestGitRepoDetection(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "[core]\n\tbare = false\n[remote \"origin\"]\n\turl = git@github.com:acme/widget.git\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n"
	if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !p.IsGitRepo || p.GitHubRepo != "acme/widget" {
		t.Errorf("got %+v", p)
	}
}

func writeTemp(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// npm init's placeholder test script is not a test suite. Node tests run
// through the test script, so a tests/ directory alone does not count.
func TestNodePlaceholderTestScript(t *testing.T) {
	for script, want := range map[string]bool{
		`echo \"Error: no test specified\" && exit 1`: false,
		`vitest run`: true,
	} {
		dir := t.TempDir()
		writeTemp(t, dir, "package.json", `{"scripts":{"test":"`+script+`"}}`)
		writeTemp(t, dir, "test/a.test.js", "")
		p, err := Scan(dir)
		if err != nil {
			t.Fatal(err)
		}
		if p.HasTests != want {
			t.Errorf("test script %q: HasTests = %v, want %v", script, p.HasTests, want)
		}
	}
}

// Version ranges fall back to the default (latest LTS/stable); exact pins are kept.
func TestLangVersionRangesUseDefault(t *testing.T) {
	for spec, want := range map[string]string{
		"20": "20", "20.11.1": "20.11.1", "v22": "22", "=20.1": "20.1",
		">=20": "", "^20.1": "", "~20": "", "18 || 20": "", "<23": "", "20.x": "", "*": "",
	} {
		dir := t.TempDir()
		writeTemp(t, dir, "package.json", `{"engines":{"node":"`+spec+`"}}`)
		p, err := Scan(dir)
		if err != nil {
			t.Fatal(err)
		}
		if p.LangVersion != want {
			t.Errorf("engines.node %q: LangVersion = %q, want %q", spec, p.LangVersion, want)
		}
	}
	for spec, want := range map[string]string{
		"3.12": "3.12", "==3.12.1": "3.12.1",
		">=3.11": "", ">=3.9,<4": "", "~=3.11": "", "^3.11": "", "3.12.*": "",
	} {
		dir := t.TempDir()
		writeTemp(t, dir, "pyproject.toml", "[project]\nrequires-python = \""+spec+"\"\n")
		p, err := Scan(dir)
		if err != nil {
			t.Fatal(err)
		}
		if p.LangVersion != want {
			t.Errorf("requires-python %q: LangVersion = %q, want %q", spec, p.LangVersion, want)
		}
	}
	for file, content := range map[string]string{".nvmrc": "20.11.1\n", ".python-version": "3.12.4\n"} {
		dir := t.TempDir()
		manifest := map[string]string{".nvmrc": "package.json", ".python-version": "pyproject.toml"}[file]
		writeTemp(t, dir, manifest, map[string]string{"package.json": "{}", "pyproject.toml": "[project]\n"}[manifest])
		writeTemp(t, dir, file, content)
		p, err := Scan(dir)
		if err != nil {
			t.Fatal(err)
		}
		if want := strings.TrimSpace(content); p.LangVersion != want {
			t.Errorf("%s: LangVersion = %q, want %q", file, p.LangVersion, want)
		}
	}
}

func TestHasCommits(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  bool
	}{
		{"no git", nil, false},
		{"fresh init", map[string]string{".git/HEAD": "ref: refs/heads/main\n"}, false},
		{"loose ref", map[string]string{".git/HEAD": "ref: refs/heads/main\n", ".git/refs/heads/main": "abc\n"}, true},
		{"packed ref", map[string]string{".git/HEAD": "ref: refs/heads/main\n", ".git/packed-refs": "# pack-refs\nabc refs/heads/main\n"}, true},
		{"detached", map[string]string{".git/HEAD": "0123456789abcdef\n"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, content := range tc.files {
				writeTemp(t, dir, name, content)
			}
			p, err := Scan(dir)
			if err != nil {
				t.Fatal(err)
			}
			if p.HasCommits != tc.want {
				t.Errorf("HasCommits = %v, want %v", p.HasCommits, tc.want)
			}
		})
	}
}
