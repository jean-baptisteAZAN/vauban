package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/jean-baptisteAZAN/vauban/internal/cloud"
	"github.com/jean-baptisteAZAN/vauban/internal/config"
	"github.com/jean-baptisteAZAN/vauban/internal/detect"
	"github.com/jean-baptisteAZAN/vauban/internal/engine"
	"github.com/jean-baptisteAZAN/vauban/internal/module"
	"github.com/jean-baptisteAZAN/vauban/internal/module/deploy"
	"github.com/jean-baptisteAZAN/vauban/internal/module/infra"
	"github.com/jean-baptisteAZAN/vauban/internal/module/server"
	"github.com/jean-baptisteAZAN/vauban/internal/ui"
)

// interactive reports whether prompts are possible (stdin and stdout TTYs).
var interactive = func() bool {
	return term.IsTerminal(os.Stdin.Fd()) && term.IsTerminal(os.Stdout.Fd())
}

var cloudLabels = map[string]string{"scaleway": "Scaleway", "digitalocean": "DigitalOcean", "hetzner": "Hetzner"}

func newPlanCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Choose a cloud and show every file and command apply would run",
		Long: "Choose a cloud and show every file and command apply would run.\n\n" +
			"Uses vauban.yaml when it exists (--reconfigure asks again); flags override vauban.yaml.",
		Args: cobra.NoArgs,
		RunE: abortable(runPlan),
	}
	addConfigFlags(cmd)
	return cmd
}

// addConfigFlags adds the flags resolveConfig reads.
func addConfigFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.String("cloud", "", "cloud provider: scaleway, digitalocean or hetzner")
	f.String("region", "", "region (default: the provider's)")
	f.String("size", "", "server size (default: the provider's)")
	f.String("domain", "", "public domain (default: <ip>.sslip.io)")
	f.Int("port", 0, "port the app listens on (default: from Dockerfile or stack)")
	f.Bool("reconfigure", false, "ask again even if vauban.yaml exists")
	addTargetFlags(cmd)
}

func runPlan(cmd *cobra.Command, _ []string) error {
	dir, _ := cmd.Flags().GetString("dir")
	force, _ := cmd.Flags().GetBool("force")
	root, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	p, err := detect.Scan(root)
	if err != nil {
		return err
	}
	if err := requireRemote(p); err != nil {
		return err
	}
	c, err := resolveConfig(cmd, p)
	if err != nil {
		return err
	}
	if err := saveConfig(root, c); err != nil {
		return err
	}
	pl, err := preparePlan(root, c, force)
	if err != nil {
		return err
	}

	w := cmd.OutOrStdout()
	writeConfigSummary(w, p, c)
	var auditLines []string
	if c.Existing() {
		fmt.Fprintln(w)
		auditLines, _ = writeAudit(cmd.Context(), w, root, p.GitHubRepo, c)
	}
	ui.RenderPlan(w, pl.items(root, force))
	writeDrift(w, root, pl.st, c, pl.infraStale)
	writeSiteChange(w, pl, c)
	switch {
	case c.Existing():
		fmt.Fprintln(w)
		fmt.Fprintln(w, "DNS: point "+c.Domain+" to "+c.Server.Host)
	case c.Domain != "":
		fmt.Fprintln(w)
		fmt.Fprintln(w, "DNS: point "+c.Domain+" to the server IP (shown after apply)")
	}
	notices, err := engine.Preflight(pl.p, c, pl.mods, pl.preflightOptions(root, c))
	lines := auditLines
	if c.Existing() && c.Server != nil {
		if hw := hostWarning(c.Server.Host); hw != "" {
			lines = append(lines, hw)
		}
		if pw := portWarning(pl.st, c, force); pw != "" {
			lines = append(lines, pw)
		}
	}
	if err != nil {
		lines = append(lines, strings.Split(err.Error(), "\n")...)
	}
	ui.Warnings(w, "before apply:", append(lines, notices.Warnings...))
	writeNotes(w, notices.Notes)
	if c.Existing() {
		ui.PlanFooterExisting(w)
		return nil
	}
	ui.PlanFooter(w)
	return nil
}

// saveConfig saves c as soon as it is resolved, before the plan builds, so
// answers survive a failing plan, but only once infra accepts the cloud: a
// refused cloud or server change must never reach vauban.yaml (destroy reads it).
func saveConfig(root string, c *config.Config) error {
	st, err := engine.LoadState(root)
	if err != nil {
		return err
	}
	if err := infra.CheckCloud(root, st, c); err != nil {
		return err
	}
	if err := server.CheckTarget(st, c); err != nil {
		return err
	}
	return c.Save(root)
}

// writeNotes prints preflight notes (e.g. a quota the cloud cannot report).
func writeNotes(w io.Writer, notes []string) {
	if len(notes) == 0 {
		return
	}
	fmt.Fprintln(w)
	for _, n := range notes {
		fmt.Fprintln(w, ui.Hint("note: "+n))
	}
}

// resolveConfig returns vauban.yaml, or builds it from flags or prompts. It
// does not save it: callers do as soon as it is returned (validated). Explicit flags override
// values from an existing vauban.yaml; a different --cloud resets region and
// size to the new provider's defaults unless they are passed too.
func resolveConfig(cmd *cobra.Command, p *detect.Project) (*config.Config, error) {
	f := cmd.Flags()
	reconfigure, _ := f.GetBool("reconfigure")
	yes, _ := f.GetBool("yes")
	existing, err := config.Load(p.Root)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	c := &config.Config{}
	if existing != nil {
		*c = *existing
	}
	if err := applyTargetFlags(cmd, c); err != nil {
		return nil, err
	}
	for name, dst := range map[string]*string{"cloud": &c.Cloud, "region": &c.Region, "size": &c.Size, "domain": &c.Domain} {
		if f.Changed(name) {
			*dst, _ = f.GetString(name)
		}
	}
	if f.Changed("port") {
		c.AppPort, _ = f.GetInt("port")
	}
	if existing != nil && !c.Existing() && c.Cloud != existing.Cloud {
		if !f.Changed("region") {
			c.Region = ""
		}
		if !f.Changed("size") {
			c.Size = ""
		}
	}

	if existing == nil || reconfigure {
		if interactive() && !yes {
			if c, err = ui.AskConfig(p, c, formSources(p)); err != nil {
				return nil, err
			}
		} else if c.Existing() {
			if err := missingExistingFlags(c); err != nil {
				return nil, err
			}
		} else if c.Cloud == "" {
			return nil, &ExitError{Code: 2, Err: errors.New("no vauban.yaml: pass --cloud or run interactively")}
		}
	}

	if c.Project == "" {
		c.Project = p.Name
	}
	if c.AppPort == 0 {
		c.AppPort = defaultPort(p)
	}
	if prov, err := cloud.Get(c.Cloud); err == nil {
		c.Region = orDefault(c.Region, prov.DefaultRegion())
		c.Size = orDefault(c.Size, prov.DefaultSize())
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", config.FileName, err)
	}
	return c, nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// defaultPort is the Dockerfile's EXPOSE, else the stack's usual port.
func defaultPort(p *detect.Project) int {
	if p.ExposedPort > 0 {
		return p.ExposedPort
	}
	switch p.Stack {
	case "node":
		return 3000
	case "python":
		return 8000
	}
	return 8080
}

// formSources exposes the cloud registry to the config form.
func formSources(p *detect.Project) ui.Sources {
	var clouds []ui.Choice
	for _, id := range cloud.IDs() {
		clouds = append(clouds, ui.Choice{ID: id, Label: orDefault(cloudLabels[id], id)})
	}
	def := func(get func(cloud.Provider) string) func(string) string {
		return func(id string) string {
			if prov, err := cloud.Get(id); err == nil {
				return get(prov)
			}
			return ""
		}
	}
	choices := func(opts []cloud.Option, err error) ([]ui.Choice, error) {
		out := make([]ui.Choice, 0, len(opts))
		for _, o := range opts {
			out = append(out, ui.Choice{ID: o.ID, Label: o.Label})
		}
		return out, err
	}
	return ui.Sources{
		Clouds:        clouds,
		DefaultRegion: def(cloud.Provider.DefaultRegion),
		DefaultSize:   def(cloud.Provider.DefaultSize),
		Regions: func(ctx context.Context, id string) ([]ui.Choice, error) {
			prov, err := cloud.Get(id)
			if err != nil {
				return nil, err
			}
			return choices(prov.Regions(ctx))
		},
		Sizes: func(ctx context.Context, id, region string) ([]ui.Choice, error) {
			prov, err := cloud.Get(id)
			if err != nil {
				return nil, err
			}
			return choices(prov.Sizes(ctx, region))
		},
		DefaultPort: defaultPort(p),
		SSHKeys:     sshKeyChoices,
	}
}

// moduleSteps is one module's planned steps.
type moduleSteps struct {
	id    string
	steps []engine.Step
}

// planned is everything plan and apply need: the ordered steps per module
// and the state with stale steps already cleared (in memory only).
type planned struct {
	p          *detect.Project
	mods       []engine.Module
	groups     []moduleSteps
	st         *engine.State
	infraStale []string
	siteStale  string // the applied server_site when the site is rewritten, else ""
}

// buildPlan scans root and returns the ordered steps of every module and the
// loaded state, with stale step IDs (and, with force, file steps) removed
// from st.Done so they run again. It never saves state.
func buildPlan(_ context.Context, root string, c *config.Config, force bool) ([]engine.Step, *engine.State, error) {
	pl, err := preparePlan(root, c, force)
	if err != nil {
		return nil, nil, err
	}
	return pl.steps(), pl.st, nil
}

func preparePlan(root string, c *config.Config, force bool) (*planned, error) {
	p, err := detect.Scan(root)
	if err != nil {
		return nil, err
	}
	mods, err := engine.Order(module.All())
	if err != nil {
		return nil, err
	}
	pl := &planned{p: p, mods: mods}
	for _, m := range mods {
		steps, err := m.Plan(p, c)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", m.ID(), err)
		}
		pl.groups = append(pl.groups, moduleSteps{id: m.ID(), steps: steps})
	}
	if pl.st, err = engine.LoadState(root); err != nil {
		return nil, err
	}
	if !c.Existing() { // no infra module, no infra fingerprint
		pl.infraStale = infra.StaleSteps(root, pl.st, c)
	}
	stale := slices.Concat(pl.infraStale, deploy.StaleSteps(pl.st, c), deploy.KnownHostsStale(root, pl.st, c))
	if len(pl.infraStale) > 0 {
		// The server may be replaced: re-read its IP after infra:output.
		stale = append(stale, deploy.ServerSteps()...)
	}
	for _, id := range server.StaleSteps(pl.st, c) {
		stale = append(stale, id)
		if id == "server:site" {
			// A new domain or app port: rewrite the site and the deploy secrets.
			pl.siteStale = cmp.Or(pl.st.Outputs["server_site"], "unrecorded")
			stale = append(stale, deploy.ExistingServerSteps()...)
		}
	}
	for _, id := range stale {
		delete(pl.st.Done, id)
	}
	if force {
		for id := range pl.st.Done {
			if strings.HasPrefix(id, "file:") || id == "infra:main-tf" || id == "infra:cloud-init" {
				delete(pl.st.Done, id)
			}
		}
	}
	return pl, nil
}

// preflightOptions says how preflight treats the cloud quota:
//   - skip when apply creates no server (infra:apply done, or the applied
//     infra already has this cloud/region/size);
//   - block, with Replacing, on drift of an applied server (the per-type or
//     per-zone quota of the new size still matters);
//   - warn only on a true resume: nothing applied yet but a rendered
//     fingerprint or a non-empty tfstate, so an interrupted first apply may
//     have left resources (tofu and the quota hint on its error back it up);
//   - block otherwise (a first create).
func (pl *planned) preflightOptions(root string, c *config.Config) engine.PreflightOptions {
	if c.Existing() {
		return engine.PreflightOptions{Quota: engine.QuotaSkip}
	}
	applied := pl.st.Outputs["infra_fingerprint"]
	switch {
	case pl.st.Done["infra:apply"] || applied == infraFingerprint(c):
		return engine.PreflightOptions{Quota: engine.QuotaSkip, Replacing: applied != ""}
	case applied != "":
		return engine.PreflightOptions{Quota: engine.QuotaBlock, Replacing: true}
	case pl.st.Outputs["rendered_fingerprint"] != "" || nonEmpty(filepath.Join(root, "infra", "terraform.tfstate")):
		return engine.PreflightOptions{Quota: engine.QuotaWarn}
	}
	return engine.PreflightOptions{Quota: engine.QuotaBlock}
}

func nonEmpty(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Size() > 0
}

func (pl *planned) steps() []engine.Step {
	var out []engine.Step
	for _, g := range pl.groups {
		out = append(out, g.steps...)
	}
	return out
}

// items turns the steps into plan lines grouped by module.
func (pl *planned) items(root string, force bool) []ui.PlanGroup {
	rerender := map[string]bool{}
	for _, id := range pl.infraStale {
		rerender[id] = true
	}
	groups := make([]ui.PlanGroup, 0, len(pl.groups))
	for _, g := range pl.groups {
		pg := ui.PlanGroup{Module: g.id}
		for _, s := range g.steps {
			pg.Items = append(pg.Items, planItem(s, root, pl.st, force || rerender[s.ID()]))
		}
		groups = append(groups, pg)
	}
	return groups
}

// kinded is implemented by custom steps that render like a built-in step
// (engine.KindWrite or engine.KindCommand).
type kinded interface{ Kind() string }

// keeper is implemented by steps that may find their work already present
// (Kept returns the plan line then, else "").
type keeper interface{ Kept() string }

// planItem classifies a step by its type: done, secret, file (write or skip),
// command, or other.
func planItem(s engine.Step, root string, st *engine.State, overwrite bool) ui.PlanItem {
	d := s.Describe()
	if st.Done[s.ID()] {
		return ui.PlanItem{Kind: ui.PlanDone, Text: d}
	}
	file := func(path string) ui.PlanItem {
		if _, err := os.Stat(filepath.Join(root, path)); err == nil && !overwrite {
			return ui.PlanItem{Kind: ui.PlanSkip, Text: path}
		}
		return ui.PlanItem{Kind: ui.PlanWrite, Text: path}
	}
	if k, ok := s.(keeper); ok {
		if text := k.Kept(); text != "" {
			return ui.PlanItem{Kind: ui.PlanKept, Text: text}
		}
	}
	switch s := s.(type) {
	case engine.SetSecret:
		return ui.PlanItem{Kind: ui.PlanSecret, Text: d}
	case engine.WriteFile:
		return file(s.Path)
	case engine.RunCommand:
		return ui.PlanItem{Kind: ui.PlanCommand, Text: strings.Join(append([]string{s.Cmd.Name}, s.Cmd.Args...), " "), Note: s.Label}
	case kinded:
		switch s.Kind() {
		case engine.KindWrite:
			return file(strings.TrimPrefix(d, "write "))
		case engine.KindCommand:
			if label, line, ok := strings.Cut(d, "$ "); ok {
				return ui.PlanItem{Kind: ui.PlanCommand, Text: line, Note: strings.TrimSuffix(strings.TrimSpace(label), ":")}
			}
		}
	}
	return ui.PlanItem{Kind: ui.PlanOther, Text: d}
}

func writeConfigSummary(w io.Writer, p *detect.Project, c *config.Config) {
	ui.Heading(w, p.Name)
	if c.Existing() && c.Server != nil {
		ui.Row(w, "server", fmt.Sprintf("%s@%s:%d", c.Server.User, c.Server.Host, c.Server.SSHPort()))
	} else {
		ui.Row(w, "cloud", orDefault(cloudLabels[c.Cloud], c.Cloud)+" · "+c.Region+" · "+c.Size)
	}
	ui.Row(w, "domain", orDefault(c.Domain, "<ip>.sslip.io"+ui.Hint(" (after apply)")))
	ui.Row(w, "port", fmt.Sprint(c.AppPort))
}

// writeDrift reports why infra steps run again: vauban.yaml no longer
// matches the applied infra (drift), a first apply was interrupted (resume),
// or the managed main.tf on disk was rendered for another config with
// nothing applied (kept after destroy).
// The exact tofu plan is only meaningful once main.tf is re-rendered, so
// apply shows it.
func writeDrift(w io.Writer, root string, st *engine.State, c *config.Config, stale []string) {
	if len(stale) == 0 {
		return
	}
	want := infraFingerprint(c)
	if applied := st.Outputs["infra_fingerprint"]; applied != "" {
		ui.Drift(w, "infra config changed: "+applied+" → "+want)
		return
	}
	if st.Outputs["rendered_fingerprint"] != "" { // first apply, interrupted
		ui.Section(w, "resume", "resuming an unfinished apply")
		return
	}
	if disk := infra.RenderedFingerprint(root); disk != "" && disk != want {
		ui.Section(w, "infra", "infra/main.tf was rendered for "+disk+"; apply re-renders it for "+want)
	}
}

// portWarning warns when app_port differs from the port the site was
// written for and --force is not set: the generated workflow (skipped as an
// existing file) still deploys with the old APP_PORT, so Caddy, rewritten
// for the new port, returns 502. "" otherwise.
func portWarning(st *engine.State, c *config.Config, force bool) string {
	rec := st.Outputs["server_site"]
	i := strings.LastIndex(rec, "|")
	if !c.Existing() || force || i < 0 || rec[i+1:] == strconv.Itoa(c.AppPort) {
		return ""
	}
	old := rec[i+1:]
	return "app_port changed (" + old + " → " + strconv.Itoa(c.AppPort) + ") but the deploy workflow still sets APP_PORT=" + old +
		": rerun with --force to regenerate the deploy files, then push; until then Caddy returns 502"
}

// writeSiteChange says why the Caddy site and deploy secrets run again.
func writeSiteChange(w io.Writer, pl *planned, c *config.Config) {
	if pl.siteStale == "" {
		return
	}
	ui.Section(w, "site", "domain or port changed: "+pl.siteStale+" → "+server.SiteFingerprint(c)+
		"; apply rewrites the Caddy site ("+c.Project+".caddy, replacing the old one) and the deploy secrets")
}

// infraFingerprint is cloud/region/size with provider defaults filled in,
// as the infra module records it.
func infraFingerprint(c *config.Config) string {
	region, size := c.Region, c.Size
	if prov, err := cloud.Get(c.Cloud); err == nil {
		region, size = orDefault(region, prov.DefaultRegion()), orDefault(size, prov.DefaultSize())
	}
	return c.Cloud + "/" + region + "/" + size
}
