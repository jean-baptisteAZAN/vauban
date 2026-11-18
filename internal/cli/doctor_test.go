package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func doctorOut(t *testing.T, dir string) string {
	t.Helper()
	return doctorOutArgs(t, "--dir", dir)
}

func doctorOutArgs(t *testing.T, args ...string) string {
	t.Helper()
	t.Setenv("NO_COLOR", "1")
	root := NewRoot("dev")
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestDoctorEmpty(t *testing.T) {
	got := doctorOut(t, "../../testdata/empty")
	for _, want := range []string{"vauban · ", "stack     unknown", "github    none", "next      commit first (git init && git add -A && git commit -m \"init\"), then gh repo create empty --private --source . --push"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\x1b") {
		t.Error("plain output contains ANSI")
	}
}

func TestDoctorGoApp(t *testing.T) {
	got := doctorOut(t, "../../testdata/go-app")
	if !strings.Contains(got, "stack     go (go ") {
		t.Errorf("unexpected stack line:\n%s", got)
	}
}

func TestDoctorDockerized(t *testing.T) {
	got := doctorOut(t, "../../testdata/dockerized")
	if !strings.Contains(got, "[ok] container") {
		t.Errorf("missing [ok] container in:\n%s", got)
	}
}

func TestDoctorSubcommand(t *testing.T) {
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("../../testdata/go-app")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, ".git/config", "[remote \"origin\"]\n\turl = git@github.com:acme/shop.git\n")
	got := doctorOutArgs(t, "doctor", "--dir", dir)
	if !strings.Contains(got, "stack     go (go ") || !strings.Contains(got, "next      vauban plan") {
		t.Errorf("unexpected doctor report:\n%s", got)
	}
}
