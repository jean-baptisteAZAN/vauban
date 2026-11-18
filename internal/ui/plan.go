package ui

import (
	"fmt"
	"io"
	"strings"
)

// PlanKind classifies a plan line.
type PlanKind int

const (
	PlanWrite   PlanKind = iota // a file to write; Text is its path
	PlanSkip                    // an existing file kept; Text is its path
	PlanDone                    // a step already done; Text is its description
	PlanCommand                 // a command to run; Text is the command line, Note its label
	PlanSecret                  // a GitHub secret to set; Text is its description
	PlanOther                   // any other step; Text is its description
)

// PlanItem is one line of plan output.
type PlanItem struct {
	Kind PlanKind
	Text string
	Note string // optional label, shown after the text
}

// PlanGroup is the steps of one module.
type PlanGroup struct {
	Module string
	Items  []PlanItem
}

// RenderPlan writes each module as a heading followed by its indented steps,
// then a one-line summary of what apply would do.
func RenderPlan(w io.Writer, groups []PlanGroup) {
	counts := map[PlanKind]int{}
	for _, g := range groups {
		if len(g.Items) == 0 {
			continue
		}
		fmt.Fprintln(w)
		heading := g.Module
		if Styled() {
			heading = Title.Render(heading)
		}
		fmt.Fprintln(w, heading)
		for _, it := range g.Items {
			counts[it.Kind]++
			fmt.Fprintln(w, "  "+planLine(it))
		}
	}
	var parts []string
	for _, c := range []struct {
		kind PlanKind
		word string
	}{
		{PlanWrite, "to write"}, {PlanCommand, "to run"}, {PlanSecret, "secrets"},
		{PlanOther, "other"}, {PlanSkip, "kept"}, {PlanDone, "done"},
	} {
		if n := counts[c.kind]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, c.word))
		}
	}
	if len(parts) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, Hint(strings.Join(parts, " · ")))
	}
}

func planLine(it PlanItem) string {
	styled := Styled()
	note := ""
	if it.Note != "" {
		note = "  (" + it.Note + ")"
		if styled {
			note = Muted.Render(note)
		}
	}
	switch it.Kind {
	case PlanDone:
		if styled {
			return OK.Render("✓") + " " + Muted.Render(it.Text+" (done)")
		}
		return "✓ " + it.Text + " (done)"
	case PlanSkip:
		line := "· skip " + it.Text + " (exists)"
		if styled {
			return Muted.Render(line)
		}
		return line
	case PlanWrite:
		if styled {
			return OK.Render("+") + " write " + it.Text
		}
		return "+ write " + it.Text
	case PlanCommand:
		if styled {
			return Warn.Render("$") + " " + it.Text + note
		}
		return "$ " + it.Text + note
	case PlanSecret:
		if styled {
			return "🔑 " + it.Text
		}
		return "[secret] " + it.Text
	}
	if styled {
		return OK.Render("+") + " " + it.Text
	}
	return "+ " + it.Text
}

// Drift writes the infra config change and what apply will do about it.
func Drift(w io.Writer, change string) {
	const note = "apply will show the exact tofu plan and ask for confirmation before destroying or replacing anything"
	fmt.Fprintln(w)
	if Styled() {
		fmt.Fprintln(w, Title.Render("drift"))
		fmt.Fprintln(w, "  "+change)
		fmt.Fprintln(w, "  "+Warn.Render(note))
		return
	}
	fmt.Fprintln(w, "drift")
	fmt.Fprintln(w, "  "+change)
	fmt.Fprintln(w, "  "+note)
}

// Warning writes a single warning line.
func Warning(w io.Writer, msg string) {
	if Styled() {
		msg = Warn.Render("! " + msg)
	} else {
		msg = "! " + msg
	}
	fmt.Fprintln(w, msg)
}

// Warnings writes a heading followed by one indented warning per message.
func Warnings(w io.Writer, heading string, msgs []string) {
	if len(msgs) == 0 {
		return
	}
	fmt.Fprintln(w)
	if Styled() {
		heading = Warn.Render(heading)
	}
	fmt.Fprintln(w, heading)
	for _, m := range msgs {
		fmt.Fprintln(w, "  "+Hint("- ")+m)
	}
}

// PlanFooter writes the local-state reminder and the next command.
func PlanFooter(w io.Writer) {
	fmt.Fprintln(w)
	const state = "Terraform state is local (infra/terraform.tfstate). Back it up."
	if Styled() {
		fmt.Fprintln(w, Warn.Render(state))
		fmt.Fprintln(w, Muted.Render("next: ")+Title.Render("vauban apply"))
		return
	}
	fmt.Fprintln(w, state)
	fmt.Fprintln(w, "next: vauban apply")
}
