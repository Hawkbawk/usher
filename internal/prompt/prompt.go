// Package prompt runs the interactive flow behind `usher up` with no
// arguments: it asks which kind of machine, which machine, a port, and a
// hostname, and returns the answers for the caller to act on.
//
// It never touches the daemon or an adapter's Attach/Publish. It only asks
// adapters to list their machines, so the caller decides what to do with the
// answers.
package prompt

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/charmbracelet/huh"

	"github.com/hawkbawk/usher/internal/adapter"
	"github.com/hawkbawk/usher/internal/route"
)

// ErrCancelled is returned when the user backs out of the form.
var ErrCancelled = errors.New("cancelled")

// Answers is what the user chose.
type Answers struct {
	Adapter  adapter.Lister
	Machine  string
	Port     int
	Hostname string // already sanitized to a single DNS label
}

type config struct {
	in  io.Reader
	out io.Writer
}

// Option tweaks how the forms run.
type Option func(*config)

// WithIO runs the forms in huh's line-based accessible mode against the given
// streams instead of taking over the terminal. It exists so tests can script
// answers.
func WithIO(in io.Reader, out io.Writer) Option {
	return func(c *config) { c.in, c.out = in, out }
}

// Run asks the questions. adapters are the kinds of machine the user can pick
// from; with only one, that question is skipped.
func Run(ctx context.Context, adapters []adapter.Lister, opts ...Option) (Answers, error) {
	var cfg config
	for _, o := range opts {
		o(&cfg)
	}
	if len(adapters) == 0 {
		return Answers{}, errors.New("no adapters support interactive mode")
	}

	var ans Answers

	if len(adapters) == 1 {
		ans.Adapter = adapters[0]
	} else {
		var kind route.Adapter
		if err := cfg.run(ctx, adapterForm(adapters, &kind)); err != nil {
			return Answers{}, err
		}
		for _, a := range adapters {
			if a.Kind() == kind {
				ans.Adapter = a
			}
		}
	}

	machines, err := ans.Adapter.Machines(ctx)
	if err != nil {
		return Answers{}, err
	}
	if len(machines) == 0 {
		return Answers{}, fmt.Errorf("no %s machines found", ans.Adapter.Label())
	}

	var portText, hostText string
	if err := cfg.run(ctx, routeForm(machines, &ans.Machine, &portText, &hostText)); err != nil {
		return Answers{}, err
	}

	// The validators already accepted these, so errors here mean a bug.
	if ans.Port, err = route.ParsePort(portText); err != nil {
		return Answers{}, err
	}
	ans.Hostname = route.Sanitize(hostText)
	return ans, nil
}

func (c config) run(ctx context.Context, f *huh.Form) error {
	if c.in != nil {
		f = f.WithAccessible(true).WithInput(c.in).WithOutput(c.out)
	}
	if err := f.RunWithContext(ctx); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return ErrCancelled
		}
		return err
	}
	return nil
}

// adapterForm keys its options on the adapter kind rather than the adapter
// itself: huh compares option values with ==, and an adapter isn't guaranteed
// to be comparable.
func adapterForm(adapters []adapter.Lister, out *route.Adapter) *huh.Form {
	opts := make([]huh.Option[route.Adapter], len(adapters))
	for i, a := range adapters {
		opts[i] = huh.NewOption(a.Label(), a.Kind())
	}
	return huh.NewForm(huh.NewGroup(
		huh.NewSelect[route.Adapter]().
			Title("What kind of machine?").
			Options(opts...).
			Value(out),
	))
}

func routeForm(machines []string, machine, port, host *string) *huh.Form {
	return huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title("Which machine?").
			Options(huh.NewOptions(machines...)...).
			Value(machine),
		huh.NewInput().
			Title("Port inside the machine").
			Value(port).
			Validate(func(s string) error {
				_, err := route.ParsePort(s)
				return err
			}),
		huh.NewInput().
			Title("Hostname").
			Description("Becomes https://<hostname>.<domain>").
			Value(host).
			Validate(func(s string) error {
				if route.Sanitize(s) == "" {
					return fmt.Errorf("%q does not reduce to a usable hostname", s)
				}
				return nil
			}),
	))
}

// SelectRoutes shows every route as its URL and returns the hostnames the user
// ticked. Nothing ticked returns an empty slice.
func SelectRoutes(ctx context.Context, routes []route.Route, domain string, opts ...Option) ([]string, error) {
	var cfg config
	for _, o := range opts {
		o(&cfg)
	}

	options := make([]huh.Option[string], len(routes))
	for i, r := range routes {
		options[i] = huh.NewOption(fmt.Sprintf("https://%s.%s", r.Host, domain), r.Host)
	}

	var hosts []string
	form := huh.NewForm(huh.NewGroup(
		huh.NewMultiSelect[string]().
			Title("Which URLs should be removed?").
			Description("space to select, enter to confirm").
			Options(options...).
			Value(&hosts),
	))
	if err := cfg.run(ctx, form); err != nil {
		return nil, err
	}
	return hosts, nil
}
