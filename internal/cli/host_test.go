package cli

import (
	"strings"
	"testing"
)

func TestHostWarning(t *testing.T) {
	for host, warn := range map[string]bool{
		"203.0.113.7":      false,
		"2001:db8::1":      false,
		"srv.example.com":  false,
		"srv.example.com.": false,
		"prod":             true, // an ~/.ssh/config alias or a LAN name
		"box.local":        true,
		"nas.lan":          true,
		"localhost":        true,
	} {
		got := hostWarning(host)
		if (got != "") != warn {
			t.Errorf("%s: warning %q", host, got)
		}
		if warn && !strings.Contains(got, "GitHub Actions must reach this host by name") {
			t.Errorf("%s: %q", host, got)
		}
	}
}

func TestPlanExistingWarnsShortHost(t *testing.T) {
	root, _ := planEnv(t, "go-app")
	fakeAudit(t, "audit-user-caddy-ready.txt", nil)
	args := append([]string{}, existingFlags...)
	args[3] = "prod"
	out, err := execPlan(t, root, args...)
	if err != nil {
		t.Fatalf("plan: %v\n%s", err, out)
	}
	if !strings.Contains(out, "prod is not an IP address or a public DNS name") {
		t.Errorf("no host warning:\n%s", out)
	}
}
