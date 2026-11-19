package remote

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/jean-baptisteAZAN/vauban/internal/engine"
)

//go:embed audit.sh
var auditScript string

// CaddyInfo describes the Caddy container found on the server.
type CaddyInfo struct {
	Container            string
	VaubanManaged        bool   // container named vauban-caddy
	SitesHostDir         string // host dir bind-mounted at /etc/caddy/sites, "" if none
	CaddyfileHostPath    string // host path of the Caddyfile, "" if not on a host bind
	HasImport, OnNetwork bool
	Networks             []string // Docker networks the container is on, as docker names them
	DirBind              bool     // whole /etc/caddy bound from the host; sites is then <src>/sites
	SitesDirExists       bool     // /etc/caddy/sites exists inside the container
}

// Facts is the parsed output of the audit script.
type Facts struct {
	User, DockerAccess                                      string // ssh user; direct|sudo|denied|unavailable|absent
	OSID, OSLike, OSPretty                                  string
	Sudo, Docker, Compose, DockerGroup, DeployUser, Network bool
	DeployDockerGroup                                       bool           // user deploy exists and is in the docker group
	Ports                                                   map[int]string // port -> owner process or container, absent when free
	Caddy                                                   *CaddyInfo
	Firewall                                                string // none|inactive|active-open|active-closed|unknown
	SSHPasswordAuth                                         string // yes|no|unknown
	DiskFreeGB, PendingUpdates                              int
	Projects                                                []string
	Project                                                 string
	ProjectOwner                                            string // "" dir absent, "-" dir without marker, else repo slug
}

type mount struct {
	Type, Source, Destination string
}

// RunAudit runs the read-only audit on the target and evaluates it for the
// given project and GitHub repo slug.
func RunAudit(ctx context.Context, r engine.Runner, t Target, project, repo string) (*Audit, error) {
	out, err := Exec(ctx, r, t, auditScript, project)
	if err != nil {
		return nil, err
	}
	f, err := ParseFacts(out)
	if err != nil {
		return nil, err
	}
	return Evaluate(f, repo), nil
}

// ParseFacts parses key=value lines. Unknown keys and non key=value lines are
// ignored; it errors only when no recognised key is present.
func ParseFacts(out []byte) (Facts, error) {
	f := Facts{Ports: map[int]string{}}
	var caddyName, mounts, networks string
	var caddyImport, sitesExist, known, done bool
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(strings.TrimRight(line, "\r"), "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		yes := v == "yes"
		switch k {
		case "os_id":
			f.OSID = v
		case "os_like":
			f.OSLike = v
		case "os_pretty":
			f.OSPretty = v
		case "audit_done":
			done = yes || v == "1"
		case "ssh_user":
			f.User = v
		case "docker_access":
			f.DockerAccess = v
		case "caddy_sites_dir":
			sitesExist = yes
		case "sudo":
			f.Sudo = yes
		case "docker":
			f.Docker = yes
		case "compose":
			f.Compose = yes
		case "deploy_docker_group":
			f.DeployDockerGroup = yes
		case "docker_group":
			f.DockerGroup = yes
		case "deploy_user":
			f.DeployUser = yes
		case "network_vauban":
			f.Network = yes
		case "port_80", "port_443":
			if v != "" {
				p, _ := strconv.Atoi(strings.TrimPrefix(k, "port_"))
				f.Ports[p] = v
			}
		case "caddy_container":
			caddyName = v
		case "caddy_mounts":
			mounts = v
		case "caddy_networks":
			networks = v
		case "caddy_import":
			caddyImport = yes
		case "firewall":
			f.Firewall = v
		case "ssh_pwauth":
			f.SSHPasswordAuth = v
		case "disk_free_gb":
			f.DiskFreeGB, _ = strconv.Atoi(v)
		case "pending_updates":
			f.PendingUpdates, _ = strconv.Atoi(v)
		case "projects":
			if v != "" {
				f.Projects = strings.Split(v, ",")
			}
		case "project":
			f.Project = v
		case "project_owner":
			f.ProjectOwner = v
		default:
			continue
		}
		known = true
	}
	if !known {
		return Facts{}, errors.New("audit output has no recognised key")
	}
	if !done {
		return Facts{}, errors.New("audit output truncated")
	}
	if caddyName != "" {
		f.Caddy = caddyInfo(caddyName, mounts, networks, caddyImport)
		f.Caddy.SitesDirExists = sitesExist
	}
	return f, nil
}

func caddyInfo(name, mountsJSON, networks string, hasImport bool) *CaddyInfo {
	c := &CaddyInfo{Container: name, VaubanManaged: name == "vauban-caddy", HasImport: hasImport}
	c.Networks = strings.Fields(networks)
	c.OnNetwork = slices.Contains(c.Networks, "vauban")
	var ms []mount
	if json.Unmarshal([]byte(mountsJSON), &ms) != nil {
		return c
	}
	var sites, file, dir string
	for _, m := range ms {
		if m.Type != "bind" { // named volumes are not editable from the host
			continue
		}
		switch m.Destination {
		case "/etc/caddy/sites":
			sites = m.Source
		case "/etc/caddy/Caddyfile":
			file = m.Source
		case "/etc/caddy":
			dir = strings.TrimRight(m.Source, "/")
		}
	}
	c.SitesHostDir, c.CaddyfileHostPath = sites, file
	if dir != "" {
		if file == "" {
			c.CaddyfileHostPath = dir + "/Caddyfile"
		}
		if sites == "" {
			c.SitesHostDir, c.DirBind = dir+"/sites", true
		}
	}
	return c
}
