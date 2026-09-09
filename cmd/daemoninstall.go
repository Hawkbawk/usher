package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/spf13/cobra"

	"github.com/hawkbawk/usher/internal/config"
	"github.com/hawkbawk/usher/internal/netalias"
)

// launchdLabel deliberately differs from nix-darwin's own label
// ("org.nixos.usher-daemon") so the two never fight over the same job if
// someone runs this against a nix-managed host anyway.
const launchdLabel = "com.hawkbawk.usher.daemon"

var plistTemplate = template.Must(template.New("plist").Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>{{.Label}}</string>
	<key>ProgramArguments</key>
	<array>
		<string>{{.ExecPath}}</string>
		<string>daemon</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardOutPath</key>
	<string>{{.LogPath}}</string>
	<key>StandardErrorPath</key>
	<string>{{.ErrLogPath}}</string>
	<key>EnvironmentVariables</key>
	<dict>
		<key>HOME</key>
		<string>/var/root</string>
	</dict>
</dict>
</plist>
`))

func newDaemonInstallCmd() *cobra.Command {
	var (
		domain        string
		tokenFile     string
		acmeEmail     string
		listenAddress net.IP
		dnsPort       int
		stateDir      string
		portMin       int
		portMax       int
	)

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install the daemon component of usher",
		Long: `Install and start the usher daemon in the background.

Creates a configuration file, adds the /etc/resolver/<domain> file, brings up the
loopback alias, and registers a launchd job that starts the daemon at boot.

Must be run as root, since it writes to /etc and /Library/LaunchDaemons.

If you installed usher via nix, use the nix-darwin and home-manager modules instead of this command`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if os.Geteuid() != 0 {
				return fmt.Errorf("this command needs root; re-run with sudo")
			}

			if managed, detail := installedViaNix(); managed {
				return fmt.Errorf("usher looks like it's managed by nix-darwin (%s); use services.usher in your darwin configuration instead of `usher daemon install`", detail)
			}

			if domain == "" || strings.Contains(domain, "CHANGEME") {
				return fmt.Errorf("--domain is required and must be a zone you control in deSEC")
			}
			if tokenFile == "" {
				return fmt.Errorf("--token-file is required")
			}
			if portMin >= portMax {
				return fmt.Errorf("--port-min %d must be below --port-max %d", portMin, portMax)
			}

			cfg := &config.Config{
				Domain:        domain,
				ListenAddress: listenAddress,
				DNSPort:       dnsPort,
				StateDir:      stateDir,
				TokenFile:     tokenFile,
				ACMEEmail:     acmeEmail,
				PortMin:       portMin,
				PortMax:       portMax,
			}

			exe, err := os.Executable()
			if err != nil {
				return fmt.Errorf("locating this binary: %w", err)
			}
			exe, err = filepath.EvalSymlinks(exe)
			if err != nil {
				return fmt.Errorf("resolving this binary's path: %w", err)
			}

			if err := writeConfig(cfg); err != nil {
				return err
			}
			if err := writeStateDirs(cfg); err != nil {
				return err
			}
			if err := writeResolver(cfg); err != nil {
				return err
			}
			if err := netalias.Ensure(cfg.ListenAddress); err != nil {
				return fmt.Errorf("adding loopback alias: %w", err)
			}
			if err := installLaunchdJob(cfg, exe); err != nil {
				return err
			}

			fmt.Printf("usher daemon installed and running (label %s)\n", launchdLabel)
			return nil
		},
	}

	cmd.Flags().StringVar(&domain, "domain", "", "domain sandbox hostnames live under, e.g. username.dedyn.io (required)")
	cmd.Flags().StringVar(&tokenFile, "token-file", "", "file containing the deSEC API token (required)")
	cmd.Flags().StringVar(&acmeEmail, "acme-email", "", "optional ACME account email for certificate expiry notices")
	cmd.Flags().IPVar(&listenAddress, "listen-address", net.IPv4(192, 168, 255, 253), "loopback alias that <label>.<domain> resolves to")
	cmd.Flags().IntVar(&dnsPort, "dns-port", 19353, "port the usher DNS server listens on")
	cmd.Flags().StringVar(&stateDir, "state-dir", "/usr/local/var/usher", "directory holding the route table, ACME storage, and logs")
	cmd.Flags().IntVar(&portMin, "port-min", 30000, "lowest host port usher will publish a sandbox on")
	cmd.Flags().IntVar(&portMax, "port-max", 39999, "highest host port usher will publish a sandbox on")

	return cmd
}

// installedViaNix reports whether usher's config is being supplied by the
// nix-darwin module, which manages /etc/usher/config.json as a symlink into the
// nix store. Running this installer on top of that would fight the module
// for the same files and launchd job.
func installedViaNix() (bool, string) {
	real, err := filepath.EvalSymlinks(config.DefaultPath)
	if err == nil && strings.Contains(real, "/nix/store/") {
		return true, real
	}
	if _, err := os.Stat("/Library/LaunchDaemons/org.nixos.usher-daemon.plist"); err == nil {
		return true, "/Library/LaunchDaemons/org.nixos.usher-daemon.plist"
	}
	return false, ""
}

func writeConfig(cfg *config.Config) error {
	if err := os.MkdirAll(filepath.Dir(config.DefaultPath), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(config.DefaultPath, data, 0o644)
}

func writeStateDirs(cfg *config.Config) error {
	for _, dir := range []string{cfg.StateDir, cfg.CaddyStorageDir(), filepath.Join(cfg.StateDir, "log")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// writeResolver installs /etc/resolver/<domain>, consulted by the host
// resolver and, via the sandbox runtime's DNS proxying, by every sandbox
// microVM too. It's a no-op if the file already has the content we'd write.
func writeResolver(cfg *config.Config) error {
	path := filepath.Join("/etc/resolver", cfg.Domain)
	want := fmt.Sprintf("nameserver 127.0.0.1\nport %d\n", cfg.DNSPort)

	if existing, err := os.ReadFile(path); err == nil && string(existing) == want {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(want), 0o644)
}

// installLaunchdJob writes the launchd plist and (re)loads it. Idempotent: if
// the plist already has the content we'd write and the job is already
// loaded, it does nothing.
func installLaunchdJob(cfg *config.Config, exe string) error {
	plistPath := filepath.Join("/Library/LaunchDaemons", launchdLabel+".plist")

	var buf bytes.Buffer
	if err := plistTemplate.Execute(&buf, struct {
		Label      string
		ExecPath   string
		LogPath    string
		ErrLogPath string
	}{launchdLabel, exe, cfg.LogPath(), cfg.ErrLogPath()}); err != nil {
		return err
	}
	desired := buf.Bytes()

	existing, err := os.ReadFile(plistPath)
	unchanged := err == nil && bytes.Equal(existing, desired)
	loaded := launchctlIsLoaded()

	if unchanged && loaded {
		return nil
	}

	if loaded {
		// Ignore errors: if it's not actually loaded this just no-ops loudly
		// to stderr, which is fine since we're about to (re)load it anyway.
		_ = exec.Command("launchctl", "unload", plistPath).Run()
	}
	if !unchanged {
		if err := os.WriteFile(plistPath, desired, 0o644); err != nil {
			return err
		}
	}

	out, err := exec.Command("launchctl", "load", "-w", plistPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl load: %w: %s", err, out)
	}
	return nil
}

func launchctlIsLoaded() bool {
	return exec.Command("launchctl", "list", launchdLabel).Run() == nil
}
