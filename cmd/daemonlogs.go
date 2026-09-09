package cmd

import (
	"os"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/hawkbawk/usher/internal/config"
)

func newDaemonLogsCmd() *cobra.Command {
	var (
		follow     bool
		stdoutOnly bool
		stderrOnly bool
	)

	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Tail the daemon's log files",
		Long: `Tail the log files launchd redirects the daemon's stdout and stderr into.

With no flags, both files are tailed. Must be run as root.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			var paths []string
			switch {
			case stdoutOnly:
				paths = []string{cfg.LogPath()}
			case stderrOnly:
				paths = []string{cfg.ErrLogPath()}
			default:
				paths = []string{cfg.LogPath(), cfg.ErrLogPath()}
			}

			tailArgs := []string{}
			if follow {
				tailArgs = append(tailArgs, "-f")
			}
			tailArgs = append(tailArgs, paths...)

			tail := exec.Command("tail", tailArgs...)
			tail.Stdin = os.Stdin
			tail.Stdout = os.Stdout
			tail.Stderr = os.Stderr
			return tail.Run()
		},
	}

	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep listening for new log output, like tail -f")
	cmd.Flags().BoolVar(&stdoutOnly, "stdout-only", false, "only show the daemon's stdout log")
	cmd.Flags().BoolVar(&stderrOnly, "stderr-only", false, "only show the daemon's stderr log")
	cmd.MarkFlagsMutuallyExclusive("stdout-only", "stderr-only")

	return cmd
}
