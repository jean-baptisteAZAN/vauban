package main

import "testing"

func TestResolveVersion(t *testing.T) {
	for _, tc := range []struct{ injected, module, want string }{
		{"1.2.3", "v9.9.9", "1.2.3"},
		{"dev", "v0.3.0", "v0.3.0"},
		{"dev", "(devel)", "dev"},
		{"dev", "", "dev"},
	} {
		if got := resolveVersion(tc.injected, tc.module); got != tc.want {
			t.Errorf("resolveVersion(%q, %q) = %q, want %q", tc.injected, tc.module, got, tc.want)
		}
	}
}
