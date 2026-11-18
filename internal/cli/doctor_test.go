package cli

import (
	"bytes"
	"strings"
	"testing"
)

func doctorOut(t *testing.T, dir string) string {
	t.Helper()
	t.Setenv("NO_COLOR", "1")
	root := NewRoot("dev")
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--dir", dir})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestDoctorEmpty(t *testing.T) {
	got := doctorOut(t, "../../testdata/empty")
	for _, want := range []string{"vauban · ", "stack     unknown", "github    none", "next      vauban plan"} {
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
