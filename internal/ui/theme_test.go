package ui

import "testing"

func TestMarkPlain(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	cases := map[string]string{"ok": "[ok]", "missing": "[missing]", "partial": "[partial]"}
	for in, want := range cases {
		if got := Mark(in); got != want {
			t.Errorf("Mark(%q) = %q, want %q", in, got, want)
		}
	}
	if Styled() {
		t.Error("Styled() should be false with NO_COLOR set")
	}
}
