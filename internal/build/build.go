// Package build reports what this binary is and where it came from.
//
// A version string is only useful if it cannot lie. The question being answered is always the same —
// "is the thing running on that machine the thing I think I built?" — and it is asked at the worst
// moment, when something is broken and the answer decides whether to keep debugging or go and
// deploy. So a build must be able to say not just a number but whether that number is trustworthy.
//
// Two kinds of build exist and they get their identity from different places:
//
//   - A release is built from a tag by the pipeline, which stamps the tag in through -ldflags. The
//     tag is the identity, because that is what a person asks for by name.
//   - A local build has no tag, or has a tag plus uncommitted changes on top. Its identity is the
//     commit, which Go embeds automatically from the VCS — no ldflags needed and nothing to forget.
//
// The second half matters more than it looks. `go build` records the revision and whether the tree
// was dirty in the binary's own build info, so a developer build identifies itself correctly even
// when nobody passed a flag. A binary that says "dev" tells you nothing; one that says
// "abc1234 (modified)" tells you exactly what to go and look at.
package build

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
)

// Stamped values, set by -ldflags at release time. Empty in a local build, which is the signal that
// the VCS information should be used instead.
var (
	// Version is the git tag, set by the release pipeline.
	Version string
	// Commit is optionally stamped too; when empty it comes from the embedded build info.
	Commit string
	// Date is when the release was built.
	Date string
)

// Source is how a binary came to exist, which is what decides whether its version can be trusted.
type Source string

const (
	// Release was built from a tag by the pipeline.
	Release Source = "release"
	// Committed was built locally from a clean checkout, so the commit identifies it exactly.
	Committed Source = "source"
	// Modified was built locally from a tree with uncommitted changes. The commit is a starting
	// point and not an identity: nobody else can reproduce this binary.
	Modified Source = "source (uncommitted changes)"
	// Unknown means neither a tag nor VCS information was available — a build from an archive
	// rather than a checkout, typically.
	Unknown Source = "unknown"
)

// Info is everything known about this binary.
type Info struct {
	Version  string `json:"version"`
	Commit   string `json:"commit,omitempty"`
	Date     string `json:"date,omitempty"`
	Source   Source `json:"source"`
	Go       string `json:"go"`
	Platform string `json:"platform"`
}

// buildInfo is indirected so tests can supply their own. debug.ReadBuildInfo reports on the running
// test binary, which is not the thing under test.
var buildInfo = debug.ReadBuildInfo

// Current returns what this binary is.
func Current() Info {
	info := Info{
		Version:  Version,
		Commit:   Commit,
		Date:     Date,
		Go:       runtime.Version(),
		Platform: runtime.GOOS + "/" + runtime.GOARCH,
	}

	revision, modified, ok := vcs()
	if info.Commit == "" {
		info.Commit = revision
	}

	switch {
	case info.Version != "" && info.Version != "dev":
		// A stamped version wins: the pipeline set it from the tag, and that is the name a person
		// will use to ask for this build.
		info.Source = Release
	case revision != "":
		info.Version = shortCommit(revision)
		if modified {
			info.Source = Modified
			// Marked in the version itself, not only in the source field, because the version is
			// what gets pasted into an issue and a dirty build must not masquerade as a commit
			// anybody else can check out.
			info.Version += "-dirty"
		} else {
			info.Source = Committed
		}
	case ok:
		// Build info was present but carried no VCS data, which is what happens when building from
		// a source archive rather than a checkout.
		info.Version = "unknown"
		info.Source = Unknown
	default:
		info.Version = "unknown"
		info.Source = Unknown
	}
	return info
}

// vcs pulls the revision and dirty flag out of the embedded build info.
func vcs() (revision string, modified, ok bool) {
	bi, ok := buildInfo()
	if !ok || bi == nil {
		return "", false, false
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	return revision, modified, true
}

// shortCommit abbreviates a hash to the twelve characters git itself would show, which is long
// enough to be unambiguous and short enough to read aloud.
func shortCommit(rev string) string {
	const short = 12
	if len(rev) > short {
		return rev[:short]
	}
	return rev
}

// String renders one line, for `--version`.
func (i Info) String() string {
	var b strings.Builder
	b.WriteString("x1200 " + i.Version)
	if i.Source != Release && i.Source != Unknown {
		b.WriteString(" (" + string(i.Source) + ")")
	}
	return b.String()
}

// Details renders the full picture, for the `version` subcommand.
//
// More than one line because the short form is for identifying a binary and this is for debugging
// one. The Go version and platform are here rather than in String because they answer a different
// question — "why does it behave differently on that machine" rather than "which build is this".
func (i Info) Details() string {
	rows := [][2]string{
		{"version", i.Version},
		{"source", string(i.Source)},
	}
	if i.Commit != "" {
		rows = append(rows, [2]string{"commit", i.Commit})
	}
	if i.Date != "" {
		rows = append(rows, [2]string{"built", i.Date})
	}
	rows = append(rows, [2]string{"go", i.Go}, [2]string{"platform", i.Platform})

	width := 0
	for _, r := range rows {
		if n := len(r[0]); n > width {
			width = n
		}
	}
	var b strings.Builder
	b.WriteString("x1200\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "  %-*s %s\n", width+1, r[0]+":", r[1])
	}
	return b.String()
}
