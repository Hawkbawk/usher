// Package adapter turns a machine name and a port into something Caddy can
// reach.
//
// Everything here runs in the CLI, never in the daemon. The daemon stays a
// generic route table: it stores the adapter name so `usher urls` and `usher down`
// know who owns a route, but it never shells out to sbx, orbctl, or docker.
// That keeps the privileged half small and means a new adapter is a new file
// here and nothing else.
package adapter

import (
	"context"
	"fmt"

	"github.com/hawkbawk/usher/internal/route"
)

// Adapter knows how to make one kind of machine reachable.
//
// The lifecycle is Attach, then Publish once the daemon has stored the route.
// It's split in two because the sbx adapter can't publish a port until the
// daemon has told it which port to publish, and only the daemon can decide
// that.
type Adapter interface {
	// Kind is the adapter name stored on the route.
	Kind() route.Adapter

	// Attach validates that machine exists and can serve port, and returns the
	// route to hand the daemon. A returned route with an empty Upstream and a
	// zero HostPort asks the daemon to allocate a host port.
	Attach(ctx context.Context, machine string, port int) (route.Route, error)

	// Publish acts on the route the daemon actually stored. Only the sbx
	// adapter needs it; for everyone else the upstream was already reachable
	// before Attach returned.
	Publish(ctx context.Context, r route.Route) error

	// Detach undoes Publish. portStillUsed reports whether another route still
	// points at the same machine port, in which case the published port has to
	// stay.
	Detach(ctx context.Context, r route.Route, portStillUsed bool) error

	// Destroy tears down the machine itself, for `usher down --destroy`.
	Destroy(ctx context.Context, machine string) error
}

// For returns the adapter implementing kind.
func For(kind route.Adapter) (Adapter, error) {
	switch kind {
	case route.Sbx:
		return Sbx{}, nil
	case route.Orb:
		return Orb{}, nil
	case route.Docker:
		return Docker{}, nil
	case route.Static:
		return Static{}, nil
	}
	return nil, fmt.Errorf("no adapter for %q", kind)
}

// Lister is implemented by adapters that can enumerate their machines, which
// is what lets the interactive `usher add` offer a picker for them. Adapters
// that don't implement won't appear in the interactive picker.
type Lister interface {
	Adapter

	// Label is the human-readable name shown in the adapter picker.
	Label() string

	// Machines lists the machines that can be routed to.
	Machines(ctx context.Context) ([]string, error)
}

// Listers returns every adapter that supports interactive selection, in the
// order they should be offered.
func Listers() []Lister {
	return []Lister{Sbx{}}
}
