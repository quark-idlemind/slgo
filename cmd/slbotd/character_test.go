package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// A person to be talking to, and somebody else.
var (
	someone     = msg.MustParseUUID("f5d57e57-7e57-c0de-3da8-44266bc7432e")
	someoneName = "Example Resident"
	anybody     = msg.MustParseUUID("f71a7e57-7e57-c0de-5e1e-5d299ba46463")
	anybodyName = "Another Person"
)

// characters writes a directory of them and hands back the path.
func characters(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, text := range files {
		if err := writeFile(filepath.Join(dir, name), text); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func read(t *testing.T, path string, who msg.UUID, name string) string {
	t.Helper()
	text, err := readCharacter(path, who, name)
	if err != nil {
		t.Fatalf("readCharacter(%q): %v", path, err)
	}
	return text
}

// A backstory line naming a FILE is what it always was: one character,
// worn with everybody.  Nothing about directories may change that.
func TestAFileIsOneCharacterForEverybody(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hobb.txt")
	if err := writeFile(path, "You are Hobb, a dockhand.\n"); err != nil {
		t.Fatal(err)
	}
	for _, who := range []struct {
		id   msg.UUID
		name string
	}{{someone, someoneName}, {anybody, anybodyName}} {
		if got := read(t, path, who.id, who.name); got != "You are Hobb, a dockhand." {
			t.Errorf("%s got %q", who.name, got)
		}
	}
}

// A person's file says for itself what it is made of: a line that is
// nothing but a relative path is the file it names.
func TestAPersonsFileIncludesTheCharacter(t *testing.T) {
	dir := characters(t, map[string]string{
		"default.txt": "You are Hobb, a dockhand.",
		"example resident.txt": "./default.txt\n\n" +
			"You have known this one for years.",
	})

	got := read(t, dir, someone, someoneName)
	want := "You are Hobb, a dockhand.\n\nYou have known this one for years."
	if got != want {
		t.Errorf("the include did not land:\n%q\nwant\n%q", got, want)
	}

	// Somebody the directory says nothing about gets the default, and
	// not the other person's file.
	if got := read(t, dir, anybody, anybodyName); got != "You are Hobb, a dockhand." {
		t.Errorf("a stranger got the wrong character: %q", got)
	}
}

// The included part may go anywhere, which is the point of it being
// the file's decision: the shared character often wants to be LAST,
// nearest the conversation.
func TestAnIncludeMayGoAnywhereInTheFile(t *testing.T) {
	dir := characters(t, map[string]string{
		"default.txt":          "You are Hobb, a dockhand.",
		"example resident.txt": "You have known this one for years.\n\n./default.txt",
	})
	want := "You have known this one for years.\n\nYou are Hobb, a dockhand."
	if got := read(t, dir, someone, someoneName); got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
}

// An include may reach out of the directory with "../", which is how
// several avatars share one paragraph.
func TestAnIncludeMayReachOutOfTheDirectory(t *testing.T) {
	root := t.TempDir()
	if err := writeFile(filepath.Join(root, "shared.txt"), "The tide is out."); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "hobb")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(filepath.Join(dir, "default"), "You are Hobb.\n../shared.txt"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, dir, someone, someoneName); got != "You are Hobb.\nThe tide is out." {
		t.Errorf("got %q", got)
	}
}

// Includes nest, and a file included twice from different places is
// included twice -- it is a cycle that is refused, not a diamond.
func TestIncludesNest(t *testing.T) {
	dir := characters(t, map[string]string{
		"default": "./a.txt\n./b.txt",
		"a.txt":   "one\n./c.txt",
		"b.txt":   "./c.txt\ntwo",
		"c.txt":   "three",
	})
	if got := read(t, dir, someone, someoneName); got != "one\nthree\nthree\ntwo" {
		t.Errorf("got %q", got)
	}
}

// A file that includes itself, directly or round a ring, is a mistake
// in a file somebody wrote and is said out loud rather than quietly
// truncated.
func TestAnIncludeCycleIsRefused(t *testing.T) {
	for _, c := range []struct {
		what  string
		files map[string]string
	}{
		{"itself", map[string]string{"default": "You are Hobb.\n./default"}},
		{"a ring", map[string]string{
			"default": "./a.txt", "a.txt": "./b.txt", "b.txt": "./default"}},
	} {
		t.Run(c.what, func(t *testing.T) {
			dir := characters(t, c.files)
			if _, err := readCharacter(dir, someone, someoneName); err == nil {
				t.Error("a cycle was not refused")
			}
		})
	}
}

// An include of something that is not there is the same class of
// mistake as a backstory path that is not there.
func TestAMissingIncludeIsAnError(t *testing.T) {
	dir := characters(t, map[string]string{"default": "You are Hobb.\n./nope.txt"})
	if _, err := readCharacter(dir, someone, someoneName); err == nil {
		t.Error("a missing include was not an error")
	}
}

// Only a line that is a path and nothing else.  Prose that mentions
// one, or a line with words after it, is prose.
func TestOnlyALineThatIsAPathIsAnInclude(t *testing.T) {
	dir := characters(t, map[string]string{
		"default":  "You keep your notes in ./notes and nowhere else.",
		"notes":    "SHOULD NOT BE HERE",
		"notes.md": "NOR THIS",
	})
	got := read(t, dir, someone, someoneName)
	if strings.Contains(got, "SHOULD NOT BE HERE") || strings.Contains(got, "NOR THIS") {
		t.Errorf("prose was treated as an include: %q", got)
	}
	if !strings.Contains(got, "./notes and nowhere else") {
		t.Errorf("the line was not left alone: %q", got)
	}
}

// An absolute path is not an include, because a directory of
// characters should be movable.  It is left as the line it is.
func TestAnAbsolutePathIsNotAnInclude(t *testing.T) {
	dir := characters(t, map[string]string{"default": "/etc/hosts"})
	if got := read(t, dir, someone, someoneName); got != "/etc/hosts" {
		t.Errorf("an absolute path was not left alone: %q", got)
	}
}

// The name is matched however somebody spelt the filename, because a
// directory of these is typed into by hand and a space in a filename is
// tiresome enough to be worth avoiding.
func TestAPersonsCharacterIsFoundHoweverTheNameIsSpelt(t *testing.T) {
	for _, name := range []string{
		"example resident.txt",
		"example-resident.txt",
		"EXAMPLE_RESIDENT.TXT",
		"Example Resident",
		"example-resident",
	} {
		t.Run(name, func(t *testing.T) {
			dir := characters(t, map[string]string{
				"default": "You are Hobb.",
				name:      "You have met before.",
			})
			if got := read(t, dir, someone, someoneName); !strings.Contains(got, "met before") {
				t.Errorf("%q was not found for %q: %q", name, someoneName, got)
			}
		})
	}
}

// A file may be named by the person's id instead, the way the trusted
// and chat lists take either.
func TestAPersonsCharacterCanBeNamedByID(t *testing.T) {
	dir := characters(t, map[string]string{
		"default":                 "You are Hobb.",
		someone.String() + ".txt": "You owe this one a favour.",
		"somebody else.txt":       "Not this one.",
	})
	if got := read(t, dir, someone, someoneName); !strings.Contains(got, "a favour") {
		t.Errorf("the id-named character was not found: %q", got)
	}

	// And by id even when the name is not known at all.
	if got := read(t, dir, someone, ""); !strings.Contains(got, "a favour") {
		t.Errorf("the id-named character was not found without a name: %q", got)
	}
}

// With no default, the person's file stands alone -- which is how an
// operator says "a different character for each person" rather than
// "one character, with additions".
func TestWithNoDefaultThePersonsCharacterStandsAlone(t *testing.T) {
	dir := characters(t, map[string]string{
		"example resident.txt": "You are the harbourmaster.",
	})
	if got := read(t, dir, someone, someoneName); got != "You are the harbourmaster." {
		t.Errorf("got %q", got)
	}
	// And somebody with nothing in there is nobody in particular,
	// which is not an error: it is an avatar with no backstory.
	if got := read(t, dir, anybody, anybodyName); got != "" {
		t.Errorf("a stranger got a character from nowhere: %q", got)
	}
}

// Two spellings of one person is a mistake in a directory, but it must
// not be an UNSTABLE mistake: the same file every time, so that the
// character does not change from turn to turn.
func TestTwoSpellingsOfOnePersonPickTheSameFileEveryTime(t *testing.T) {
	dir := characters(t, map[string]string{
		"default":              "You are Hobb.",
		"example resident.txt": "first",
		"example-resident.txt": "second",
	})
	first := read(t, dir, someone, someoneName)
	for i := 0; i < 5; i++ {
		if got := read(t, dir, someone, someoneName); got != first {
			t.Fatalf("the character changed between reads:\n%q\n%q", first, got)
		}
	}
}

// A path that was named and cannot be read is worth saying out loud;
// the daemon logs it and speaks with no character at all.
func TestANamedPathThatIsNotThereIsAnError(t *testing.T) {
	if _, err := readCharacter(filepath.Join(t.TempDir(), "nope"), someone, someoneName); err == nil {
		t.Error("a missing backstory path was not an error")
	}
}

// --check assembles every character, so that a file which forgot to
// include the character is visible by its size, and a broken include is
// found before anybody is logged in.
func TestDescribeCharactersSaysWhatIsThere(t *testing.T) {
	file := filepath.Join(t.TempDir(), "hobb.txt")
	if err := writeFile(file, "You are Hobb."); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		what string
		path string
		want []string
	}{
		{"a plain file", file, []string{"hobb.txt", "13 characters"}},
		{"a default alone", characters(t, map[string]string{
			"default": "x"}), []string{"nobody in particular"}},
		{"a default and people", characters(t, map[string]string{
			"default": "x", "a b.txt": "y", "c d.txt": "z"}),
			[]string{"a default, and 2 for particular people"}},
		{"one person, no default", characters(t, map[string]string{
			"a b.txt": "y"}), []string{"NO DEFAULT"}},
		{"nothing at all", t.TempDir(), []string{"EMPTY"}},
		{"an assembled size", characters(t, map[string]string{
			"default": "You are Hobb.",
			"a b.txt": "./default",
			"c d.txt": "short",
		}), []string{"a b.txt", "13 characters", "5 characters"}},
		{"a broken include", characters(t, map[string]string{
			"default": "./nope"}), []string{"BROKEN"}},
	} {
		t.Run(c.what, func(t *testing.T) {
			lines, err := describeCharacters(c.path)
			if err != nil {
				t.Fatal(err)
			}
			got := strings.Join(lines, "\n")
			for _, want := range c.want {
				if !strings.Contains(got, want) {
					t.Errorf("got\n%s\nwant something with %q in it", got, want)
				}
			}
		})
	}
}

// End to end: one avatar, two people, two characters, and each reply
// composed under the right one.
//
// The kv cache is why this is worth a test of its own rather than
// trusting the lookup.  Conversations with one avatar are pinned to
// slots and a slot taken from somebody else is erased precisely
// because they used to share a prefix; with a directory they no longer
// do, and the character each person gets has to be the one their own
// prompt was built with.
func TestTwoPeopleGetTheirOwnCharacters(t *testing.T) {
	d, b, grid := newTestDaemon(t)
	f := newFakeLLM(t)
	withChat(t, d, f, Anyone)

	dir := characters(t, map[string]string{
		"default": "You are Hobb, a dockhand.",
		"example resident.txt": "./default\n\n" +
			"You have known this one for years.",
	})
	d.cfg.Backstory["example"] = dir
	d.chat.cfg = d.cfg

	defer serving(t, b)()
	grid.deliver(t, incoming(someone, someoneName, sl.DialogMessage, "who am I?"))
	waitIMs(t, grid, 1)
	grid.deliver(t, incoming(anybody, anybodyName, sl.DialogMessage, "who am I?"))
	waitIMs(t, grid, 2)

	asks := f.sawAsks()
	if len(asks) < 2 {
		t.Fatalf("wanted two asks, got %d", len(asks))
	}
	system := func(ask map[string]any) string {
		msgs, _ := ask["messages"].([]any)
		first, _ := msgs[0].(map[string]any)
		s, _ := first["content"].(string)
		return s
	}

	known, stranger := system(asks[0]), system(asks[1])
	if !strings.Contains(known, "known this one for years") {
		t.Errorf("the person's own character did not reach the model: %q", known)
	}
	if !strings.Contains(known, "Hobb") {
		t.Errorf("the default did not reach the model: %q", known)
	}
	if strings.Contains(stranger, "known this one for years") {
		t.Errorf("a stranger was answered in somebody else's character: %q", stranger)
	}
	if !strings.Contains(stranger, "Hobb") {
		t.Errorf("the stranger did not get the default: %q", stranger)
	}
}

// Adding a character for somebody is a file appearing, not a restart.
func TestACharacterAddedWhileRunningIsUsed(t *testing.T) {
	dir := characters(t, map[string]string{"default": "You are Hobb."})

	if got := read(t, dir, someone, someoneName); strings.Contains(got, "harbour") {
		t.Fatalf("unexpected character before the file exists: %q", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "example resident.txt"),
		[]byte("You met at the harbour."), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := read(t, dir, someone, someoneName); !strings.Contains(got, "harbour") {
		t.Errorf("a character written while running was not picked up: %q", got)
	}
}
