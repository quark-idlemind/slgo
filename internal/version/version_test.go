package version

import "testing"

// TestAReleaseSaysItsTagDateAndCommit: a build of a tagged commit from a
// clean tree.
func TestAReleaseSaysItsTagDateAndCommit(t *testing.T) {
	got := describe("slsh", "v0.6.1", map[string]string{
		"vcs.revision": "5947e3f0c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f6",
		"vcs.time":     "2026-09-23T16:40:12Z",
		"vcs.modified": "false",
	})
	if want := "slsh v0.6.1 (2026-09-23) 5947e3f"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestACommitAfterAReleaseIsNotCalledTheNextOne: Go names such a commit
// after the NEXT patch release, and a person reading "v0.6.2" would take
// it for one.  A tree with edits in it says so.
func TestACommitAfterAReleaseIsNotCalledTheNextOne(t *testing.T) {
	got := describe("slsh", "v0.6.2-0.20260924101500-8c21d0a4b3e2+dirty", map[string]string{
		"vcs.revision": "8c21d0a4b3e2f1a0b9c8d7e6f5a4b3c2d1e0f9a8",
		"vcs.time":     "2026-09-24T10:15:00Z",
		"vcs.modified": "true",
	})
	if want := "slsh development build after v0.6.1 (2026-09-24) 8c21d0a modified"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestABuildWithNothingRecordedStillAnswers: built outside a checkout, or
// under go test, there is no tag and no commit to say.
func TestABuildWithNothingRecordedStillAnswers(t *testing.T) {
	for _, main := range []string{"", "(devel)", "v0.0.0-20260924101500-8c21d0a4b3e2"} {
		if got, want := describe("slsh", main, nil), "slsh development build (unknown)"; got != want {
			t.Errorf("for %q got %q, want %q", main, got, want)
		}
	}
}

// TestWhatIsSetAtLinkTimeWins over what the build recorded.
func TestWhatIsSetAtLinkTimeWins(t *testing.T) {
	defer func(n, d, h, s, b string) { name, commitDate, commitHash, treeState, buildTime = n, d, h, s, b }(
		name, commitDate, commitHash, treeState, buildTime)
	name, commitDate, commitHash, treeState, buildTime = "v9.9.9", "2026-01-02", "abcdef1", "modified", "2026-01-02T03:04:05Z"

	got := describe("slsh", "v0.6.1", map[string]string{"vcs.revision": "5947e3f0", "vcs.time": "2026-09-23T00:00:00Z"})
	if want := "slsh v9.9.9 (2026-01-02) abcdef1 modified 2026-01-02T03:04:05Z"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
