package route

import (
	"path/filepath"
	"strconv"
	"testing"
)

func TestSanitize(t *testing.T) {
	cases := map[string]string{
		"canvas-lti-fix":  "canvas-lti-fix",
		"feature/lti-fix": "feature-lti-fix",
		"Canvas_LMS":      "canvas-lms",
		"--weird--name--": "weird-name",
		"...":             "",
		// Truncation must not leave a trailing dash, which is not a legal
		// hostname label.
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/b": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	for in, want := range cases {
		if got := Sanitize(in); got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseAdapter(t *testing.T) {
	for _, in := range []string{"sbx", "ORB", " docker "} {
		if _, err := ParseAdapter(in); err != nil {
			t.Errorf("ParseAdapter(%q) failed: %v", in, err)
		}
	}
	// "static" is inferred from the ip:port form, never named with --adapter.
	for _, in := range []string{"static", "", "podman"} {
		if _, err := ParseAdapter(in); err == nil {
			t.Errorf("ParseAdapter(%q) should have failed", in)
		}
	}
}

// An orb or static route arrives knowing its own dial target, so the daemon
// must not allocate a host port for it.
func TestUpsertKeepsCallerUpstream(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "routes.json"))
	if err != nil {
		t.Fatal(err)
	}

	got, err := store.Upsert(Route{
		Host:        "canvas",
		Adapter:     Orb,
		Machine:     "canvas-lms",
		MachinePort: 3000,
		Upstream:    "canvas-lms.orb.local:3000",
	}, 30000, 39999)
	if err != nil {
		t.Fatal(err)
	}
	if got.Upstream != "canvas-lms.orb.local:3000" {
		t.Errorf("upstream = %q, want the caller's", got.Upstream)
	}
	if got.HostPort != 0 {
		t.Errorf("host port = %d, want none allocated", got.HostPort)
	}
}

// The docker adapter finds a port the container already publishes, so the
// daemon forms the upstream from it rather than allocating a new one.
func TestUpsertUsesCallerHostPort(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "routes.json"))
	if err != nil {
		t.Fatal(err)
	}

	got, err := store.Upsert(Route{
		Host:        "web",
		Adapter:     Docker,
		Machine:     "nginx",
		MachinePort: 80,
		HostPort:    18080,
	}, 30000, 39999)
	if err != nil {
		t.Fatal(err)
	}
	if got.Upstream != "127.0.0.1:18080" {
		t.Errorf("upstream = %q, want 127.0.0.1:18080", got.Upstream)
	}
}

// A route file has to stay correct across machine restarts, so re-upserting a
// host must never re-roll its host port.
func TestUpsertReusesHostPort(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routes.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}

	first, err := store.Upsert(sbxRoute("canvas", "canvas", 3000), 30000, 39999)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Upsert(sbxRoute("canvas", "canvas", 4000), 30000, 39999)
	if err != nil {
		t.Fatal(err)
	}
	if first.HostPort != second.HostPort {
		t.Errorf("host port changed on re-upsert: %d -> %d", first.HostPort, second.HostPort)
	}
	if second.MachinePort != 4000 {
		t.Errorf("machine port = %d, want 4000", second.MachinePort)
	}
	if second.Upstream != "127.0.0.1:"+strconv.Itoa(second.HostPort) {
		t.Errorf("upstream = %q, does not match host port %d", second.Upstream, second.HostPort)
	}

	// And it has to survive a daemon restart.
	reloaded, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got := reloaded.List()
	if len(got) != 1 || got[0].HostPort != first.HostPort || got[0].Adapter != Sbx {
		t.Errorf("route table did not round-trip: %+v", got)
	}
}

func TestUpsertAvoidsCollision(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "routes.json"))
	if err != nil {
		t.Fatal(err)
	}

	seen := map[int]string{}
	for _, host := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		// A range narrow enough that the host hashes are very likely to
		// collide, so the probe-upward path actually runs. Distinct machines,
		// so port sharing can't paper over a collision.
		r, err := store.Upsert(sbxRoute(host, host, 3000), 30000, 30015)
		if err != nil {
			t.Fatal(err)
		}
		if other, ok := seen[r.HostPort]; ok {
			t.Fatalf("port %d handed to both %q and %q", r.HostPort, other, host)
		}
		seen[r.HostPort] = host
	}
}

// A multi-tenant machine routes several hostnames to the same machine port;
// those routes must share one published host port rather than publishing the
// same port over and over.
func TestUpsertSharesPortAcrossHostsOnSameMachinePort(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "routes.json"))
	if err != nil {
		t.Fatal(err)
	}

	tenantA, err := store.Upsert(sbxRoute("tenant-a", "multi-tenant", 3000), 30000, 39999)
	if err != nil {
		t.Fatal(err)
	}
	tenantB, err := store.Upsert(sbxRoute("tenant-b", "multi-tenant", 3000), 30000, 39999)
	if err != nil {
		t.Fatal(err)
	}
	if tenantA.HostPort != tenantB.HostPort {
		t.Errorf("routes for the same machine port got different host ports: %d vs %d",
			tenantA.HostPort, tenantB.HostPort)
	}

	// A different port on the same machine still needs its own host port.
	other, err := store.Upsert(sbxRoute("tenant-a-admin", "multi-tenant", 3001), 30000, 39999)
	if err != nil {
		t.Fatal(err)
	}
	if other.HostPort == tenantA.HostPort {
		t.Errorf("routes for different machine ports shared a host port: %d", other.HostPort)
	}

	// Same name, different adapter, is a different machine entirely.
	orb, err := store.Upsert(Route{
		Host: "orb-tenant", Adapter: Orb, Machine: "multi-tenant", MachinePort: 3000,
		Upstream: "multi-tenant.orb.local:3000",
	}, 30000, 39999)
	if err != nil {
		t.Fatal(err)
	}
	if orb.HostPort != 0 {
		t.Errorf("orb route borrowed the sbx machine's host port %d", orb.HostPort)
	}
}

func TestRemoveMachineDropsAllItsHosts(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "routes.json"))
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range []Route{
		sbxRoute("tenant-a", "multi-tenant", 3000),
		sbxRoute("tenant-b", "multi-tenant", 3000),
		sbxRoute("other", "other-sandbox", 3000),
	} {
		if _, err := store.Upsert(r, 30000, 39999); err != nil {
			t.Fatal(err)
		}
	}
	// Same machine name under a different adapter must survive.
	if _, err := store.Upsert(Route{
		Host: "orb-tenant", Adapter: Orb, Machine: "multi-tenant", MachinePort: 3000,
		Upstream: "multi-tenant.orb.local:3000",
	}, 30000, 39999); err != nil {
		t.Fatal(err)
	}

	removed, err := store.RemoveMachine(Sbx, "multi-tenant")
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 2 {
		t.Fatalf("removed %d routes, want 2: %+v", len(removed), removed)
	}

	remaining := store.List()
	if len(remaining) != 2 {
		t.Fatalf("route table after RemoveMachine = %+v, want 2 routes", remaining)
	}
	for _, r := range remaining {
		if r.Host != "orb-tenant" && r.Host != "other" {
			t.Errorf("unexpected surviving route %+v", r)
		}
	}
}

func sbxRoute(host, machine string, port int) Route {
	return Route{Host: host, Adapter: Sbx, Machine: machine, MachinePort: port}
}

func TestUpsertManySharesHostPort(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "routes.json"))
	if err != nil {
		t.Fatal(err)
	}

	got, err := store.UpsertMany([]Route{
		{Host: "a", Adapter: Sbx, Machine: "m", MachinePort: 3000},
		{Host: "b", Adapter: Sbx, Machine: "m", MachinePort: 3000},
	}, 30000, 40000)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].HostPort == 0 || got[0].HostPort != got[1].HostPort {
		t.Fatalf("want two routes sharing a host port, got %+v", got)
	}
	if n := len(store.List()); n != 2 {
		t.Errorf("stored %d routes, want 2", n)
	}
}
