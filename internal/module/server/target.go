package server

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/jean-baptisteAZAN/vauban/internal/config"
	"github.com/jean-baptisteAZAN/vauban/internal/engine"
)

const siteStepID = "server:site"

// TargetString is the server a project is applied on, as recorded in the
// server_target output: user@host:port.
func TargetString(user, host string, port int) string {
	return user + "@" + host + ":" + strconv.Itoa(port)
}

// ParseTarget parses a TargetString (the host may be an IPv6 address).
func ParseTarget(s string) (config.Server, bool) {
	user, rest, ok := strings.Cut(s, "@")
	i := strings.LastIndex(rest, ":")
	if !ok || user == "" || i <= 0 {
		return config.Server{}, false
	}
	port, err := strconv.Atoi(rest[i+1:])
	if err != nil || port < 1 || port > 65535 {
		return config.Server{}, false
	}
	return config.Server{User: user, Host: rest[:i], Port: port}, true
}

// AppliedTarget returns the server the state says this project is applied
// on: server_target, else one rebuilt from the audit outputs of an older
// state, else "".
func AppliedTarget(st *engine.State) string {
	if t := st.Outputs["server_target"]; t != "" {
		return t
	}
	user, host := st.Outputs["server_user"], st.Outputs["server_ip"]
	if !st.Done["server:audit"] || user == "" || host == "" {
		return ""
	}
	port, err := strconv.Atoi(st.Outputs["server_port"])
	if err != nil {
		port = 22
	}
	return TargetString(user, host, port)
}

// anyDone reports whether a server step is done in st.
func anyDone(st *engine.State) bool {
	for id, done := range st.Done {
		if done && strings.HasPrefix(id, "server:") {
			return true
		}
	}
	return false
}

// CheckTarget refuses a config that would move an applied project: another
// host, port or user than the recorded target, or back to a cloud target
// while server steps are done. Like infra.CheckCloud, callers check it
// before saving vauban.yaml, so a refused change is never saved.
func CheckTarget(st *engine.State, c *config.Config) error {
	applied := AppliedTarget(st)
	if !c.Existing() {
		if applied == "" && !anyDone(st) {
			return nil
		}
		where := ""
		if applied != "" {
			where = ": this project is deployed on " + applied
		}
		return fmt.Errorf("changing target (existing → cloud) is not supported in place%s; run `vauban destroy` first, then apply", where)
	}
	if applied == "" || c.Server == nil {
		return nil
	}
	if want := TargetString(c.Server.User, c.Server.Host, c.Server.SSHPort()); want != applied {
		return fmt.Errorf("this project is deployed on %s; migrating between servers is not supported (vauban.yaml now says %s): run `vauban destroy` first, then apply", applied, want)
	}
	return nil
}

// SiteFingerprint is what the site file depends on, as recorded in the
// server_site output: domain|app_port.
func SiteFingerprint(c *config.Config) string {
	return c.Domain + "|" + strconv.Itoa(c.AppPort)
}

// StaleSteps returns the server step IDs the caller must clear from st.Done
// before Execute: server:site when the domain or app port no longer match
// the site written (or the state predates the server_site record). The site
// file name does not depend on the domain, so a rewrite replaces the old one.
func StaleSteps(st *engine.State, c *config.Config) []string {
	if !c.Existing() || !st.Done[siteStepID] || st.Outputs["server_site"] == SiteFingerprint(c) {
		return nil
	}
	return []string{siteStepID}
}
