package ui

import (
	"strings"
	"testing"

	"charm.land/huh/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestOptionLabelShowsIDOnce(t *testing.T) {
	cases := []struct {
		in   Choice
		want string
	}{
		{Choice{ID: "fra1", Label: "fra1 · Frankfurt 1"}, "fra1 · Frankfurt 1"},
		{Choice{ID: "cx23", Label: "cx23 · 2 vCPU · 4 GB · €3.79/mo"}, "cx23 · 2 vCPU · 4 GB · €3.79/mo"},
		{Choice{ID: "fr-par-1", Label: "fr-par-1"}, "fr-par-1"},
		{Choice{ID: "nyc3"}, "nyc3"},
		{Choice{ID: "x1", Label: "Somewhere"}, "x1 · Somewhere"},
	}
	for _, c := range cases {
		if got := optionLabel(c.in); got != c.want {
			t.Errorf("optionLabel(%+v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestChoiceLabelMapsValueToDisplay(t *testing.T) {
	clouds := []Choice{{ID: "digitalocean", Label: "DigitalOcean"}, {ID: "hetzner", Label: "Hetzner"}}
	if got := choiceLabel(clouds, "hetzner", cloudLabel); got != "Hetzner" {
		t.Errorf("choiceLabel(hetzner) = %q, want Hetzner", got)
	}
	if got := choiceLabel(clouds, "other", cloudLabel); got != "other" {
		t.Errorf("choiceLabel(unknown) = %q, want the ID", got)
	}
	regions := []Choice{{ID: "fra1", Label: "fra1 · Frankfurt 1"}}
	if got := choiceLabel(regions, "fra1", optionLabel); got != "fra1 · Frankfurt 1" {
		t.Errorf("choiceLabel(fra1) = %q", got)
	}
}

func TestAnswerSummary(t *testing.T) {
	got := Answer("Cloud", "Hetzner")
	if !strings.Contains(got, "Cloud") || !strings.Contains(got, "Hetzner") || strings.Contains(got, "\n") {
		t.Errorf("Answer = %q", got)
	}
}

// Once submitted, the form's last frame grows by one row (so the renderer
// leaves the cursor below it) instead of becoming empty.
func TestAnsweredFormGrowsLastFrame(t *testing.T) {
	var value string
	form := huh.NewForm(huh.NewGroup(huh.NewInput().Title("Domain").Value(&value)))
	m := answeredForm{form: form, last: new(string)}
	m.Init()
	before := m.View().Content
	if before == "" || *m.last != before {
		t.Fatalf("last view not remembered: %q / %q", before, *m.last)
	}
	form.State = huh.StateCompleted
	if got := m.View().Content; got != before+"\n" {
		t.Errorf("completed view = %q, want the last frame plus an empty row", got)
	}
	if got := eraseUp(lineCount(before)); got != "\r"+ansi.CursorUp(lineCount(before))+ansi.EraseScreenBelow {
		t.Errorf("eraseUp = %q", got)
	}
	if lineCount("") != 0 || lineCount("a\nb") != 2 {
		t.Error("lineCount")
	}
}
