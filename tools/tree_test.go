package tools

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestNothingInTheTreeIsAnUnsignedIdentifier is tools/check-identities'
// uuid, digest and elision rule run over every tracked file, so that a
// real or unsigned id fails `go test` and not only a commit.  go.sum is
// left out: it is a list of hashes of other people's code.
func TestNothingInTheTreeIsAnUnsignedIdentifier(t *testing.T) {
	needPerl(t)
	top, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skip("not in a git checkout")
	}
	root := strings.TrimSpace(string(top))
	list, err := exec.Command("git", "-C", root, "ls-files", "-z").Output()
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, f := range strings.Split(string(list), "\x00") {
		if f == "" || f == "go.sum" || strings.HasSuffix(f, "/go.sum") {
			continue
		}
		files = append(files, filepath.Join(root, f))
	}
	for len(files) > 0 {
		n := min(len(files), 200)
		out, err := exec.Command("perl", append([]string{"scan-ids", "--check"}, files[:n]...)...).CombinedOutput()
		if err != nil {
			t.Errorf("identifiers that are neither signed nor known:\n%s", out)
		}
		files = files[n:]
	}
}

// TestEveryNameInTheTreeIsOnTheList is tools/scan-names run over every
// tracked file, so that a name that is not in tools/known-names fails
// `go test` and not only a commit.
func TestEveryNameInTheTreeIsOnTheList(t *testing.T) {
	needPerl(t)
	top, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skip("not in a git checkout")
	}
	root := strings.TrimSpace(string(top))
	list, err := exec.Command("git", "-C", root, "ls-files", "-z").Output()
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, f := range strings.Split(string(list), "\x00") {
		if f == "" || strings.HasSuffix(f, ".png") || strings.HasSuffix(f, ".jpg") {
			continue
		}
		files = append(files, filepath.Join(root, f))
	}
	for len(files) > 0 {
		n := min(len(files), 200)
		out, err := exec.Command("perl", append([]string{"scan-names", "--check"}, files[:n]...)...).CombinedOutput()
		if err != nil {
			t.Errorf("names that are not on tools/known-names:\n%s", out)
		}
		files = files[n:]
	}
}
