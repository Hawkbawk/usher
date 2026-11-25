package adapter

import (
	"context"
	"fmt"

	"github.com/hawkbawk/usher/internal/route"
	"github.com/hawkbawk/usher/internal/sbx"
)

// Sbx routes to a Docker Sandbox microVM.
//
// Sandboxes are microVMs, not containers, so they can't share a Docker network
// with the host. The only way in is a published host port, which is why this
// is the one adapter that needs the daemon to pick a port before it can act.
type Sbx struct{}

func (Sbx) Kind() route.Adapter { return route.Sbx }

func (Sbx) Label() string { return "Docker Sandbox (sbx)" }

func (Sbx) Machines(_ context.Context) ([]string, error) { return sbx.Names() }

func (Sbx) Attach(_ context.Context, machine string, port int) (route.Route, error) {
	exists, err := sbx.Exists(machine)
	if err != nil {
		return route.Route{}, err
	}
	if !exists {
		return route.Route{}, fmt.Errorf("no sandbox named %q (see `sbx ls`)", machine)
	}
	// If sbx already forwards this sandbox port, point at that host port.
	// Otherwise leave it zero and the daemon allocates one.
	hostPort, err := sbx.PublishedHostPort(machine, port)
	if err != nil {
		return route.Route{}, err
	}
	return route.Route{
		Adapter:     route.Sbx,
		Machine:     machine,
		MachinePort: port,
		HostPort:    hostPort,
	}, nil
}

func (Sbx) Publish(_ context.Context, r route.Route) error {
	// sbx maps one host port per sandbox port, so skip if it's already forwarded.
	existing, err := sbx.PublishedHostPort(r.Machine, r.MachinePort)
	if err != nil {
		return err
	}
	if existing != 0 {
		return nil
	}
	return sbx.Capture("port", "publish", r.Machine,
		fmt.Sprintf("127.0.0.1:%d:%d", r.HostPort, r.MachinePort))
}

func (Sbx) Detach(_ context.Context, r route.Route, portStillUsed bool) error {
	if portStillUsed {
		// Another hostname points at the same sandbox port and still needs the
		// publish.
		return nil
	}
	return sbx.Capture("port", "unpublish", r.Machine,
		fmt.Sprintf("127.0.0.1:%d:%d", r.HostPort, r.MachinePort))
}

func (Sbx) Destroy(_ context.Context, machine string) error {
	return sbx.Run("rm", "--force", machine)
}
