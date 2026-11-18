package cli

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jean-baptisteAZAN/vauban/internal/cloud"
	"github.com/jean-baptisteAZAN/vauban/internal/config"
	"github.com/jean-baptisteAZAN/vauban/internal/engine"
	"github.com/jean-baptisteAZAN/vauban/internal/module/infra"
)

const tofuDestroyCmd = "tofu -chdir=infra destroy -auto-approve -input=false"

// destroyEnv is an applied project: vauban.yaml, a managed main.tf, a local
// tfstate, a state.json and the deploy key.
func destroyEnv(t *testing.T) (string, *engine.FakeRunner) {
	t.Helper()
	root, fr := applyEnv(t)
	writeFile(t, root, config.FileName, "project: shop\ncloud: hetzner\nregion: nbg1\nsize: cx23\napp_port: 8080\n")
	writeFile(t, root, "Dockerfile", "FROM scratch\n")
	writeFile(t, root, "infra/main.tf", infra.Marker+"\n# fingerprint: hetzner/nbg1/cx23\n")
	writeFile(t, root, "infra/terraform.tfstate", "{}\n")
	writeFile(t, root, "infra/.terraform/terraform.tfstate", "{}\n") // tofu init already ran
	home, _ := os.UserHomeDir()
	saveState(t, root, &engine.State{
		Done:    map[string]bool{"file:Dockerfile": true, "infra:main-tf": true, "infra:apply": true},
		Outputs: engine.Outputs{"deploy_key_path": filepath.Join(home, ".ssh", "vauban_shop_ed25519"), "server_ip": testIP},
	})
	return root, fr
}

func statePath(root string) string { return filepath.Join(root, ".vauban", "state.json") }

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestDestroyNonTTYNeedsConfirm(t *testing.T) {
	root, fr := destroyEnv(t)
	for _, args := range [][]string{nil, {"--yes"}, {"--yes", "--force"}} {
		out, err := execVauban(t, context.Background(), append([]string{"destroy", "--dir", root}, args...)...)
		wantExit(t, err, 2, "destroy needs a terminal or --confirm <project>", out)
	}
	if len(fr.Calls) != 0 {
		t.Errorf("calls made: %q", callLines(fr))
	}
	if !exists(statePath(root)) {
		t.Error("state.json removed")
	}
}

func TestDestroyWrongConfirmExit2(t *testing.T) {
	root, fr := destroyEnv(t)
	out, err := execVauban(t, context.Background(), "destroy", "--dir", root, "--confirm", "shopp")
	wantExit(t, err, 2, "", out)
	if len(fr.Calls) != 0 {
		t.Errorf("calls made: %q", callLines(fr))
	}
	if !exists(statePath(root)) {
		t.Error("state.json removed")
	}
}

func TestDestroyConfirmed(t *testing.T) {
	root, fr := destroyEnv(t)
	out, err := execVauban(t, context.Background(), "destroy", "--dir", root, "--confirm", "shop")
	if err != nil {
		t.Fatalf("destroy: %v\n%s", err, out)
	}
	want := []string{
		tofuDestroyCmd,
		"gh secret delete DEPLOY_HOST --repo acme/shop",
		"gh secret delete DEPLOY_SSH_KEY --repo acme/shop",
		"gh secret delete DEPLOY_DOMAIN --repo acme/shop",
	}
	if got := callLines(fr); !slices.Equal(got, want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
	tofu := fr.Calls[0]
	if tofu.Dir != root || !slices.Contains(tofu.Env, "HCLOUD_TOKEN="+planToken) {
		t.Errorf("tofu destroy dir/env = %q %q", tofu.Dir, tofu.Env)
	}
	if exists(statePath(root)) {
		t.Error("state.json not removed")
	}
	home, _ := os.UserHomeDir()
	key := filepath.Join(home, ".ssh", "vauban_shop_ed25519")
	for _, p := range []string{key, filepath.Join(root, "Dockerfile"), filepath.Join(root, "infra", "main.tf"), filepath.Join(root, config.FileName)} {
		if !exists(p) {
			t.Errorf("%s deleted", p)
		}
	}
	for _, s := range []string{"left in place", "Dockerfile", "infra/main.tf", "infra/terraform.tfstate", key, config.FileName} {
		if !strings.Contains(out, s) {
			t.Errorf("missing %q in:\n%s", s, out)
		}
	}
	if strings.Contains(out, planToken) || strings.Contains(out, "PRIVATE KEY") {
		t.Errorf("secret in output:\n%s", out)
	}
}

func TestDestroySecretErrors(t *testing.T) {
	root, fr := destroyEnv(t)
	fr.Errors = map[string]error{
		"gh secret delete DEPLOY_HOST --repo acme/shop":   errors.New("gh: exit status 1: HTTP 404: Not Found"),
		"gh secret delete DEPLOY_DOMAIN --repo acme/shop": errors.New("gh: exit status 1: HTTP 403: Forbidden"),
	}
	out, err := execVauban(t, context.Background(), "destroy", "--dir", root, "--confirm", "shop")
	wantExit(t, err, 1, "", out)
	if !strings.Contains(err.Error(), "DEPLOY_DOMAIN") || strings.Contains(err.Error(), "DEPLOY_HOST") {
		t.Errorf("error = %q, want only DEPLOY_DOMAIN reported", err)
	}
	if exists(statePath(root)) {
		t.Error("state.json not removed")
	}
}

func TestDestroyTofuFailureKeepsState(t *testing.T) {
	root, fr := destroyEnv(t)
	fr.Errors = map[string]error{tofuDestroyCmd: errors.New("tofu: exit status 1")}
	out, err := execVauban(t, context.Background(), "destroy", "--dir", root, "--confirm", "shop")
	if err == nil {
		t.Fatalf("want error\n%s", out)
	}
	if len(fr.Calls) != 1 {
		t.Errorf("ran after tofu failure: %q", callLines(fr))
	}
	if !exists(statePath(root)) {
		t.Error("state.json removed after failed destroy")
	}
}

func TestDestroySkipsUnmanagedTerraform(t *testing.T) {
	root, fr := destroyEnv(t)
	writeFile(t, root, "infra/main.tf", "terraform {}\n")
	out, err := execVauban(t, context.Background(), "destroy", "--dir", root, "--confirm", "shop")
	if err != nil {
		t.Fatalf("destroy: %v\n%s", err, out)
	}
	if slices.Contains(callLines(fr), tofuDestroyCmd) {
		t.Errorf("tofu destroy ran on unmanaged terraform")
	}
}

func TestDestroyWithoutConfig(t *testing.T) {
	root, fr := applyEnv(t)
	out, err := execVauban(t, context.Background(), "destroy", "--dir", root, "--confirm", "shop")
	if err == nil || !strings.Contains(err.Error(), config.FileName) {
		t.Fatalf("want missing vauban.yaml error, got %v\n%s", err, out)
	}
	if len(fr.Calls) != 0 {
		t.Errorf("calls made: %q", callLines(fr))
	}
	if _, err := os.Stat(filepath.Join(root, config.FileName)); !errors.Is(err, fs.ErrNotExist) {
		t.Error("vauban.yaml created")
	}
}

func TestDestroyPreflightMissingGh(t *testing.T) {
	root, fr := destroyEnv(t)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "tofu"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	out, err := execVauban(t, context.Background(), "destroy", "--dir", root, "--confirm", "shop")
	wantExit(t, err, 1, "", out)
	if !strings.Contains(out, "gh not found") {
		t.Errorf("missing gh problem in:\n%s", out)
	}
	if len(fr.Calls) != 0 || !exists(statePath(root)) {
		t.Errorf("destroy acted despite failed preflight: %q", callLines(fr))
	}
}

func TestDestroyPreflightGhNotAuthenticated(t *testing.T) {
	root, fr := destroyEnv(t)
	old := engine.PreflightRunner
	engine.PreflightRunner = &engine.FakeRunner{Errors: map[string]error{"gh auth": errors.New("not logged in")}}
	t.Cleanup(func() { engine.PreflightRunner = old })
	out, err := execVauban(t, context.Background(), "destroy", "--dir", root, "--confirm", "shop")
	wantExit(t, err, 1, "", out)
	if !strings.Contains(out, "gh is not authenticated") || len(fr.Calls) != 0 {
		t.Errorf("calls %q, out:\n%s", callLines(fr), out)
	}
}

func TestDestroyRepoAccessFailureExit1(t *testing.T) {
	root, fr := destroyEnv(t)
	pre := &engine.FakeRunner{Errors: map[string]error{
		"gh api repos/acme/shop --silent": errors.New("gh: exit status 1: HTTP 404: Not Found"),
	}}
	old := engine.PreflightRunner
	engine.PreflightRunner = pre
	t.Cleanup(func() { engine.PreflightRunner = old })
	out, err := execVauban(t, context.Background(), "destroy", "--dir", root, "--confirm", "shop")
	wantExit(t, err, 1, "", out)
	if !strings.Contains(out, "cannot access GitHub repo acme/shop with the current gh login") {
		t.Errorf("missing repo access problem in:\n%s", out)
	}
	if len(fr.Calls) != 0 || !exists(statePath(root)) {
		t.Errorf("destroy acted despite failed repo access: %q", callLines(fr))
	}
	if strings.Contains(out, "destroy will:") {
		t.Errorf("preflight must fail before the confirmation step:\n%s", out)
	}
}

func TestDestroySecretNotSetIsSuccess(t *testing.T) {
	root, fr := destroyEnv(t)
	fr.Errors = map[string]error{
		"gh secret delete DEPLOY_SSH_KEY --repo acme/shop": errors.New("gh: exit status 1: HTTP 404: Not Found (https://api.github.com/repos/acme/shop/actions/secrets/DEPLOY_SSH_KEY)"),
	}
	out, err := execVauban(t, context.Background(), "destroy", "--dir", root, "--confirm", "shop")
	if err != nil {
		t.Fatalf("destroy: %v\n%s", err, out)
	}
	if !strings.Contains(out, "[skip] gh secret delete DEPLOY_SSH_KEY --repo acme/shop (not set)") {
		t.Errorf("missing (not set) line in:\n%s", out)
	}
	if exists(statePath(root)) {
		t.Error("state.json not removed")
	}
}

// A signal while deleting secrets keeps state and exits 130.
func TestDestroyInterruptedDuringSecrets(t *testing.T) {
	root, fr := destroyEnv(t)
	sigs := injectSignals(t)
	setRunner(t, signalRunner{FakeRunner: fr, on: "gh secret delete DEPLOY_DOMAIN --repo acme/shop", sigs: sigs})
	out, err := execVauban(t, context.Background(), "destroy", "--dir", root, "--confirm", "shop")
	wantExit(t, err, 130, "interrupted — state kept; rerun `vauban destroy` to finish", out)
	if !exists(statePath(root)) {
		t.Error("state.json removed after interrupt")
	}
}

// A fresh checkout has no infra/.terraform: destroy runs tofu init first.
func TestDestroyInitsWhenTerraformDirMissing(t *testing.T) {
	root, fr := destroyEnv(t)
	if err := os.RemoveAll(filepath.Join(root, "infra", ".terraform")); err != nil {
		t.Fatal(err)
	}
	out, err := execVauban(t, context.Background(), "destroy", "--dir", root, "--confirm", "shop")
	if err != nil {
		t.Fatalf("destroy: %v\n%s", err, out)
	}
	const initCmd = "tofu -chdir=infra init -input=false"
	calls := callLines(fr)
	if len(calls) < 2 || calls[0] != initCmd || calls[1] != tofuDestroyCmd {
		t.Fatalf("calls = %q, want init then destroy", calls)
	}
	if init := fr.Calls[0]; init.Dir != root || !slices.Contains(init.Env, "HCLOUD_TOKEN="+planToken) {
		t.Errorf("tofu init dir/env = %q %q", init.Dir, init.Env)
	}
	if !strings.Contains(out, "  - $ "+initCmd+"\n") {
		t.Errorf("init not announced before confirmation:\n%s", out)
	}
	if strings.Contains(out, planToken) {
		t.Errorf("token in output:\n%s", out)
	}
}

// destroy uses the cloud the infra was applied on, even if vauban.yaml now
// names another one.
func TestDestroyUsesAppliedCloud(t *testing.T) {
	root, fr := destroyEnv(t)
	t.Cleanup(cloud.Register(&doProvider{}))
	writeFile(t, root, config.FileName, "project: shop\ncloud: digitalocean\nregion: fra1\nsize: s-1vcpu-1gb\napp_port: 8080\n")
	st, _ := engine.LoadState(root)
	st.Outputs["infra_fingerprint"] = "hetzner/nbg1/cx23"
	saveState(t, root, st)
	writeFile(t, root, "infra/main.tf", infra.Marker+"\n# fingerprint: digitalocean/fra1/s-1vcpu-1gb\n") // state wins
	out, err := execVauban(t, context.Background(), "destroy", "--dir", root, "--confirm", "shop")
	if err != nil {
		t.Fatalf("destroy: %v\n%s", err, out)
	}
	if !slices.Contains(fr.Calls[0].Env, "HCLOUD_TOKEN="+planToken) || !strings.Contains(out, "deletes the hetzner server") {
		t.Errorf("destroy env %q, output:\n%s", fr.Calls[0].Env, out)
	}
}

// Without an applied fingerprint, the main.tf fingerprint names the cloud,
// and without either, vauban.yaml does.
func TestDestroyCloudFallbacks(t *testing.T) {
	root, fr := destroyEnv(t) // main.tf: hetzner, no infra_fingerprint
	t.Cleanup(cloud.Register(&doProvider{}))
	writeFile(t, root, config.FileName, "project: shop\ncloud: digitalocean\nregion: fra1\nsize: s-1vcpu-1gb\napp_port: 8080\n")
	if out, err := execVauban(t, context.Background(), "destroy", "--dir", root, "--confirm", "shop"); err != nil {
		t.Fatalf("destroy: %v\n%s", err, out)
	}
	if !slices.Contains(fr.Calls[0].Env, "HCLOUD_TOKEN="+planToken) {
		t.Errorf("main.tf cloud not used: %q", fr.Calls[0].Env)
	}

	root, fr = destroyEnv(t)
	t.Cleanup(cloud.Register(&doProvider{}))
	writeFile(t, root, config.FileName, "project: shop\ncloud: digitalocean\nregion: fra1\nsize: s-1vcpu-1gb\napp_port: 8080\n")
	writeFile(t, root, "infra/main.tf", infra.Marker+"\n")
	if out, err := execVauban(t, context.Background(), "destroy", "--dir", root, "--confirm", "shop"); err != nil {
		t.Fatalf("destroy: %v\n%s", err, out)
	}
	if !slices.Contains(fr.Calls[0].Env, "DIGITALOCEAN_TOKEN="+doToken) {
		t.Errorf("vauban.yaml cloud not used: %q", fr.Calls[0].Env)
	}
}
