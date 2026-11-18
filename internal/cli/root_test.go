package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersionCommand(t *testing.T) {
	root := NewRoot("1.2.3")
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"version"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "vauban") || !strings.Contains(got, "1.2.3") {
		t.Errorf("unexpected output %q", got)
	}
}

func TestGlobalFlags(t *testing.T) {
	root := NewRoot("dev")
	for _, f := range []string{"dir", "yes", "force"} {
		if root.PersistentFlags().Lookup(f) == nil {
			t.Errorf("missing flag --%s", f)
		}
	}
}
