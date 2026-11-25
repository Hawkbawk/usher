// Package route holds the route table: which hostname forwards to which
// upstream, which adapter put it there, and how hostnames are derived from
// user input.
package route

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Adapter names the kind of thing a route points at. It decides what the CLI
// has to do to make the upstream reachable; the daemon only stores it so that
// `usher urls` and `usher down` know which adapter to hand the route back to.
type Adapter string

const (
	// Static is a literal ip:port the user supplied. No machine, nothing to
	// attach to, nothing to tear down.
	Static Adapter = "static"
	// Sbx is a Docker Sandbox microVM, reached through a published host port.
	Sbx Adapter = "sbx"
	// Orb is an OrbStack VM, reached by name at <machine>.orb.local.
	Orb Adapter = "orb"
	// Docker is a running container, reached through a port it already
	// publishes.
	Docker Adapter = "docker"
)

// Adapters lists every adapter a user may name, in the order help text should
// show them.
var Adapters = []Adapter{Sbx, Orb, Docker}

// ParseAdapter resolves a user-supplied adapter name. Static is deliberately
// not accepted: it isn't selected with --adapter, it's inferred from the
// ip:port form of `usher up`.
func ParseAdapter(s string) (Adapter, error) {
	for _, a := range Adapters {
		if string(a) == strings.ToLower(strings.TrimSpace(s)) {
			return a, nil
		}
	}
	names := make([]string, len(Adapters))
	for i, a := range Adapters {
		names[i] = string(a)
	}
	return "", fmt.Errorf("unknown adapter %q, want one of: %s", s, strings.Join(names, ", "))
}

// Route is one hostname reachable at https://<Host>.<domain>, forwarding to
// Upstream. A machine can be the target of many routes at once, which is how a
// multi-tenant app that switches on Host gets one hostname per tenant.
type Route struct {
	Host    string  `json:"host"`
	Adapter Adapter `json:"adapter"`
	// Machine is the sandbox, VM, or container name. Empty for static routes.
	Machine string `json:"machine,omitempty"`
	// MachinePort is the port the app listens on inside the machine. For a
	// static route it's the port half of the address the user gave.
	MachinePort int `json:"machinePort"`
	// Upstream is exactly what Caddy dials, e.g. "127.0.0.1:31847",
	// "canvas.orb.local:3000", or "10.0.0.5:8080".
	Upstream string `json:"upstream"`
	// HostPort is non-zero only when a host port is involved. Every route for
	// the same (Adapter, Machine, MachinePort) triple shares one, so that
	// machine port only ever gets published once.
	HostPort int `json:"hostPort,omitempty"`
}

// SameMachine reports whether two routes point at the same port on the same
// machine, and so share a published host port.
func (r Route) SameMachine(other Route) bool {
	return r.Adapter == other.Adapter &&
		r.Machine == other.Machine &&
		r.MachinePort == other.MachinePort
}

var (
	notLabelChar = regexp.MustCompile(`[^a-z0-9-]`)
	dashRun      = regexp.MustCompile(`-{2,}`)
)

// Sanitize flattens arbitrary input into a single DNS label. The wildcard cert
// covers *.<domain> and nothing deeper, and git worktree names routinely
// contain slashes ("feature/lti-fix"), so anything that isn't alphanumeric
// collapses to a dash.
func Sanitize(s string) string {
	s = strings.ToLower(s)
	s = notLabelChar.ReplaceAllString(s, "-")
	s = dashRun.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	return s
}

// Store is the daemon's persisted route table. Every mutation writes the file
// before returning, so a daemon crash can't lose a route the CLI already
// reported as created.
type Store struct {
	path string

	mu     sync.Mutex
	routes map[string]Route // keyed by Host
}

func NewStore(path string) (*Store, error) {
	s := &Store{path: path, routes: map[string]Route{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}

	var list []Route
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	for _, r := range list {
		s.routes[r.Host] = r
	}
	return s, nil
}

// List returns every route sorted by host.
func (s *Store) List() []Route {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listLocked()
}

func (s *Store) listLocked() []Route {
	out := make([]Route, 0, len(s.routes))
	for _, r := range s.routes {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	return out
}

// ForMachine returns every route currently pointing at a machine, sorted by
// host.
func (s *Store) ForMachine(adapter Adapter, machine string) []Route {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]Route, 0)
	for _, r := range s.routes {
		if r.Adapter == adapter && r.Machine == machine {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	return out
}

// Upsert records a route, filling in whatever the caller left for the daemon
// to decide.
//
// The caller decides the upstream when it can: a static or orb route knows its
// own dial target, and the docker adapter knows the host port the container
// already publishes. Only the sbx adapter arrives with neither, because only
// the daemon knows which ports its other routes already hold.
//
// When a host port has to be allocated, it's reused in priority order from:
// another route already publishing this same (adapter, machine, machinePort)
// triple, or this host's own previous route (so a route survives a machine
// restart). Otherwise a fresh port is allocated.
func (s *Store) Upsert(r Route, portMin, portMax int) (Route, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch {
	case r.Upstream != "":
		// Static and orb: the caller already knows the dial target.
	case r.HostPort != 0:
		// Docker: the caller found a port the container already publishes.
		r.Upstream = hostUpstream(r.HostPort)
	default:
		// Sbx: only the daemon can pick a free port.
		port, err := s.allocLocked(r, portMin, portMax)
		if err != nil {
			return Route{}, err
		}
		r.HostPort = port
		r.Upstream = hostUpstream(port)
	}

	s.routes[r.Host] = r
	return r, s.saveLocked()
}

// allocLocked resolves the host port for a route that needs one: a port
// already published for this machine, then this host's own previous port, then
// a fresh one. A fresh port starts at a deterministic offset derived from the
// hostname, then probes upward past anything already claimed by another route
// or already bound on the host.
func (s *Store) allocLocked(r Route, portMin, portMax int) (int, error) {
	taken := map[int]bool{}
	for _, existing := range s.routes {
		if existing.SameMachine(r) && existing.HostPort != 0 {
			return existing.HostPort, nil
		}
		taken[existing.HostPort] = true
	}
	if existing, ok := s.routes[r.Host]; ok && existing.HostPort != 0 {
		return existing.HostPort, nil
	}

	h := fnv.New32a()
	if _, err := h.Write([]byte(r.Host)); err != nil {
		return 0, fmt.Errorf("hash failed: %w", err)
	}
	span := portMax - portMin + 1
	start := portMin + int(h.Sum32()%uint32(span))

	for i := start; i < portMax; i++ {
		if taken[i] || bound(i) {
			continue
		}
		return i, nil
	}
	return 0, fmt.Errorf("no free host port in %d-%d", portMin, portMax)
}

// Remove drops a single host's route. It reports whether the host was
// present.
func (s *Store) Remove(host string) (Route, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.routes[host]
	if !ok {
		return Route{}, false, nil
	}
	delete(s.routes, host)
	return r, true, s.saveLocked()
}

// RemoveMachine drops every route pointing at a machine, e.g. when the machine
// itself is being destroyed. It returns the routes that were removed.
func (s *Store) RemoveMachine(adapter Adapter, machine string) ([]Route, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var removed []Route
	for host, r := range s.routes {
		if r.Adapter == adapter && r.Machine == machine {
			removed = append(removed, r)
			delete(s.routes, host)
		}
	}
	if len(removed) == 0 {
		return nil, nil
	}
	sort.Slice(removed, func(i, j int) bool { return removed[i].Host < removed[j].Host })
	return removed, s.saveLocked()
}

// hostUpstream is the dial target for a port published on the host. Sandboxes
// and containers both publish to loopback, so the daemon reaches them the same
// way.
func hostUpstream(port int) string {
	return net.JoinHostPort("127.0.0.1", fmt.Sprint(port))
}

// bound reports whether something is already listening on the loopback port.
func bound(port int) bool {
	ln, err := net.Listen("tcp", hostUpstream(port))
	if err != nil {
		return true
	}
	_ = ln.Close()
	return false
}

func (s *Store) saveLocked() error {
	data, err := json.MarshalIndent(s.listLocked(), "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}

	// Write-then-rename so a torn write can't leave an unparseable table.
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// ParsePort parses a TCP port number from user input.
func ParsePort(s string) (int, error) {
	p, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || p < 1 || p > 65535 {
		return 0, fmt.Errorf("%q is not a port between 1 and 65535", s)
	}
	return p, nil
}
