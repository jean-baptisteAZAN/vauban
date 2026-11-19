package ui

import (
	"fmt"
	"io"
	"strings"

	"github.com/jean-baptisteAZAN/vauban/internal/remote"
)

// RenderAudit writes the audit checks: a coloured table on a TTY, plain
// "name  status  detail" lines otherwise.
func RenderAudit(w io.Writer, a *remote.Audit) {
	width := 0
	for _, c := range a.Checks {
		width = max(width, len(c.Name))
	}
	styled := Styled()
	for _, c := range a.Checks {
		name := c.Name + strings.Repeat(" ", width-len(c.Name))
		status := fmt.Sprintf("%-7s", c.Status)
		detail := c.Detail
		if styled {
			status = auditStyle(c.Status).Render(status)
			detail = Muted.Render(detail)
		}
		fmt.Fprintln(w, strings.TrimRight(name+"  "+status+"  "+detail, " "))
	}
}
