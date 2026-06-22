package main

import (
	"runtime/debug"
	"strings"

	"github.com/spf13/cobra"
)

// newVersionCmd builds the `booty version` subcommand. It reports the build
// version (from the -ldflags `version` var), the VCS commit and dirty flag and
// the Go toolchain version (from the embedded build info), and the build
// timestamp (from the -ldflags `timestamp` var).
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version and build information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			bi, _ := debug.ReadBuildInfo()
			_, err := cmd.OutOrStdout().Write([]byte(versionInfo(version, timestamp, bi)))
			return err
		},
	}
}

// versionInfo composes the human-readable version block. It is pure so it can
// be unit-tested with a synthesized *debug.BuildInfo. version and timestamp are
// the -ldflags vars; bi may be nil when build info is unavailable.
func versionInfo(version, timestamp string, bi *debug.BuildInfo) string {
	var b strings.Builder

	b.WriteString("booty ")
	if version == "" {
		version = "dev"
	}
	b.WriteString(version)
	b.WriteByte('\n')

	if bi != nil {
		if commit := buildSetting(bi, "vcs.revision"); commit != "" {
			b.WriteString("commit: ")
			b.WriteString(commit)
			if buildSetting(bi, "vcs.modified") == "true" {
				b.WriteString(" (dirty)")
			}
			b.WriteByte('\n')
		}
		if bi.GoVersion != "" {
			b.WriteString("go: ")
			b.WriteString(bi.GoVersion)
			b.WriteByte('\n')
		}
	}

	if timestamp != "" {
		b.WriteString("built: ")
		b.WriteString(timestamp)
		b.WriteByte('\n')
	}

	return b.String()
}

// buildSetting returns the value of the named build setting, or "" if absent.
func buildSetting(bi *debug.BuildInfo, key string) string {
	for _, s := range bi.Settings {
		if s.Key == key {
			return s.Value
		}
	}
	return ""
}
