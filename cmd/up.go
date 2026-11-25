package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hawkbawk/usher/internal/adapter"
	"github.com/hawkbawk/usher/internal/prompt"
	"github.com/hawkbawk/usher/internal/route"
)

func newUpCmd() *cobra.Command {
	var adapterName string

	cmd := &cobra.Command{
		Use:     "up [<machine> <port> <hostname> | <ip>:<port> <hostname>]",
		Aliases: []string{"add"},
		Short:   "Route a hostname to a machine",
		Long: `Route https://<hostname>.<domain> to a port on a machine.

Run it with no arguments to be prompted for everything instead.

The adapter decides what a machine name means and how usher reaches it:

  sbx     a Docker Sandbox microVM, reached through a published host port
  orb     an OrbStack VM, reached by name at <machine>.orb.local
  docker  a running container, reached through a port it already publishes

Give an <ip>:<port> instead of a machine and a port and there's no adapter at
all; the proxy points straight at that address.

usher never creates a machine. The machine has to be running already.

A machine can have as many hostnames as you like, all pointing at it at once.`,
		Example: `  usher up
  usher up canvas-lms 3000 canvas -a orb
  usher up my-sandbox 3000 lti --adapter sbx
  usher up postgres-container 5432 db -a docker
  usher up 10.0.0.5:8080 legacy`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return nil
			}
			return cobra.RangeArgs(2, 3)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				ans, err := prompt.Run(cmd.Context(), adapter.Listers())
				if err != nil {
					return err
				}
				return addRoute(cmd.Context(), ans.Adapter.Kind(), ans.Machine, ans.Port, ans.Hostname)
			}

			kind, machine, port, hostname, err := parseUpArgs(args, adapterName, cmd.Flags().Changed("adapter"))
			if err != nil {
				return err
			}

			return addRoute(cmd.Context(), kind, machine, port, hostname)
		},
	}

	cmd.Flags().StringVarP(&adapterName, "adapter", "a", string(route.Sbx),
		"how to reach the machine: sbx, orb, or docker")
	return cmd
}

// parseUpArgs splits the two accepted forms apart. Two args is the static
// ip:port form; three is a machine plus a port.
func parseUpArgs(args []string, adapterName string, adapterSet bool) (kind route.Adapter, machine string, port int, hostname string, err error) {
	if len(args) == 2 {
		ip, p, ok := adapter.ParseStatic(args[0])
		if !ok {
			return "", "", 0, "", fmt.Errorf("%q is not an <ip>:<port> address; "+
				"to route to a machine give it as `usher up <machine> <port> <hostname>`", args[0])
		}
		if adapterSet {
			return "", "", 0, "", fmt.Errorf("--adapter does not apply to an <ip>:<port> route")
		}
		hostname, err = sanitizeHostname(args[1])
		return route.Static, ip, p, hostname, err
	}

	kind, err = route.ParseAdapter(adapterName)
	if err != nil {
		return "", "", 0, "", err
	}
	port, err = route.ParsePort(args[1])
	if err != nil {
		return "", "", 0, "", err
	}
	hostname, err = sanitizeHostname(args[2])
	return kind, args[0], port, hostname, err
}

// sanitizeHostname collapses user input into the single DNS label the wildcard
// cert covers.
func sanitizeHostname(s string) (string, error) {
	h := route.Sanitize(s)
	if h == "" {
		return "", fmt.Errorf("%q does not reduce to a usable hostname", s)
	}
	return h, nil
}

// addRoute attaches to a machine, reserves the route with the daemon, and
// publishes it. It's shared by `up` and the interactive `add`.
func addRoute(ctx context.Context, kind route.Adapter, machine string, port int, hostname string) error {
	cfg, client, err := load()
	if err != nil {
		return err
	}
	defer client.Close()

	a, err := adapter.For(kind)
	if err != nil {
		return err
	}

	r, err := a.Attach(ctx, machine, port)
	if err != nil {
		return err
	}
	r.Host = hostname

	// Reserve the route first: the sbx adapter can't publish until the
	// daemon has told it which host port to use.
	stored, err := client.Upsert(r)
	if err != nil {
		return err
	}

	if err := a.Publish(ctx, stored); err != nil {
		// Never leave a route pointing at something unreachable.
		if _, _, rmErr := client.Remove(stored.Host); rmErr != nil {
			return fmt.Errorf("%w (and rolling back the route failed: %v)", err, rmErr)
		}
		return err
	}

	fmt.Printf("https://%s.%s -> %s\n", stored.Host, cfg.Domain, stored.Upstream)
	return nil
}
