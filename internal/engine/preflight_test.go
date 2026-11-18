package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jean-baptisteAZAN/vauban/internal/cloud"
	"github.com/jean-baptisteAZAN/vauban/internal/config"
	"github.com/jean-baptisteAZAN/vauban/internal/detect"
)

const preflightToken = "super-secret-token-value"

type preflightProvider struct {
	credErr, authErr error
	authCalled       *bool
	quota            cloud.QuotaStatus
	quotaErr         error
	quotaCalled      *bool
	gotReplacing     *bool
}

func (preflightProvider) ID() string            { return "hetzner" }
func (preflightProvider) DefaultRegion() string { return "nbg1" }
func (preflightProvider) DefaultSize() string   { return "cx23" }
func (p preflightProvider) CredentialsEnv() ([]string, error) {
	if p.credErr != nil {
		return nil, p.credErr
	}
	return []string{"HCLOUD_TOKEN=" + preflightToken}, nil
}
func (p preflightProvider) CheckAuth(context.Context) error {
	*p.authCalled = true
	return p.authErr
}
func (preflightProvider) Regions(context.Context) ([]cloud.Option, error) { return nil, nil }
func (preflightProvider) Sizes(context.Context, string) ([]cloud.Option, error) {
	return nil, nil
}
func (preflightProvider) Terraform(cloud.TFInput) ([]byte, error) { return nil, nil }
func (p preflightProvider) Quota(_ context.Context, _, _ string, replacing bool) (cloud.QuotaStatus, error) {
	if p.quotaCalled != nil {
		*p.quotaCalled = true
	}
	if p.gotReplacing != nil {
		*p.gotReplacing = replacing
	}
	if p.quotaErr == nil && p.quota == (cloud.QuotaStatus{}) {
		return cloud.QuotaStatus{OK: true}, nil
	}
	return p.quota, p.quotaErr
}

type idModule string

func (m idModule) ID() string                                         { return string(m) }
func (m idModule) Title() string                                      { return string(m) }
func (idModule) Detect(*detect.Project) Status                        { return StatusMissing }
func (idModule) Requires() []string                                   { return nil }
func (idModule) Plan(*detect.Project, *config.Config) ([]Step, error) { return nil, nil }

var allMods = []Module{idModule("container"), idModule("ci"), idModule("infra"), idModule("deploy")}

// fakeBin creates an executable named name in dir.
func fakeBin(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// preflightSetup installs a fake provider and runner and a PATH with only the
// given tools.
func preflightSetup(t *testing.T, prov preflightProvider, bins ...string) *FakeRunner {
	t.Helper()
	called := false
	if prov.authCalled == nil {
		prov.authCalled = &called
	}
	t.Cleanup(cloud.Register(prov))
	fr := &FakeRunner{}
	old := PreflightRunner
	PreflightRunner = fr
	t.Cleanup(func() { PreflightRunner = old })
	dir := t.TempDir()
	for _, b := range bins {
		fakeBin(t, dir, b)
	}
	t.Setenv("PATH", dir)
	return fr
}

func goodConfig() *config.Config {
	return &config.Config{Project: "widget", Cloud: "hetzner", AppPort: 8080}
}

func TestPreflightOK(t *testing.T) {
	called := false
	fr := preflightSetup(t, preflightProvider{authCalled: &called}, "tofu", "ssh-keygen")
	err := runPreflight(&detect.Project{GitHubRepo: "acme/widget"}, goodConfig(), allMods, PreflightOptions{Quota: QuotaBlock})
	if err != nil {
		t.Fatal(err)
	}
	if len(fr.Calls) != 1 || strings.Join(append([]string{fr.Calls[0].Name}, fr.Calls[0].Args...), " ") != "gh auth status" {
		t.Errorf("calls = %v", fr.Calls)
	}
	if !called {
		t.Error("CheckAuth not called")
	}
}

func TestPreflightCollectsEveryProblem(t *testing.T) {
	called := false
	fr := preflightSetup(t, preflightProvider{authCalled: &called, credErr: errors.New("HCLOUD_TOKEN is not set")})
	fr.Errors = map[string]error{"gh auth status": errors.New("exit status 1")}
	c := goodConfig()
	c.AppPort = 0
	err := runPreflight(&detect.Project{}, c, allMods, PreflightOptions{Quota: QuotaBlock})
	if err == nil {
		t.Fatal("no error")
	}
	msg := err.Error()
	for _, want := range []string{
		"deploy needs a GitHub remote (git remote add origin …)",
		"gh is not authenticated",
		"OpenTofu (tofu) not found",
		"ssh-keygen not found",
		"HCLOUD_TOKEN is not set",
		"app_port 0 out of range",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in:\n%s", want, msg)
		}
	}
	if called {
		t.Error("CheckAuth called although credentials are missing")
	}
	if strings.Contains(msg, preflightToken) {
		t.Error("token leaked")
	}
}

func TestPreflightNoRemoteNoTofu(t *testing.T) {
	preflightSetup(t, preflightProvider{}, "ssh-keygen")
	err := runPreflight(&detect.Project{}, goodConfig(), allMods, PreflightOptions{Quota: QuotaBlock})
	if err == nil || !strings.Contains(err.Error(), "deploy needs a GitHub remote") || !strings.Contains(err.Error(), "OpenTofu (tofu) not found") {
		t.Errorf("err = %v", err)
	}
}

func TestPreflightAuthRejected(t *testing.T) {
	preflightSetup(t, preflightProvider{authErr: errors.New("hetzner: authentication check failed: 401")}, "tofu", "ssh-keygen")
	err := runPreflight(&detect.Project{GitHubRepo: "acme/widget"}, goodConfig(), allMods, PreflightOptions{Quota: QuotaBlock})
	if err == nil || !strings.Contains(err.Error(), "authentication check failed") {
		t.Errorf("err = %v", err)
	}
}

func TestPreflightOnlySelectedModules(t *testing.T) {
	fr := preflightSetup(t, preflightProvider{credErr: errors.New("HCLOUD_TOKEN is not set")})
	// container + ci need neither GitHub, tofu nor cloud credentials.
	if err := runPreflight(&detect.Project{}, goodConfig(), allMods[:2], PreflightOptions{Quota: QuotaBlock}); err != nil {
		t.Errorf("err = %v", err)
	}
	if len(fr.Calls) != 0 {
		t.Errorf("calls = %v", fr.Calls)
	}
}

const quotaMsg = "scaleway: quota for STARDUST1-S in fr-par-1 is 0 — pick another size (vauban plan --size …) or request a quota increase at https://console.scaleway.com/organization/quotas"

func TestPreflightQuotaExceeded(t *testing.T) {
	preflightSetup(t, preflightProvider{quota: cloud.QuotaStatus{Reason: quotaMsg}}, "tofu", "ssh-keygen")
	err := runPreflight(&detect.Project{GitHubRepo: "acme/widget"}, goodConfig(), allMods, PreflightOptions{Quota: QuotaBlock})
	if err == nil || err.Error() != quotaMsg {
		t.Errorf("err = %v", err)
	}
}

func TestPreflightQuotaNotCheckedIsOK(t *testing.T) {
	preflightSetup(t, preflightProvider{quota: cloud.QuotaStatus{OK: true, Reason: "not checked: no API"}}, "tofu", "ssh-keygen")
	if err := runPreflight(&detect.Project{GitHubRepo: "acme/widget"}, goodConfig(), allMods, PreflightOptions{Quota: QuotaBlock}); err != nil {
		t.Errorf("err = %v", err)
	}
}

func TestPreflightQuotaErrorIsWarning(t *testing.T) {
	preflightSetup(t, preflightProvider{quotaErr: errors.New("scaleway: list quotas: 500 internal error")}, "tofu", "ssh-keygen")
	n, err := Preflight(&detect.Project{GitHubRepo: "acme/widget"}, goodConfig(), allMods, PreflightOptions{Quota: QuotaBlock})
	if err != nil {
		t.Fatalf("a quota error must not block: %v", err)
	}
	if len(n.Warnings) != 1 || n.Warnings[0] != "quota not checked: scaleway: list quotas: 500 internal error" {
		t.Errorf("warnings = %q", n.Warnings)
	}
}

// Resuming an interrupted apply: an exhausted quota only warns.
func TestPreflightQuotaWarnMode(t *testing.T) {
	preflightSetup(t, preflightProvider{quota: cloud.QuotaStatus{Reason: quotaMsg}}, "tofu", "ssh-keygen")
	n, err := Preflight(&detect.Project{GitHubRepo: "acme/widget"}, goodConfig(), allMods, PreflightOptions{Quota: QuotaWarn})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(n.Warnings) != 1 || !strings.HasPrefix(n.Warnings[0], quotaMsg) {
		t.Errorf("warnings = %q", n.Warnings)
	}
}

func TestPreflightQuotaNotCheckedNote(t *testing.T) {
	replacing := false
	preflightSetup(t, preflightProvider{quota: cloud.QuotaStatus{OK: true, Reason: "not checked: API has no limits"}, gotReplacing: &replacing}, "tofu", "ssh-keygen")
	n, err := Preflight(&detect.Project{GitHubRepo: "acme/widget"}, goodConfig(), allMods, PreflightOptions{Quota: QuotaBlock, Replacing: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(n.Notes) != 1 || n.Notes[0] != "hetzner quota not checked (API has no limits)" || len(n.Warnings) != 0 {
		t.Errorf("notices = %+v", n)
	}
	if !replacing {
		t.Error("replacing not passed to Quota")
	}
}

// Without a new server to create (already applied), quotas are not checked.
func TestPreflightSkipsQuota(t *testing.T) {
	called := false
	preflightSetup(t, preflightProvider{quota: cloud.QuotaStatus{Reason: quotaMsg}, quotaCalled: &called}, "tofu", "ssh-keygen")
	if err := runPreflight(&detect.Project{GitHubRepo: "acme/widget"}, goodConfig(), allMods, PreflightOptions{}); err != nil {
		t.Errorf("err = %v", err)
	}
	if called {
		t.Error("Quota called")
	}
}

// Quota is not checked when the credentials are rejected.
func TestPreflightNoQuotaWithoutAuth(t *testing.T) {
	called := false
	preflightSetup(t, preflightProvider{authErr: errors.New("401"), quotaCalled: &called}, "tofu", "ssh-keygen")
	_ = runPreflight(&detect.Project{GitHubRepo: "acme/widget"}, goodConfig(), allMods, PreflightOptions{Quota: QuotaBlock})
	if called {
		t.Error("Quota called after a failed auth check")
	}
}

func runPreflight(p *detect.Project, c *config.Config, mods []Module, opts PreflightOptions) error {
	_, err := Preflight(p, c, mods, opts)
	return err
}
