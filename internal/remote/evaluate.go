package remote

import (
	"fmt"
	"strings"
)

// CaddyMode tells how the shared Caddy will be handled.
type CaddyMode int

const (
	CaddyInstall        CaddyMode = iota // nothing on 80/443: install vauban-caddy
	CaddyVaubanReady                     // vauban-caddy already running
	CaddyUserReady                       // user's Caddy satisfies all three conditions
	CaddyUserNeedsSetup                  // user's Caddy needs a one-time manual change
	CaddyBlocked                         // 80/443 held by something that is not Caddy
)

// Check is one row of the audit table.
type Check struct {
	Name, Status, Detail string // Status: ok|missing|warn|blocked
}

// Audit is the evaluated report.
type Audit struct {
	Facts          Facts
	Checks         []Check
	Caddy          CaddyMode
	SitesDir       string // host dir holding <project>.caddy files, "" until Caddy is ready or installed
	CaddyContainer string

	blockers []string
}

const vaubanSitesDir = "/opt/vauban/caddy/sites"

// Blockers lists the reasons apply must stop before changing anything.
func (a *Audit) Blockers() []string { return a.blockers }

func (a *Audit) add(name, status, detail string) {
	a.Checks = append(a.Checks, Check{name, status, detail})
}

func (a *Audit) block(format string, args ...any) {
	a.blockers = append(a.blockers, fmt.Sprintf(format, args...))
}

func debianLike(f Facts) bool {
	return f.OSID == "debian" || f.OSID == "ubuntu" || strings.Contains(" "+f.OSLike+" ", " debian ")
}

// Evaluate turns facts into checks, blockers and the Caddy mode for repo.
func Evaluate(f Facts, repo string) *Audit {
	a := &Audit{Facts: f}
	canInstall := f.Sudo && debianLike(f)
	why := func() string {
		switch {
		case !f.Sudo:
			return "no passwordless sudo"
		default:
			return "OS is not Debian/Ubuntu (" + orUnknown(f.OSPretty) + ")"
		}
	}
	missing := func(name, what string) {
		if canInstall {
			a.add(name, "missing", "will be installed")
			return
		}
		a.add(name, "blocked", what+" missing and "+why())
		a.block("%s missing and %s; install it by hand", what, why())
	}

	if debianLike(f) {
		a.add("OS", "ok", orUnknown(f.OSPretty))
	} else {
		a.add("OS", "warn", orUnknown(f.OSPretty)+": vauban installs only on Debian/Ubuntu")
	}
	if f.Sudo {
		a.add("sudo", "ok", "passwordless sudo")
	} else {
		a.add("sudo", "warn", "no passwordless sudo: only usable if everything is already installed")
	}
	user := f.User
	if user == "" {
		user = "the ssh user"
	}
	unusable := false
	switch {
	case f.Docker:
		a.add("Docker engine", "ok", "")
	case f.DockerAccess == "denied":
		unusable = true
		a.add("Docker engine", "blocked", "installed but "+user+" cannot use it")
		a.block("Docker is installed but %s cannot use it: add %s to the docker group or allow passwordless sudo", user, user)
	case f.DockerAccess == "unavailable":
		unusable = true
		a.add("Docker engine", "blocked", "installed but not responding")
		a.block("Docker is installed but not responding: is the daemon running?")
	default:
		missing("Docker engine", "Docker")
	}
	switch {
	case f.Compose:
		a.add("Compose plugin", "ok", "")
	case unusable:
		a.add("Compose plugin", "warn", "unknown until Docker is usable")
	default:
		missing("Compose plugin", "Docker Compose plugin")
	}
	if f.DeployUser {
		a.add("Deploy user", "ok", "deploy")
	} else {
		missing("Deploy user", "deploy user")
	}

	a.evalCaddy()

	switch f.Firewall {
	case "active-closed":
		a.add("Firewall", "warn", "ufw active without ports 80/443 allowed; run: sudo ufw allow 80,443/tcp")
	case "active-open":
		a.add("Firewall", "ok", "ufw active, 80/443 allowed")
	case "inactive":
		a.add("Firewall", "ok", "ufw inactive")
	case "unknown":
		a.add("Firewall", "ok", "status not readable without root")
	default:
		a.add("Firewall", "ok", "none detected")
	}
	switch f.SSHPasswordAuth {
	case "yes":
		a.add("SSH password auth", "warn", "enabled; consider disabling it")
	case "no":
		a.add("SSH password auth", "ok", "disabled")
	default:
		a.add("SSH password auth", "ok", "unknown (needs sudo)")
	}
	if f.DiskFreeGB < 5 {
		a.add("Disk space", "warn", fmt.Sprintf("%d GB free on /, under 5 GB", f.DiskFreeGB))
	} else {
		a.add("Disk space", "ok", fmt.Sprintf("%d GB free", f.DiskFreeGB))
	}
	if len(f.Projects) == 0 {
		a.add("Other projects", "ok", "none")
	} else {
		a.add("Other projects", "ok", strings.Join(f.Projects, ", "))
	}
	switch f.ProjectOwner {
	case "":
		a.add("Project dir", "ok", "apps/"+f.Project+" does not exist yet")
	case "?":
		a.add("Project dir", "warn", "cannot read ~deploy/apps; ownership is checked again at apply")
	case "-":
		a.add("Project dir", "blocked", "apps/"+f.Project+" exists without a vauban owner marker")
		a.block("apps/%s exists without a vauban owner marker", f.Project)
	default:
		if f.ProjectOwner == repo {
			a.add("Project dir", "ok", "apps/"+f.Project+" owned by "+repo)
			break
		}
		a.add("Project dir", "blocked", "apps/"+f.Project+" belongs to "+f.ProjectOwner)
		a.block("apps/%s belongs to %s", f.Project, f.ProjectOwner)
	}
	return a
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func (a *Audit) evalCaddy() {
	f := a.Facts
	c := f.Caddy
	var foreign []string
	for _, p := range []int{80, 443} {
		o, held := f.Ports[p]
		if !held || (c != nil && (o == c.Container || o == "docker-proxy")) {
			continue
		}
		foreign = append(foreign, fmt.Sprintf("%d held by %s", p, o))
	}
	if len(foreign) > 0 {
		a.Caddy = CaddyBlocked
		d := "port " + strings.Join(foreign, ", port ")
		a.add("Ports 80/443", "blocked", d)
		a.add("Caddy container", "blocked", "no Caddy container using 80/443")
		a.block("%s: stop it or point it at a Caddy container", d)
		return
	}
	if c == nil {
		a.Caddy = CaddyInstall
		a.CaddyContainer, a.SitesDir = "vauban-caddy", vaubanSitesDir
		a.add("Ports 80/443", "ok", "free")
		if !f.Sudo {
			a.add("Caddy container", "blocked", "installing Caddy needs passwordless sudo")
			a.block("installing Caddy needs passwordless sudo")
			return
		}
		a.add("Caddy container", "missing", "vauban-caddy will be installed")
		return
	}
	a.CaddyContainer = c.Container
	a.add("Ports 80/443", "ok", "held by "+c.Container)
	switch {
	case c.VaubanManaged:
		a.Caddy, a.SitesDir = CaddyVaubanReady, vaubanSitesDir
		if c.SitesHostDir != "" {
			a.SitesDir = c.SitesHostDir
		}
		a.add("Caddy container", "ok", "vauban-caddy")
	case c.sitesReady() && c.HasImport && c.OnNetwork:
		a.Caddy, a.SitesDir = CaddyUserReady, c.SitesHostDir
		a.add("Caddy container", "ok", "user caddy "+c.Container+", sites dir "+c.SitesHostDir)
	default:
		a.Caddy = CaddyUserNeedsSetup
		d := "user caddy " + c.Container + ": needs a one-time change"
		if c.CaddyfileHostPath == "" {
			d = "user caddy " + c.Container + ": not editable (Caddyfile not on a host bind mount)"
		}
		a.add("Caddy container", "warn", d)
	}
}

func (c *CaddyInfo) sitesReady() bool {
	return c.SitesHostDir != "" && (!c.DirBind || c.SitesDirExists)
}

// CaddyGuide prints the one-time change to make to a user-managed Caddy (only
// the steps that are still needed), or "" when no guide is needed.
func CaddyGuide(a *Audit) string {
	if a.Caddy != CaddyUserNeedsSetup || a.Facts.Caddy == nil {
		return ""
	}
	c := a.Facts.Caddy
	dir := ""
	if i := strings.LastIndex(c.CaddyfileHostPath, "/"); i >= 0 {
		dir = c.CaddyfileHostPath[:i+1]
	}
	sites := c.SitesHostDir
	if sites == "" {
		sites = dir + "sites"
		if dir == "" {
			sites = "./sites"
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Caddy container %q is not set up for vauban. vauban never edits it; make this one-time change:\n", c.Container)
	n := 0
	step := func(format string, args ...any) {
		n++
		fmt.Fprintf(&b, "\n%d. ", n)
		fmt.Fprintf(&b, format, args...)
	}
	recreate := false
	if c.CaddyfileHostPath == "" {
		recreate = true
		step("The Caddyfile is not on a host bind mount (baked into the image or on a named volume), so it cannot be edited in place.\n" +
			"   Bind-mount a Caddyfile from the host into the container, then add this line to it:\n\n     import /etc/caddy/sites/*.caddy\n")
	} else if !c.HasImport {
		step("Add this line to %s:\n\n     import /etc/caddy/sites/*.caddy\n", c.CaddyfileHostPath)
	}
	if !c.sitesReady() {
		if c.SitesHostDir == "" {
			recreate = true
			step("Mount a sites directory in the compose file of that container:\n\n     volumes:\n       - %s:/etc/caddy/sites:ro\n", sites)
		}
		step("Create the directory so vauban can write site files:\n\n     mkdir -p %s && sudo chown deploy %s\n", sites, sites)
	}
	if !c.OnNetwork {
		recreate = true
		step("Join the vauban network (vauban creates it itself). In the same compose file, on the service:\n\n     networks: [vauban]\n\n" +
			"   and at the top level:\n\n     networks:\n       vauban:\n         external: true\n")
	}
	if recreate {
		step("Recreate the container: docker compose up -d\n")
	}
	step("Run: vauban apply\n")
	return b.String()
}
