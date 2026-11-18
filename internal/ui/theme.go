// Package ui holds the terminal theme: a stone/amber palette echoing fortifications.
package ui

import (
	"os"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/term"
)

var (
	Title = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#D9A441")) // amber
	OK    = lipgloss.NewStyle().Foreground(lipgloss.Color("#7FA66B"))            // moss
	Warn  = lipgloss.NewStyle().Foreground(lipgloss.Color("#E0A030"))            // amber
	Err   = lipgloss.NewStyle().Foreground(lipgloss.Color("#C75D4A"))            // brick
	Muted = lipgloss.NewStyle().Foreground(lipgloss.Color("#8C857B"))            // stone
)

// Styled reports whether coloured output is appropriate: NO_COLOR unset and stdout a TTY.
func Styled() bool {
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	return term.IsTerminal(os.Stdout.Fd())
}

// Mark renders a status as a symbol, or plain text when output is unstyled.
// Status is one of "ok", "missing", "partial".
func Mark(status string) string {
	if !Styled() {
		return "[" + status + "]"
	}
	switch status {
	case "ok":
		return OK.Render("✓")
	case "missing":
		return Err.Render("✗")
	case "partial":
		return Warn.Render("◐")
	}
	return status
}
