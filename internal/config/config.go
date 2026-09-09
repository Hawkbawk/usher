// Package config loads the host-wide usher settings.
//
// The file is written by the nix darwin module so that the daemon and the CLI
// agree on the domain, the loopback alias, and where state lives. Nothing here
// is user-tunable at runtime on purpose: if the daemon and the CLI disagreed
// about the domain you would get routes that resolve nowhere.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
)

// DefaultPath is where the darwin module drops the generated config.
const DefaultPath = "/etc/usher/config.json"

// Config holds the host-wide usher settings. It should never be constructed
// directly; use Load() instead.
type Config struct {
	// Domain that sandbox hostnames live directly under, so a sandbox
	// labelled `canvas-foo` is reachable at `canvas-foo.<domain>`. Must be a
	// zone you control in deSEC, since Caddy writes TXT records under it to
	// get the wildcard cert.
	Domain string `json:"domain"`

	// ListenAddress is the loopback alias that `<label>.<domain>` resolves to
	// and that Caddy binds 443 on.
	ListenAddress net.IP `json:"listenAddress"`

	// DNSPort is not 53, because /etc/resolver files can name a port and
	// binding 53 would fight with everything else on the machine.
	DNSPort int `json:"dnsPort"`

	StateDir  string `json:"stateDir"`
	TokenFile string `json:"tokenFile"`
	ACMEEmail string `json:"acmeEmail"`

	// Host ports are handed out from this range. Derived from the label
	// rather than left ephemeral, so a route survives a sandbox restart.
	PortMin int `json:"portMin"`
	PortMax int `json:"portMax"`
}

func (c *Config) applyDefaults() {
	if c.ListenAddress == nil {
		c.ListenAddress = net.IPv4(192, 168, 255, 253)
	}
	if c.DNSPort == 0 {
		c.DNSPort = 19353
	}
	if c.StateDir == "" {
		c.StateDir = "/usr/local/var/usher"
	}
	if c.PortMin == 0 {
		c.PortMin = 30000
	}
	if c.PortMax == 0 {
		c.PortMax = 39999
	}
}

func (c *Config) validate() error {
	if c.Domain == "" {
		return errors.New("domain is empty")
	}
	if c.PortMin >= c.PortMax {
		return fmt.Errorf("portMin %d must be below portMax %d", c.PortMin, c.PortMax)
	}
	return nil
}

// SocketPath is the unix socket the CLI uses to talk to the daemon. It lives
// under the state dir so a single chown at activation covers it.
func (c *Config) SocketPath() string { return filepath.Join(c.StateDir, "usher.sock") }

// CaddyStorageDir holds ACME account keys and the wildcard cert.
func (c *Config) CaddyStorageDir() string { return filepath.Join(c.StateDir, "caddy") }

// RoutesPath is the daemon's persisted route table.
func (c *Config) RoutesPath() string { return filepath.Join(c.StateDir, "routes.json") }

// LogPath is where launchd redirects the daemon's stdout. Must match the
// StandardOutPath the nix-darwin module and `daemon install` write into the
// launchd plist.
func (c *Config) LogPath() string { return filepath.Join(c.StateDir, "log", "usher.log") }

// ErrLogPath is where launchd redirects the daemon's stderr. Must match the
// StandardErrorPath the nix-darwin module and `daemon install` write into the
// launchd plist.
func (c *Config) ErrLogPath() string { return filepath.Join(c.StateDir, "log", "usher.err.log") }

// Path resolves the config location: $USHER_CONFIG wins, then the system file,
// then a per-user file for people not using the darwin module.
func Path() string {
	if p := os.Getenv("USHER_CONFIG"); p != "" {
		return p
	}
	if _, err := os.Stat(DefaultPath); err == nil {
		return DefaultPath
	}
	if config, err := os.UserConfigDir(); err == nil {
		return filepath.Join(config, "usher", "config.json")
	}
	return DefaultPath
}

// Load reads and validates the config, failing loudly rather than guessing a
// domain when the darwin module isn't enabled on this host.
func Load() (*Config, error) {
	path := Path()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("no usher config at %s\nEnable services.usher in your nix-darwin configuration and rebuild, or set $USHER_CONFIG", path)
		}
		return nil, err
	}

	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}
