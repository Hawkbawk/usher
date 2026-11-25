package cmd

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hawkbawk/usher/internal/adapter"
	"github.com/hawkbawk/usher/internal/api"
	"github.com/hawkbawk/usher/internal/prompt"
	"github.com/hawkbawk/usher/internal/route"
)

func newDownCmd() *cobra.Command {
	var destroy bool

	cmd := &cobra.Command{
		Use:   "down [<hostname>]",
		Short: "Remove a hostname's route",
		Long: `Remove a hostname's route.

Run it with no arguments to pick the hostnames to remove from a list.

Only that hostname goes away. Other hostnames pointing at the same machine keep
working, and the machine itself is left alone.

Pass --destroy to also tear down the machine and every other route pointing at
it. That works for sbx and docker machines; usher refuses to delete an OrbStack
VM, since that's rarely what you meant.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				if destroy {
					return fmt.Errorf("--destroy needs a hostname")
				}
				return downInteractive(cmd)
			}

			hostname, err := sanitizeHostname(args[0])
			if err != nil {
				return err
			}

			cfg, client, err := load()
			if err != nil {
				return err
			}
			defer client.Close()

			fmt.Printf("Removing https://%s.%s\n", hostname, cfg.Domain)
			r, err := removeRoute(cmd, client, hostname)
			if err != nil {
				return err
			}
			if !destroy {
				return nil
			}
			a, err := adapter.For(r.Adapter)
			if err != nil {
				return err
			}
			return destroyMachine(cmd, client, a, r, cfg.Domain)
		},
	}

	cmd.Flags().BoolVar(&destroy, "destroy", false,
		"also remove the machine and every other route pointing at it")
	return cmd
}

// destroyMachine drops the machine's remaining routes before removing the
// machine, so nothing is left pointing at something that no longer exists.
func destroyMachine(cmd *cobra.Command, client *api.Client, a adapter.Adapter, r route.Route, domain string) error {
	if r.Adapter == route.Static {
		return fmt.Errorf("--destroy has nothing to do for a static route")
	}

	orphans, err := client.RemoveMachine(r.Adapter, r.Machine)
	if err != nil {
		return err
	}
	for _, o := range orphans {
		fmt.Printf("Removing https://%s.%s\n", o.Host, domain)
	}

	fmt.Printf("Destroying %s %s\n", r.Adapter, r.Machine)
	return a.Destroy(cmd.Context(), r.Machine)
}

// machinePortStillRouted reports whether another hostname still points at the
// same machine port, in which case its published host port has to stay.
func machinePortStillRouted(client *api.Client, r route.Route) (bool, error) {
	routes, err := client.List()
	if err != nil {
		return false, err
	}
	for _, other := range routes {
		if other.SameMachine(r) {
			return true, nil
		}
	}
	return false, nil
}

// downInteractive lists every route and removes the ones the user ticks.
func downInteractive(cmd *cobra.Command) error {
	cfg, client, err := load()
	if err != nil {
		return err
	}
	defer client.Close()

	routes, err := client.List()
	if err != nil {
		return err
	}
	if len(routes) == 0 {
		fmt.Println("(nothing routed)")
		return nil
	}

	hosts, err := prompt.SelectRoutes(cmd.Context(), routes, cfg.Domain)
	if err != nil {
		return err
	}
	if len(hosts) == 0 {
		return nil
	}

	for _, h := range hosts {
		fmt.Printf("Removing https://%s.%s\n", h, cfg.Domain)
	}
	removed, err := client.RemoveMany(hosts)
	if err != nil {
		return err
	}
	// Detach after the whole batch is gone, so a host port shared by routes in
	// the same batch is released rather than judged "still used" by a sibling.
	// A failure doesn't stop the rest from being cleaned up.
	var errs []error
	for _, r := range removed {
		if err := detachRoute(cmd, client, r); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", r.Host, err))
		}
	}
	return errors.Join(errs...)
}

// removeRoute drops one route from the daemon and detaches it from its
// machine. It's shared by the argument and interactive forms of `down`.
func removeRoute(cmd *cobra.Command, client *api.Client, hostname string) (route.Route, error) {
	r, existed, err := client.Remove(hostname)
	if err != nil {
		return r, err
	}
	if !existed {
		return r, fmt.Errorf("no route named %q (see `usher urls`)", hostname)
	}

	return r, detachRoute(cmd, client, r)
}

// detachRoute tells the route's adapter it's gone, keeping the published host
// port if another route still shares it.
func detachRoute(cmd *cobra.Command, client *api.Client, r route.Route) error {
	a, err := adapter.For(r.Adapter)
	if err != nil {
		return err
	}
	stillUsed, err := machinePortStillRouted(client, r)
	if err != nil {
		return err
	}
	return a.Detach(cmd.Context(), r, stillUsed)
}
