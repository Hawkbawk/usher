package adapter

import (
	"context"
	"fmt"
	"net"
	"strconv"

	"github.com/hawkbawk/usher/internal/route"
)

// Static routes straight to an address the user gave us. There is no machine
// behind it, so every verb but Attach is a no-op.
type Static struct{}

func (Static) Kind() route.Adapter { return route.Static }

// Attach for a static route only has a port to work with; ParseStatic has
// already done the real work of validating the address. It exists so `usher up`
// can treat every adapter the same way.
func (Static) Attach(_ context.Context, machine string, port int) (route.Route, error) {
	if machine == "" {
		return route.Route{}, fmt.Errorf("a static route needs an address")
	}
	return route.Route{
		Adapter:     route.Static,
		Machine:     machine,
		MachinePort: port,
		Upstream:    net.JoinHostPort(machine, strconv.Itoa(port)),
	}, nil
}

func (Static) Publish(context.Context, route.Route) error { return nil }

func (Static) Detach(context.Context, route.Route, bool) error { return nil }

func (Static) Destroy(_ context.Context, _ string) error {
	return fmt.Errorf("a static route has no machine to destroy")
}

// ParseStatic recognises the `usher up <ip>:<port> <hostname>` form. It only
// accepts a literal IP: a hostname there would be ambiguous with a machine
// name, and would silently skip every check the adapters do.
func ParseStatic(s string) (ip string, port int, ok bool) {
	h, p, err := net.SplitHostPort(s)
	if err != nil {
		return "", 0, false
	}

	if h == "localhost" {
		h = "127.0.0.1"
	}
	if net.ParseIP(h) == nil {
		return "", 0, false
	}
	n, err := strconv.Atoi(p)
	if err != nil || n < 1 || n > 65535 {
		return "", 0, false
	}
	return h, n, true
}
