package detect

import (
	"os"
	"path/filepath"
	"reflect"
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
			if p.Stack != "node" || p.PackageManager != "pnpm" || p.Scripts["lint"] == "" || !p.HasTests {
				t.Errorf("got %+v", p)
			}
		}},
		{"python-app", func(t *testing.T, p *Project) {
			if p.Stack != "python" || p.PackageManager != "uv" || !p.HasTests || p.LangVersion != "3.12" {
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
