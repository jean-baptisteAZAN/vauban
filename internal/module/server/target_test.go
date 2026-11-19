package server

import (
	"strings"
	"testing"

	"github.com/jean-baptisteAZAN/vauban/internal/config"
	"github.com/jean-baptisteAZAN/vauban/internal/engine"
)

func TestSiteStepOutputsFingerprint(t *testing.T) {
	out, err := stepEnv(t, cfg(config.TargetExisting), "server:site", siteIn(), &fakeRunner{})
	if err != nil {
		t.Fatal(err)
	}
	if got := out["server_site"]; got != "shop.example.com|3000" {
		t.Fatalf("server_site = %q", got)
	}
}

func TestAuditOutputsTarget(t *testing.T) {
	r := &fakeRunner{out: readFixture(t, "audit-fresh-ubuntu.txt")}
	out, err := stepEnv(t, cfg(config.TargetExisting), "server:audit", engine.Outputs{}, r)
	if err != nil {
		t.Fatal(err)
	}
	if got := out["server_target"]; got != "ops@203.0.113.10:2222" {
		t.Fatalf("server_target = %q", got)
	}
}

func TestAppliedTarget(t *testing.T) {
	for _, tc := range []struct {
		st   *engine.State
		want string
	}{
		{&engine.State{}, ""},
		{&engine.State{Outputs: engine.Outputs{"server_target": "a@h:22"}}, "a@h:22"},
		// state written before server_target existed
		{&engine.State{Done: map[string]bool{"server:audit": true},
			Outputs: engine.Outputs{"server_user": "ops", "server_ip": "h", "server_port": "2222"}}, "ops@h:2222"},
		{&engine.State{Done: map[string]bool{"server:audit": true},
			Outputs: engine.Outputs{"server_user": "ops", "server_ip": "h"}}, "ops@h:22"},
		// cloud: server_ip without the audit
		{&engine.State{Outputs: engine.Outputs{"server_ip": "h"}}, ""},
	} {
		if got := AppliedTarget(tc.st); got != tc.want {
			t.Errorf("%+v: got %q want %q", tc.st, got, tc.want)
		}
	}
}

func TestParseTarget(t *testing.T) {
	for in, want := range map[string]config.Server{
		"ops@h.example:22":     {User: "ops", Host: "h.example", Port: 22},
		"ops@2001:db8::1:2222": {User: "ops", Host: "2001:db8::1", Port: 2222},
	} {
		got, ok := ParseTarget(in)
		if !ok || got.User != want.User || got.Host != want.Host || got.Port != want.Port {
			t.Errorf("%s: %+v %v", in, got, ok)
		}
	}
	for _, bad := range []string{"", "h:22", "ops@h", "ops@h:x"} {
		if _, ok := ParseTarget(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestCheckTarget(t *testing.T) {
	applied := &engine.State{Done: map[string]bool{"server:audit": true}, Outputs: engine.Outputs{"server_target": "ops@203.0.113.10:2222"}}
	c := cfg(config.TargetExisting)
	if err := CheckTarget(applied, c); err != nil {
		t.Fatalf("same target: %v", err)
	}
	if err := CheckTarget(&engine.State{}, c); err != nil {
		t.Fatalf("nothing applied: %v", err)
	}
	moved := cfg(config.TargetExisting)
	moved.Server.Host = "198.51.100.1"
	err := CheckTarget(applied, moved)
	if err == nil || !strings.Contains(err.Error(), "deployed on 203.0.113.10:2222") || !strings.Contains(err.Error(), "vauban destroy") {
		t.Fatalf("host change: %v", err)
	}
	// the server is its host and port: another ssh user (root ssh turned
	// off, say) or key on the same host is allowed.
	user := cfg(config.TargetExisting)
	user.Server.User, user.Server.Key = "admin", "~/.ssh/other"
	if err := CheckTarget(applied, user); err != nil {
		t.Fatalf("user change: %v", err)
	}
	port := cfg(config.TargetExisting)
	port.Server.Port = 22
	if err := CheckTarget(applied, port); err == nil {
		t.Fatal("port change must be refused")
	}
	cloud := cfg(config.TargetCloud)
	if err := CheckTarget(applied, cloud); err == nil || !strings.Contains(err.Error(), "existing → cloud") {
		t.Fatalf("existing → cloud: %v", err)
	}
	// any server step done counts, even without a recorded target.
	if err := CheckTarget(&engine.State{Done: map[string]bool{"server:docker": true}}, cloud); err == nil {
		t.Fatal("existing → cloud with server:docker done must be refused")
	}
	if err := CheckTarget(&engine.State{Done: map[string]bool{"infra:apply": true}}, cloud); err != nil {
		t.Fatalf("cloud state: %v", err)
	}
}

func TestStaleSteps(t *testing.T) {
	c := cfg(config.TargetExisting)
	done := map[string]bool{"server:site": true}
	if got := StaleSteps(&engine.State{Done: done, Outputs: engine.Outputs{"server_site": "shop.example.com|3000"}}, c); got != nil {
		t.Fatalf("unchanged: %v", got)
	}
	for _, rec := range []string{"old.example.com|3000", "shop.example.com|8080", ""} {
		got := StaleSteps(&engine.State{Done: done, Outputs: engine.Outputs{"server_site": rec}}, c)
		if len(got) != 1 || got[0] != "server:site" {
			t.Errorf("%q: %v", rec, got)
		}
	}
	if got := StaleSteps(&engine.State{Outputs: engine.Outputs{"server_site": "old|1"}}, c); got != nil {
		t.Errorf("site not done: %v", got)
	}
	if got := StaleSteps(&engine.State{Done: done, Outputs: engine.Outputs{"server_site": "old|1"}}, cfg(config.TargetCloud)); got != nil {
		t.Errorf("cloud: %v", got)
	}
}

// The owner marker is written lowercased and compared case-insensitively by
// every script that checks it (GitHub slugs are case-insensitive).
func TestScriptsCompareSlugCaseInsensitively(t *testing.T) {
	lower := `tr '[:upper:]' '[:lower:]'`
	for name, s := range map[string]string{"project": projectScript, "site": siteScript, "destroy": destroyScript} {
		if !strings.Contains(s, lower) {
			t.Errorf("%s script never lowercases the slug", name)
		}
	}
	for _, want := range []string{`slug=$(printf '%s' "$3" | ` + lower + `)`, `[ "$(lc "$cur")" = "$slug" ]`} {
		if !strings.Contains(projectScript, want) {
			t.Errorf("project script lacks %q", want)
		}
	}
	if !strings.Contains(siteScript, `[ "$(lc "$cur")" != "$(lc "$slug")" ]`) {
		t.Error("site script compares the owner case-sensitively")
	}
	if !strings.Contains(destroyScript, `[ "$(lc "$cur")" != "$(lc "$slug")" ]`) {
		t.Error("destroy script compares the owner case-sensitively")
	}
}

// A new ssh user re-reads the facts (user, sudo) with server:audit; the
// other server steps stay done.
func TestStaleStepsUserChange(t *testing.T) {
	c := cfg(config.TargetExisting)
	st := &engine.State{Done: map[string]bool{"server:audit": true, "server:site": true},
		Outputs: engine.Outputs{"server_target": "root@203.0.113.10:2222", "server_site": SiteFingerprint(c)}}
	if got := StaleSteps(st, c); len(got) != 1 || got[0] != "server:audit" {
		t.Fatalf("user change: %v", got)
	}
	st.Outputs["server_target"] = "ops@203.0.113.10:2222"
	if got := StaleSteps(st, c); got != nil {
		t.Fatalf("same user: %v", got)
	}
}
