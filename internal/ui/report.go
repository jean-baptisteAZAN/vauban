package ui

import (
	"fmt"
	"io"
	"strings"
)

const labelWidth = 10

// Heading writes the report title line.
func Heading(w io.Writer, name string) {
	text := "vauban · " + name
	if Styled() {
		text = Title.Render("vauban") + Muted.Render(" · ") + name
	}
	fmt.Fprintln(w, text)
}

// Row writes a "label     value" line; the label is muted when styled.
func Row(w io.Writer, label, value string) {
	pad := strings.Repeat(" ", max(labelWidth-len(label), 1))
	if Styled() {
		label = Muted.Render(label)
	}
	fmt.Fprintf(w, "%s%s%s\n", label, pad, value)
}

// Hint renders secondary text (muted when styled).
func Hint(s string) string {
	if Styled() {
		return Muted.Render(s)
	}
	return s
}
