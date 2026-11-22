package main

import (
	"fmt"
	"runtime/debug"
	"strings"

	"github.com/spf13/cobra"
)

// Set at build time: -ldflags "-X main.version=v0.1.0 -X main.commit=<sha> -X main.date=<yyyy-mm-dd>".
// A plain `go build` leaves them at their defaults, and `go install` of a tagged
// version is described from the build info the Go toolchain embeds.
var (
	version = "dev"
	commit  = ""
	date    = ""
)

// versionString describes this binary.
func versionString() string {
	bi, _ := debug.ReadBuildInfo()
	return describeVersion(version, commit, date, bi)
}

// describeVersion turns the ldflags values (and, as a fallback, the Go build
// info) into "v0.1.0 (commit 1a2b3c4, built 2026-11-22)". Values set at build
// time win; the build info only fills in what they leave out.
func describeVersion(version, commit, date string, bi *debug.BuildInfo) string {
	if bi != nil {
		if version == "dev" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			version = bi.Main.Version
		}
		if commit == "" {
			for _, s := range bi.Settings {
				if s.Key == "vcs.revision" {
					commit = s.Value
				}
			}
		}
	}
	var details []string
	if commit != "" {
		details = append(details, "commit "+commit[:min(len(commit), 7)])
	}
	if date != "" {
		details = append(details, "built "+date)
	}
	if len(details) == 0 {
		return version
	}
	return fmt.Sprintf("%s (%s)", version, strings.Join(details, ", "))
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the quietbox version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "quietbox %s\n", versionString())
		},
	}
}
