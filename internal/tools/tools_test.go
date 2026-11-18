package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestToolsTofuFallback(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "terraform"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	name, ok := Tofu()
	if name != "terraform" || !ok {
		t.Fatalf("Tofu() = %q, %v", name, ok)
	}
	for _, tl := range Check() {
		if tl.Name == "terraform" && !tl.Found {
			t.Error("terraform should be reported found")
		}
		if tl.Name == "gh" && tl.Found {
			t.Error("gh should be missing")
		}
	}
}
