// Package sbx wraps the Docker Sandbox CLI.
//
// usher never reimplements sandbox management; it only adds routing on top, so
// everything here is a thin exec of the real `sbx` binary.
package sbx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
)

// Run streams a sandbox command straight to the terminal, since these are
// interactive-ish and their progress output is the point.
func Run(args ...string) error {
	cmd := exec.Command("sbx", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sbx %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// Sandbox is the subset of `sbx ls --format json` that usher uses. Unknown
// fields are ignored, so new sbx releases don't break decoding.
type Sandbox struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// List returns the existing sandboxes.
func List() ([]Sandbox, error) {
	cmd := exec.Command("sbx", "ls", "--format", "json")
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("sbx ls: %w: %s", err, strings.TrimSpace(errb.String()))
	}

	var sandboxes []Sandbox
	if err := json.NewDecoder(&out).Decode(&sandboxes); err != nil {
		return nil, fmt.Errorf("decoding sbx ls output: %w", err)
	}
	return sandboxes, nil
}

// Names lists existing sandbox names.
func Names() ([]string, error) {
	sandboxes, err := List()
	if err != nil {
		return nil, err
	}
	names := make([]string, len(sandboxes))
	for i, sb := range sandboxes {
		names[i] = sb.Name
	}
	return names, nil
}

// Exists reports whether a sandbox with exactly this name is present.
func Exists(name string) (bool, error) {
	names, err := Names()
	if err != nil {
		return false, err
	}
	if slices.Contains(names, name) {
		return true, nil
	}
	return false, nil
}

// PublishedHostPort returns the host port sbx already forwards to the given
// sandbox port, or 0 if there is none. sbx allows only one host port per
// sandbox port, so publishing again would fail.
func PublishedHostPort(name string, sandboxPort int) (int, error) {
	cmd := exec.Command("sbx", "inspect", name, "--format", "json")
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("sbx inspect %s: %w: %s", name, err, strings.TrimSpace(errb.String()))
	}

	var info struct {
		// Entries look like "127.0.0.1:36728->3000/tcp4".
		Ports []string `json:"ports"`
	}
	if err := json.NewDecoder(&out).Decode(&info); err != nil {
		return 0, fmt.Errorf("decoding sbx inspect output: %w", err)
	}
	for _, p := range info.Ports {
		host, sandbox, ok := strings.Cut(p, "->")
		if !ok {
			continue
		}
		sandbox, _, _ = strings.Cut(sandbox, "/")
		if sandbox != strconv.Itoa(sandboxPort) {
			continue
		}
		_, port, err := net.SplitHostPort(host)
		if err != nil {
			return 0, fmt.Errorf("unparseable sbx port mapping %q: %w", p, err)
		}
		hostPort, err := strconv.Atoi(port)
		if err != nil {
			return 0, fmt.Errorf("unparseable sbx port mapping %q: %w", p, err)
		}

		return hostPort, nil
	}
	return 0, nil
}
