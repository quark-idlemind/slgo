package auth

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestANameRoundTripsThroughTheFourWords: what a client packs is what
// the daemon reads, for every length up to the limit, including none.
func TestANameRoundTripsThroughTheFourWords(t *testing.T) {
	for _, name := range []string{
		"", "slsh", "slbotd", "slbench", "slgo-multiattach",
		strings.Repeat("x", NameSize-1), strings.Repeat("y", NameSize),
		"slé", // not all of UTF-8 is one byte a character
	} {
		if got := UnpackName(PackName(name)); got != name {
			t.Errorf("%q came back as %q", name, got)
		}
	}
}

// TestALongerNameIsCutShortNotRefused: a client has no say in what its
// binary is called, so a long name is cut rather than refused -- and
// cut where a character ends, so what is left is still text.
func TestALongerNameIsCutShortNotRefused(t *testing.T) {
	long := strings.Repeat("z", 4<<20)
	if got := UnpackName(PackName(long)); got != long[:NameSize] {
		t.Errorf("a %d byte name came back as %d bytes, want the first %d", len(long), len(got), NameSize)
	}

	// A three-byte character straddling the limit is dropped whole.
	name := strings.Repeat("a", NameSize-1) + "€"
	got := UnpackName(PackName(name))
	if got != strings.Repeat("a", NameSize-1) {
		t.Errorf("%q came back as %q", name, got)
	}
	if !utf8.ValidString(got) {
		t.Errorf("the cut left %q, which is not UTF-8", got)
	}
}

// TestWhatIsReadIsTextAndNoLonger: the words are the sender's to fill
// with anything, and what comes out goes into sentences a person reads.
func TestWhatIsReadIsTextAndNoLonger(t *testing.T) {
	var words [4]uint64
	for i := range words {
		words[i] = 0xfffefdfcfbfaf9f8 // not UTF-8, and no zero to stop at
	}
	got := UnpackName(words)
	if !utf8.ValidString(got) || len(got) > NameSize {
		t.Errorf("read %q (%d bytes) from words that were not text", got, len(got))
	}
}

// TestBeginKeepsNoMoreThanAName: the name is kept with each pending
// challenge, for somebody who has proved nothing, so whatever a caller
// hands Begin is cut to a size this end chose.
func TestBeginKeepsNoMoreThanAName(t *testing.T) {
	s := newServer(t)
	if _, err := s.Begin(strings.Repeat("q", 4<<20)); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	kept := len(s.pendings[0].client)
	s.mu.Unlock()
	if kept > NameSize {
		t.Errorf("Begin kept a %d byte name; the limit is %d", kept, NameSize)
	}
}

// TestAShortSecretIsWarnedAbout: a secret short enough to type is
// short enough to guess, and the daemon's log should say so -- once,
// and not for a secret made as the guide says.
func TestAShortSecretIsWarnedAbout(t *testing.T) {
	var heard []string
	was := Warnf
	Warnf = func(format string, args ...any) { heard = append(heard, fmt.Sprintf(format, args...)) }
	t.Cleanup(func() { Warnf = was })

	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	short := filepath.Join(dir, "short")
	if err := os.WriteFile(short, []byte("hunter2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	long := filepath.Join(dir, "long")
	if err := os.WriteFile(long, []byte(strings.Repeat("0f", 32)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		if _, err := LoadSecret(short); err != nil {
			t.Fatalf("a short secret was refused rather than warned about: %v", err)
		}
	}
	if _, err := LoadSecret(long); err != nil {
		t.Fatal(err)
	}
	if len(heard) != 1 {
		t.Fatalf("heard %d warnings, want one for the short secret however often it is read: %q", len(heard), heard)
	}
	if !strings.Contains(heard[0], short) || !strings.Contains(heard[0], "7 bytes") {
		t.Errorf("the warning does not say which file or how short: %s", heard[0])
	}
}
