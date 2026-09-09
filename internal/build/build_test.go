package build

import (
	"runtime/debug"
	"strings"
	"testing"
)

// with swaps in fake build info and stamped values for one test, restoring everything after.
func with(t *testing.T, version, commit, date string, settings map[string]string, present bool) {
	t.Helper()
	oldV, oldC, oldD, oldFn := Version, Commit, Date, buildInfo
	t.Cleanup(func() { Version, Commit, Date, buildInfo = oldV, oldC, oldD, oldFn })

	Version, Commit, Date = version, commit, date
	buildInfo = func() (*debug.BuildInfo, bool) {
		if !present {
			return nil, false
		}
		bi := &debug.BuildInfo{}
		for k, v := range settings {
			bi.Settings = append(bi.Settings, debug.BuildSetting{Key: k, Value: v})
		}
		return bi, true
	}
}

// A tagged release identifies itself by its tag, because that is the name a person asks for.
func TestReleaseUsesTheStampedTag(t *testing.T) {
	with(t, "v1.2.3", "", "2026-09-09T10:00:00Z", map[string]string{
		"vcs.revision": "abcdef1234567890",
		"vcs.modified": "false",
	}, true)

	got := Current()
	if got.Version != "v1.2.3" {
		t.Errorf("version = %q, want the tag", got.Version)
	}
	if got.Source != Release {
		t.Errorf("source = %q, want %q", got.Source, Release)
	}
	// The commit is still reported: the tag says what was asked for, the commit says what it was.
	if got.Commit != "abcdef1234567890" {
		t.Errorf("commit = %q, want it filled in from the build info", got.Commit)
	}
}

// A local build from a clean checkout has no tag, and its identity is the commit — which Go embeds
// on its own, so nothing has to be passed for this to work.
func TestSourceBuildUsesTheCommit(t *testing.T) {
	with(t, "", "", "", map[string]string{
		"vcs.revision": "abcdef1234567890abcdef",
		"vcs.modified": "false",
	}, true)

	got := Current()
	if got.Version != "abcdef123456" {
		t.Errorf("version = %q, want the commit abbreviated to 12 chars", got.Version)
	}
	if got.Source != Committed {
		t.Errorf("source = %q, want %q", got.Source, Committed)
	}
}

// The case that matters most for trust: a build with uncommitted changes is not reproducible by
// anyone, and must not present itself as a commit that somebody else could check out.
func TestDirtyBuildIsMarkedInTheVersionItself(t *testing.T) {
	with(t, "", "", "", map[string]string{
		"vcs.revision": "abcdef1234567890",
		"vcs.modified": "true",
	}, true)

	got := Current()
	if !strings.HasSuffix(got.Version, "-dirty") {
		t.Errorf("version = %q, want a -dirty suffix in the version string, not only in the source field", got.Version)
	}
	if got.Source != Modified {
		t.Errorf("source = %q, want %q", got.Source, Modified)
	}
}

// "dev" is the old placeholder and carries no information. It must not be mistaken for a release.
func TestDevPlaceholderDoesNotCountAsARelease(t *testing.T) {
	with(t, "dev", "", "", map[string]string{
		"vcs.revision": "abcdef1234567890",
		"vcs.modified": "false",
	}, true)

	got := Current()
	if got.Source == Release {
		t.Error(`"dev" was treated as a release version`)
	}
	if got.Version == "dev" {
		t.Error(`version stayed "dev" when a commit was available`)
	}
}

// Building from a source archive rather than a checkout leaves no VCS data. Saying "unknown" is
// correct; inventing a version would be worse than admitting it.
func TestArchiveBuildAdmitsItDoesNotKnow(t *testing.T) {
	with(t, "", "", "", map[string]string{}, true)
	if got := Current(); got.Source != Unknown || got.Version != "unknown" {
		t.Errorf("got %+v, want an honest unknown", got)
	}

	with(t, "", "", "", nil, false)
	if got := Current(); got.Source != Unknown {
		t.Errorf("source = %q with no build info at all, want %q", got.Source, Unknown)
	}
}

// A stamped commit is not overwritten by the embedded one: the pipeline may know something the
// build info does not.
func TestStampedCommitWins(t *testing.T) {
	with(t, "v1.0.0", "deadbeef", "", map[string]string{"vcs.revision": "abcdef"}, true)
	if got := Current(); got.Commit != "deadbeef" {
		t.Errorf("commit = %q, want the stamped value", got.Commit)
	}
}

func TestStringIsOneLineAndNamesTheBinary(t *testing.T) {
	with(t, "v1.2.3", "", "", nil, false)
	got := Current().String()
	if strings.Count(got, "\n") != 0 {
		t.Errorf("String() = %q, want a single line", got)
	}
	if !strings.HasPrefix(got, "x1200 ") || !strings.Contains(got, "v1.2.3") {
		t.Errorf("String() = %q", got)
	}
	// A release needs no qualifier; anything else does.
	if strings.Contains(got, "(") {
		t.Errorf("String() = %q, want no qualifier on a release", got)
	}

	with(t, "", "", "", map[string]string{"vcs.revision": "abcdef1234567890", "vcs.modified": "true"}, true)
	if got := Current().String(); !strings.Contains(got, "uncommitted") {
		t.Errorf("String() = %q, want a dirty build to say so", got)
	}
}

func TestDetailsCoversWhatIsNeededToDebugABinary(t *testing.T) {
	with(t, "v1.2.3", "deadbeefcafe", "2026-09-09T10:00:00Z", nil, false)
	got := Current().Details()
	for _, want := range []string{"version:", "v1.2.3", "source:", "commit:", "deadbeefcafe", "built:", "go:", "platform:"} {
		if !strings.Contains(got, want) {
			t.Errorf("Details() missing %q:\n%s", want, got)
		}
	}
}

// Absent fields are omitted rather than shown empty: a blank "built:" line invites the reader to
// wonder what went wrong, when the answer is simply that a local build has no release date.
func TestDetailsOmitsWhatItDoesNotKnow(t *testing.T) {
	with(t, "", "", "", map[string]string{"vcs.revision": "abcdef1234567890"}, true)
	if got := Current().Details(); strings.Contains(got, "built:") {
		t.Errorf("Details() shows an empty built line:\n%s", got)
	}
}
