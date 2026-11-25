// Package api is the client half of the wire between the usher CLI and the usher
// daemon.
//
// The daemon owns the route table and the Caddy config; the CLI only asks it
// for changes. That is why `usher up` needs no sudo and why there is no shared
// directory of config fragments for the two halves to disagree about.
//
// The schema lives in proto/usher/v1/usher.proto; this package wraps the generated
// stubs so callers keep working in route.Route rather than protobuf types.
package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/hawkbawk/usher/internal/api/usherv1"
	"github.com/hawkbawk/usher/internal/route"
)

// callTimeout bounds a single RPC. An upsert reloads Caddy, which can take a
// moment on first run when the wildcard cert still has to be issued.
const callTimeout = 60 * time.Second

// Client talks to the daemon over its unix socket.
type Client struct {
	conn   *grpc.ClientConn
	routes usherv1.RouteServiceClient
}

// NewClient dials lazily: grpc.NewClient does not connect until the first RPC,
// so a missing daemon surfaces as a call error rather than a construction one.
func NewClient(socketPath string) (*Client, error) {
	conn, err := grpc.NewClient("unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("dialing the usher daemon socket %s: %w", socketPath, err)
	}
	return &Client{conn: conn, routes: usherv1.NewRouteServiceClient(conn)}, nil
}

func (c *Client) Close() error { return c.conn.Close() }

func (c *Client) List() ([]route.Route, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()

	resp, err := c.routes.ListRoutes(ctx, &usherv1.ListRoutesRequest{})
	if err != nil {
		return nil, callErr(err)
	}
	out := make([]route.Route, 0, len(resp.GetRoutes()))
	for _, r := range resp.GetRoutes() {
		out = append(out, FromProto(r))
	}
	return out, nil
}

// Upsert asks the daemon to record r. Leave r.Upstream and r.HostPort unset to
// have the daemon allocate a host port; the route it returns is the one that
// was actually stored.
func (c *Client) Upsert(r route.Route) (route.Route, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()

	resp, err := c.routes.UpsertRoute(ctx, &usherv1.UpsertRouteRequest{
		Host:        r.Host,
		Adapter:     adapterToProto(r.Adapter),
		Machine:     r.Machine,
		MachinePort: uint32(r.MachinePort),
		Upstream:    r.Upstream,
		HostPort:    uint32(r.HostPort),
	})
	if err != nil {
		return route.Route{}, callErr(err)
	}
	return FromProto(resp.GetRoute()), nil
}

// UpsertMany is Upsert for several routes in one call, with one Caddy reload.
// The batch is all-or-nothing, and the stored routes come back in request order.
func (c *Client) UpsertMany(rs []route.Route) ([]route.Route, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()

	reqs := make([]*usherv1.UpsertRouteRequest, 0, len(rs))
	for _, r := range rs {
		reqs = append(reqs, &usherv1.UpsertRouteRequest{
			Host:        r.Host,
			Adapter:     adapterToProto(r.Adapter),
			Machine:     r.Machine,
			MachinePort: uint32(r.MachinePort),
			Upstream:    r.Upstream,
			HostPort:    uint32(r.HostPort),
		})
	}
	resp, err := c.routes.UpsertRoutes(ctx, &usherv1.UpsertRoutesRequest{Routes: reqs})
	if err != nil {
		return nil, callErr(err)
	}
	out := make([]route.Route, 0, len(resp.GetRoutes()))
	for _, r := range resp.GetRoutes() {
		out = append(out, FromProto(r))
	}
	return out, nil
}

// Remove removes host's route, if it has one, and reports whether it did.
func (c *Client) Remove(host string) (route.Route, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()

	resp, err := c.routes.RemoveRoute(ctx, &usherv1.RemoveRouteRequest{Host: host})
	if err != nil {
		return route.Route{}, false, callErr(err)
	}
	if resp.GetRoute() == nil {
		return route.Route{}, false, nil
	}
	return FromProto(resp.GetRoute()), true, nil
}

// RemoveMany removes several hosts' routes in one call and returns the ones
// that existed.
func (c *Client) RemoveMany(hosts []string) ([]route.Route, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()

	resp, err := c.routes.RemoveRoutes(ctx, &usherv1.RemoveRoutesRequest{Hosts: hosts})
	if err != nil {
		return nil, callErr(err)
	}
	out := make([]route.Route, 0, len(resp.GetRoutes()))
	for _, r := range resp.GetRoutes() {
		out = append(out, FromProto(r))
	}
	return out, nil
}

// RemoveMachine removes every route pointing at a machine, e.g. when the
// machine itself is being destroyed.
func (c *Client) RemoveMachine(adapter route.Adapter, machine string) ([]route.Route, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()

	resp, err := c.routes.RemoveMachineRoutes(ctx, &usherv1.RemoveMachineRoutesRequest{
		Adapter: adapterToProto(adapter),
		Machine: machine,
	})
	if err != nil {
		return nil, callErr(err)
	}
	out := make([]route.Route, 0, len(resp.GetRoutes()))
	for _, r := range resp.GetRoutes() {
		out = append(out, FromProto(r))
	}
	return out, nil
}

// ToProto and FromProto keep route.Route as the type the rest of usher passes
// around. The store persists it as JSON, so making the generated type the
// storage type would tie the on-disk format to the wire format.
func ToProto(r route.Route) *usherv1.Route {
	return &usherv1.Route{
		Host:        r.Host,
		Adapter:     adapterToProto(r.Adapter),
		Machine:     r.Machine,
		MachinePort: uint32(r.MachinePort),
		Upstream:    r.Upstream,
		HostPort:    uint32(r.HostPort),
	}
}

func FromProto(r *usherv1.Route) route.Route {
	return route.Route{
		Host:        r.GetHost(),
		Adapter:     AdapterFromProto(r.GetAdapter()),
		Machine:     r.GetMachine(),
		MachinePort: int(r.GetMachinePort()),
		Upstream:    r.GetUpstream(),
		HostPort:    int(r.GetHostPort()),
	}
}

// adapterMap is the one place the wire enum and the stored string meet.
// Keeping it a table rather than a pair of switches means a new adapter can't
// be added to one direction and forgotten in the other.
var adapterMap = map[route.Adapter]usherv1.Adapter{
	route.Static: usherv1.Adapter_ADAPTER_STATIC,
	route.Sbx:    usherv1.Adapter_ADAPTER_SBX,
	route.Orb:    usherv1.Adapter_ADAPTER_ORB,
	route.Docker: usherv1.Adapter_ADAPTER_DOCKER,
}

func adapterToProto(a route.Adapter) usherv1.Adapter {
	return adapterMap[a]
}

// AdapterFromProto maps the wire enum back. An unspecified or unknown value
// yields the empty Adapter, which the daemon rejects as InvalidArgument.
func AdapterFromProto(a usherv1.Adapter) route.Adapter {
	for name, wire := range adapterMap {
		if wire == a {
			return name
		}
	}
	return ""
}

// callErr unwraps a gRPC status into something worth printing at a prompt. A
// refused or missing socket means the daemon isn't up, which is by far the
// most common failure here, so name the fix.
func callErr(err error) error {
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	if !ok {
		return err
	}
	if st.Code() == codes.Unavailable {
		// The two install paths use different launchd labels, and there's no
		// way to tell from here which one the user has, so name both.
		return fmt.Errorf("cannot reach the usher daemon: %s\n"+
			"Check: sudo launchctl print system/org.nixos.usher-daemon   (nix-darwin)\n"+
			"   or: sudo launchctl print system/com.hawkbawk.usher.daemon  (usher daemon install)",
			st.Message())
	}
	return errors.New(st.Message())
}
