package cli

import (
	"fmt"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// Build identifies the binary. cmd/stillhere fills it from variables that
// the release build injects with -ldflags -X.
type Build struct {
	Version string // release version, such as "0.1.0"; a leading "v" is dropped
	Commit  string // git SHA the binary was built from: full from -ldflags or vcs.revision, the first 12 digits from a pseudo-version
	Date    string // build time, RFC 3339; only an -ldflags build knows it
	// Committed is the commit's time, RFC 3339 in UTC, from the build
	// information (vcs.time) or a pseudo-version (RB-13); "" when unknown.
	// It is not the build's time, which Date is.
	Committed string
}

// pseudoVersion matches the end of a Go module pseudo-version, which go
// install of a commit, @latest before any tag included, puts in the build
// information: v0.0.0-20261015160338-5eb40a154b2f, and the pre-release
// forms vX.Y.Z-pre.0.yyyymmddhhmmss-abcdef123456 and
// vX.Y.(Z+1)-0.yyyymmddhhmmss-abcdef123456 (go.dev/ref/mod#pseudo-versions).
// It holds the commit's UTC time and the first 12 digits of its SHA.
var pseudoVersion = regexp.MustCompile(`[-.](\d{14})-([0-9a-f]{12})(?:\+incompatible)?(?:\+dirty)?$`)

// resolved fills what -ldflags did not inject from the build information the
// Go toolchain embeds: the module version of a go install build, the VCS
// revision and commit time of a build from a checkout, and, for go install
// of a commit, the commit and its time that its pseudo-version holds
// (RB-13). It never takes the build date from there: vcs.time is the
// commit's time, not the build's.
func (b Build) resolved() Build {
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; b.Version == "" && v != "" && v != "(devel)" {
			b.Version = v
		}
		for _, s := range info.Settings {
			switch {
			case s.Key == "vcs.revision" && b.Commit == "":
				b.Commit = s.Value
			case s.Key == "vcs.time" && b.Committed == "":
				b.Committed = s.Value
			}
		}
	}
	if m := pseudoVersion.FindStringSubmatch(b.Version); m != nil {
		if b.Commit == "" {
			b.Commit = m[2]
		}
		if t, err := time.Parse("20060102150405", m[1]); err == nil && b.Committed == "" {
			b.Committed = t.UTC().Format(time.RFC3339)
		}
	}
	// Module versions and git describe both carry a "v".
	b.Version = strings.TrimPrefix(b.Version, "v")
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

// versionLine is the first line of 'stillhere version', and all that
// 'stillhere --version' prints: one function, so the two cannot drift.
func versionLine(b Build) string { return "stillhere " + b.Version }

func (e *env) versionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version, commit, build date and Go version",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(*cobra.Command, []string) error {
			commit := e.build.Commit
			if e.build.Committed != "" {
				commit += " (committed " + e.build.Committed + ")"
			}
			_, err := fmt.Fprintf(e.stdout, "%s\ncommit    %s\nbuilt     %s\ngo        %s %s/%s\n",
				versionLine(e.build), commit, e.build.Date,
				runtime.Version(), runtime.GOOS, runtime.GOARCH)
			return err
		},
	}
}
