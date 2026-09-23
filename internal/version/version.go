// Package version says which build of a program this is.
//
// The answer comes from what the Go toolchain records in every binary
// built inside a git checkout: the version, taken from the release tag,
// and the commit, its date and whether the tree had edits in it.  So a
// plain `go build` or `go install ./cmd/...` is enough, and nothing has
// to remember to pass anything.
//
// Each piece can still be set at link time, which beats what was
// recorded -- for a build made somewhere the checkout is not:
//
//	-ldflags "-X github.com/quark-idlemind/slgo/internal/version.name=v0.6.1
//	          -X github.com/quark-idlemind/slgo/internal/version.commitDate=2026-09-23 ..."
package version

import (
	"fmt"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
)

// Set at link time, when they are set at all.  Empty means "take it from
// the build information".
var (
	name       = "" // the release, as tagged: v0.6.1
	commitDate = "" // the date of the commit built, YYYY-MM-DD
	commitHash = "" // the commit, abbreviated
	treeState  = "" // "modified" when the tree had edits in it
	buildTime  = "" // when a modified tree was built, since its commit does not say
)

// String is the line a program prints for --version:
//
//	slsh v0.6.1 (2026-09-23) 5947e3f
//	slsh development build after v0.6.1 (2026-09-24) 8c21d0a modified
func String(program string) string {
	var main string
	settings := map[string]string{}
	if info, ok := debug.ReadBuildInfo(); ok {
		main = info.Main.Version
		for _, s := range info.Settings {
			settings[s.Key] = s.Value
		}
	}
	return describe(program, main, settings)
}

// describe is String on what the build information said, apart from where
// the link-time values say otherwise.
func describe(program, main string, settings map[string]string) string {
	release := name
	if release == "" {
		release = releaseOf(main)
	}

	date := commitDate
	if date == "" {
		date = settings["vcs.time"]
		if len(date) >= len("2006-01-02") {
			date = date[:len("2006-01-02")]
		}
	}
	if date == "" {
		date = "unknown"
	}

	hash := commitHash
	if hash == "" {
		hash = settings["vcs.revision"]
		if len(hash) > 7 {
			hash = hash[:7]
		}
	}

	state := treeState
	if state == "" && settings["vcs.modified"] == "true" {
		state = "modified"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s %s (%s)", program, release, date)
	if hash != "" {
		b.WriteString(" " + hash)
	}
	if state != "" {
		b.WriteString(" " + state)
		if buildTime != "" {
			b.WriteString(" " + buildTime)
		}
	}
	return b.String()
}

// pseudo is the form Go gives a commit that is not a release: the next
// patch release, a zero, the commit's time and its hash.  v0.6.2-0.2026...
// is a commit after v0.6.1, not a v0.6.2 that does not exist yet.
var pseudo = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)-0\.\d{14}-[0-9a-f]{12}$`)

// untagged is the form for a commit with no release tag behind it at all.
var untagged = regexp.MustCompile(`^v\d+\.0\.0-\d{14}-[0-9a-f]{12}$`)

// releaseOf says which release a recorded version is, in words a person
// will not misread.
func releaseOf(v string) string {
	v = strings.TrimSuffix(v, "+dirty") // said as "modified" instead
	switch {
	case v == "" || v == "(devel)" || untagged.MatchString(v):
		// No tag reached this build, or it was built somewhere with no
		// history to read one from.
		return "development build"
	case pseudo.MatchString(v):
		m := pseudo.FindStringSubmatch(v)
		patch, _ := strconv.Atoi(m[3])
		if patch == 0 {
			return "development build"
		}
		return fmt.Sprintf("development build after v%s.%s.%d", m[1], m[2], patch-1)
	}
	return v
}
