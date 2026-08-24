package cmd

import (
	"fmt"
	"runtime/debug"
	"time"

	"github.com/spf13/cobra"
)

var (
	Version   = "dev" // overridden by -ldflags in release builds
	Commit    = "none"
	BuildDate = "unknown"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("homelabmon %s (commit: %s, built: %s)\n", Version, Commit, BuildDate)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)

	// Plain `go build` and Docker builds don't pass -ldflags -- fall back
	// to the VCS info the Go toolchain embeds automatically when building
	// inside a git repository (requires the .git dir and a git binary).
	if Version == "dev" {
		if info, ok := debug.ReadBuildInfo(); ok {
			var rev, when, modified string
			for _, s := range info.Settings {
				switch s.Key {
				case "vcs.revision":
					rev = s.Value
				case "vcs.time":
					when = s.Value
				case "vcs.modified":
					modified = s.Value
				}
			}
			if len(rev) > 8 {
				rev = rev[:8]
			}
			if rev != "" {
				Version = "g" + rev
				if modified == "true" {
					Version += "-dirty"
				}
				Commit = rev
			}
			if when != "" {
				if t, err := time.Parse(time.RFC3339, when); err == nil {
					BuildDate = t.UTC().Format("2006-01-02 15:04:05")
				} else {
					BuildDate = when
				}
			}
		}
	}
}
