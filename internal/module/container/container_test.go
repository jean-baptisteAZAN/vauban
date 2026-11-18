package container

import (
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

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

func dockerfile(t *testing.T, p *detect.Project) string {
	t.Helper()
	steps, err := New().Plan(p, &config.Config{AppPort: 8080})
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 2 {
		t.Fatalf("want 2 steps, got %d", len(steps))
	}
	df, ok := steps[0].(engine.WriteFile)
	if !ok || df.Path != "Dockerfile" {
		t.Fatalf("step 0 = %#v", steps[0])
	}
	di, ok := steps[1].(engine.WriteFile)
	if !ok || di.Path != ".dockerignore" || !strings.Contains(string(di.Content), "node_modules") {
		t.Fatalf("step 1 = %#v", steps[1])
	}
	return string(df.Content)
}

func TestGolden(t *testing.T) {
	for _, tc := range []struct{ fixture, golden string }{
		{"go-app", "go"}, {"node-app", "node"}, {"python-app", "python"},
	} {
		t.Run(tc.golden, func(t *testing.T) {
			got := dockerfile(t, scan(t, tc.fixture))
			path := filepath.Join("..", "..", "..", "testdata", "golden", "container", tc.golden+".Dockerfile")
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
			if !strings.Contains(got, "EXPOSE 8080") {
				t.Error("missing EXPOSE 8080")
			}
			if strings.Count(got, "FROM ") < 2 {
				t.Error("not multi-stage")
			}
			if !strings.Contains(got, "USER ") {
				t.Error("no non-root USER")
			}
		})
	}
}

func TestVariants(t *testing.T) {
	cases := []struct {
		name   string
		p      detect.Project
		wants  []string
		absent []string
	}{
		{"node-npm", detect.Project{Stack: "node", PackageManager: "npm"},
			[]string{"FROM node:" + defaultNode + "-slim", `CMD ["npm", "start"]`, "npm prune --omit=dev"}, []string{"corepack"}},
		{"node-yarn", detect.Project{Stack: "node", PackageManager: "yarn", LangVersion: "20"},
			[]string{"FROM node:20-slim", "corepack enable", "yarn install --frozen-lockfile", `CMD ["npm", "start"]`}, []string{"--immutable"}},
		{"node-pnpm", detect.Project{Stack: "node", PackageManager: "pnpm"},
			[]string{"corepack enable", "pnpm install --frozen-lockfile", "pnpm prune --prod", `CMD ["npm", "start"]`}, nil},
		{"python-pip", detect.Project{Stack: "python", PackageManager: "pip"},
			[]string{"FROM python:" + defaultPython + "-slim", "pip install"}, []string{"astral-sh/uv"}},
		{"go-default", detect.Project{Stack: "go", PackageManager: "go"},
			[]string{"FROM golang:" + defaultGo + " AS", "CGO_ENABLED=0", "gcr.io/distroless/static-debian13:nonroot"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.p
			p.Root = t.TempDir()
			got := dockerfile(t, &p)
			for _, w := range tc.wants {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in:\n%s", w, got)
				}
			}
			for _, a := range tc.absent {
				if strings.Contains(got, a) {
					t.Errorf("unexpected %q in:\n%s", a, got)
				}
			}
		})
	}
}

func TestNodeFinalStageNoCorepack(t *testing.T) {
	for _, pm := range []string{"npm", "pnpm", "yarn", "bun"} {
		got := dockerfile(t, &detect.Project{Stack: "node", PackageManager: pm, Root: t.TempDir()})
		final := got[strings.LastIndex(got, "FROM "):]
		if strings.Contains(final, "corepack") || !strings.Contains(final, `CMD ["npm", "start"]`) {
			t.Errorf("%s final stage:\n%s", pm, final)
		}
	}
}

func TestYarnBerry(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".yarnrc.yml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	got := dockerfile(t, &detect.Project{Stack: "node", PackageManager: "yarn", Root: root})
	for _, w := range []string{"yarn install --immutable", "yarn workspaces focus --all --production", ".yarnrc.yml"} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in:\n%s", w, got)
		}
	}
}

func TestUnknownStack(t *testing.T) {
	m := New()
	empty := scan(t, "empty")
	if m.Detect(empty) != engine.StatusMissing {
		t.Error("empty should be Missing")
	}
	_, err := m.Plan(empty, &config.Config{AppPort: 8080})
	if err == nil || err.Error() != "unsupported stack: add a Dockerfile and rerun" {
		t.Errorf("err = %v", err)
	}
	d := scan(t, "dockerized")
	if m.Detect(d) != engine.StatusPresent {
		t.Error("dockerized should be Present")
	}
	steps, err := m.Plan(d, &config.Config{AppPort: 8080})
	if err != nil || len(steps) != 0 {
		t.Errorf("steps=%v err=%v", steps, err)
	}
}

func TestIdentity(t *testing.T) {
	m := New()
	if m.ID() != "container" || m.Title() != "container" || len(m.Requires()) != 0 {
		t.Error("bad identity")
	}
}

// Secrets and Terraform state in any subdirectory stay out of the build context.
func TestDockerignoreNested(t *testing.T) {
	steps, err := New().Plan(scan(t, "go-app"), &config.Config{AppPort: 8080})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(steps[1].(engine.WriteFile).Content), "\n")
	for _, want := range []string{"**/*.tfstate*", "**/.env", "**/.env.*"} {
		if !slices.Contains(lines, want) {
			t.Errorf("missing %q in .dockerignore:\n%s", want, strings.Join(lines, "\n"))
		}
	}
}
