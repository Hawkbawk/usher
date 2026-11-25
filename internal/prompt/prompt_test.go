package prompt

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/hawkbawk/usher/internal/adapter"
	"github.com/hawkbawk/usher/internal/route"
)

// fakeAdapter satisfies adapter.Lister without touching any real machine.
type fakeAdapter struct {
	kind     route.Adapter
	label    string
	machines []string
	err      error
}

func (f fakeAdapter) Kind() route.Adapter { return f.kind }
func (f fakeAdapter) Label() string       { return f.label }
func (f fakeAdapter) Machines(context.Context) ([]string, error) {
	return f.machines, f.err
}
func (fakeAdapter) Attach(context.Context, string, int) (route.Route, error) {
	return route.Route{}, nil
}
func (fakeAdapter) Publish(context.Context, route.Route) error      { return nil }
func (fakeAdapter) Detach(context.Context, route.Route, bool) error { return nil }
func (fakeAdapter) Destroy(context.Context, string) error           { return nil }

var _ adapter.Lister = fakeAdapter{}

// script feeds answers one line at a time. huh's accessible mode builds a new
// buffered scanner per question, so a plain reader would swallow later lines.
func script(lines ...string) io.Reader {
	return iotest.OneByteReader(strings.NewReader(strings.Join(lines, "\n") + "\n"))
}

func TestRunSingleAdapterSkipsAdapterQuestion(t *testing.T) {
	sbx := fakeAdapter{kind: route.Sbx, label: "Sandbox", machines: []string{"alpha", "beta"}}

	// Machine 2, port, hostname.
	got, err := Run(context.Background(), []adapter.Lister{sbx},
		WithIO(script("2", "3000", "My App"), io.Discard))
	if err != nil {
		t.Fatal(err)
	}

	if got.Adapter.Kind() != route.Sbx {
		t.Errorf("adapter = %q, want sbx", got.Adapter.Kind())
	}
	if got.Machine != "beta" {
		t.Errorf("machine = %q, want beta", got.Machine)
	}
	if got.Port != 3000 {
		t.Errorf("port = %d, want 3000", got.Port)
	}
	if got.Hostname != route.Sanitize("My App") || got.Hostname == "" {
		t.Errorf("hostname = %q, want sanitized label", got.Hostname)
	}
}

func TestRunPicksAmongAdapters(t *testing.T) {
	a := fakeAdapter{kind: route.Sbx, label: "A", machines: []string{"a1"}}
	b := fakeAdapter{kind: route.Orb, label: "B", machines: []string{"b1", "b2"}}

	// Adapter 2 (B), machine 2, port, hostname.
	got, err := Run(context.Background(), []adapter.Lister{a, b},
		WithIO(script("2", "2", "8080", "web"), io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	if got.Adapter.Kind() != route.Orb || got.Machine != "b2" {
		t.Errorf("got %s/%s, want orb/b2", got.Adapter.Kind(), got.Machine)
	}
}

func TestRunRepromptsOnInvalidPort(t *testing.T) {
	a := fakeAdapter{kind: route.Sbx, label: "A", machines: []string{"a1"}}

	var out strings.Builder
	got, err := Run(context.Background(), []adapter.Lister{a},
		WithIO(script("1", "nope", "99999", "4000", "web"), &out))
	if err != nil {
		t.Fatal(err)
	}
	if got.Port != 4000 {
		t.Errorf("port = %d, want 4000", got.Port)
	}
	if !strings.Contains(out.String(), "not a port") {
		t.Errorf("expected a validation message, got:\n%s", out.String())
	}
}

func TestRunNoMachines(t *testing.T) {
	a := fakeAdapter{kind: route.Sbx, label: "Sandbox"}
	_, err := Run(context.Background(), []adapter.Lister{a}, WithIO(script(), io.Discard))
	if err == nil || !strings.Contains(err.Error(), "no Sandbox machines") {
		t.Fatalf("err = %v, want a no-machines error", err)
	}
}

func TestRunListError(t *testing.T) {
	boom := errors.New("boom")
	a := fakeAdapter{kind: route.Sbx, label: "A", err: boom}
	_, err := Run(context.Background(), []adapter.Lister{a}, WithIO(script(), io.Discard))
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
}

func TestRunNoAdapters(t *testing.T) {
	if _, err := Run(context.Background(), nil); err == nil {
		t.Fatal("expected an error with no adapters")
	}
}
