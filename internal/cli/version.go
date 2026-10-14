package cli

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/spf13/cobra"
)

// Build identifies the binary. cmd/stillhere fills it from variables that
// the release build injects with -ldflags -X.
type Build struct {
	Version string // release version without a leading "v", such as "0.1.0"
	Commit  string // full git SHA the binary was built from
	Date    string // build time, RFC 3339
}

// resolved fills what -ldflags did not inject from the build information the
// Go toolchain embeds: the module version of a go install build, and the VCS
// revision and time of a build from a checkout.
func (b Build) resolved() Build {
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; b.Version == "" && v != "" && v != "(devel)" {
			b.Version = strings.TrimPrefix(v, "v")
		}
		for _, s := range info.Settings {
			switch {
			case s.Key == "vcs.revision" && b.Commit == "":
				b.Commit = s.Value
			case s.Key == "vcs.time" && b.Date == "":
				b.Date = s.Value
			}
		}
	}
	if b.Version == "" {
		b.Version = "dev"
	}
	if b.Commit == "" {
		b.Commit = "unknown"
	}
	if b.Date == "" {
		b.Date = "unknown"
	}
	return b
}

func (e *env) versionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version, commit, build date and Go version",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(*cobra.Command, []string) error {
			_, err := fmt.Fprintf(e.stdout, "stillhere %s\ncommit    %s\nbuilt     %s\ngo        %s %s/%s\n",
				e.build.Version, e.build.Commit, e.build.Date,
				runtime.Version(), runtime.GOOS, runtime.GOARCH)
			return err
		},
	}
}
