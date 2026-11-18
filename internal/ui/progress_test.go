package ui

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/jean-baptisteAZAN/vauban/internal/engine"
)

func TestProgressExistingFileIsSkip(t *testing.T) {
	for _, styled := range []bool{false, true} {
		var buf bytes.Buffer
		p := &Progress{w: &buf, styled: styled}
		p.Update("file:Dockerfile", "write Dockerfile", engine.PhaseStart, nil)
		fmt.Fprintf(p.Log(), "skip (exists) Dockerfile\n")
		p.Update("file:Dockerfile", "write Dockerfile", engine.PhaseDone, nil)
		p.Update("file:x", "write x", engine.PhaseStart, nil)
		p.Update("file:x", "write x", engine.PhaseDone, nil)
		out := buf.String()
		if strings.Contains(out, "skip (exists)") || strings.Contains(out, "✓ write Dockerfile") || strings.Contains(out, "[ok] write Dockerfile") {
			t.Errorf("styled=%v: existing file shown as written:\n%s", styled, out)
		}
		if !strings.Contains(out, "write Dockerfile (exists)") || !(strings.Contains(out, "✓") || strings.Contains(out, "[ok] write x")) {
			t.Errorf("styled=%v: unexpected output:\n%s", styled, out)
		}
	}
}
