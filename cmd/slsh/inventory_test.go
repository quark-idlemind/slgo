package main

// Inventory, driven over a grid that is not there.
//
// The pure helpers -- the flag letters, the date column, the sort --
// are next door in ls_test.go, cat_test.go and rm_test.go, and they are
// the half that never needed a grid.  This is the other half: the
// commands themselves, which resolve a path, ask AIS, and print or
// delete what came back.  None of it was reachable before there was a
// fake to answer, so all of it was checked by hand against a live
// avatar -- which for rm and emptytrash means checked by deleting
// something real, since there is no undo.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// serveAsset makes the fake answer a transfer request with these bytes,
// which is the whole of what cat reads.
//
// A notecard and a script do not come over a capability -- the content
// delivery network answers 403 for both -- so the only way to fetch one
// is the UDP transfer protocol: a request, a TransferInfo carrying the
// size, and packets.  Refused, when refused is true, is what the
// simulator says instead when the permissions do not allow it.
func serveAsset(t *testing.T, x *testShell, body string, refused bool) {
	t.Helper()
	x.grid.mu.Lock()
	defer x.grid.mu.Unlock()
	x.grid.onSend = func(m msg.Message) {
		req, ok := m.(*msg.TransferRequest)
		if !ok {
			return
		}
		id := req.TransferInfo.TransferID
		info := &msg.TransferInfo{}
		info.TransferInfo.TransferID = id
		info.TransferInfo.ChannelType = 2
		if refused {
			info.TransferInfo.Status = -3 // insufficient permissions
			x.grid.Relay(t, info)
			return
		}
		info.TransferInfo.Size = int32(len(body))
		x.grid.Relay(t, info)

		p := &msg.TransferPacket{}
		p.TransferData.TransferID = id
		p.TransferData.ChannelType = 2
		p.TransferData.Status = 1 // the last packet says so in its status
		p.TransferData.Data = []byte(body)
		x.grid.Relay(t, p)
	}
}

// refuseChanges leaves listings alone and turns down anything that
// would change something, which is what a command finds when the grid
// has decided halfway through that it will not.
func refuseChanges(t *testing.T, x *testShell) {
	t.Helper()
	x.grid.ServeCap(t, agent.InventoryCap, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			http.Error(w, "not just now", http.StatusForbidden)
			return
		}
		body, status := x.grid.inventoryRequest(r)
		if status != http.StatusOK {
			http.Error(w, body, status)
			return
		}
		w.Header().Set("Content-Type", "application/llsd+xml")
		fmt.Fprint(w, body)
	})
}

// TestCdWalksDownAndBackUp, and keeps the names rather than only the
// folder it landed in, so that the prompt says where it is.
func TestCdWalksDownAndBackUp(t *testing.T) {
	x := newTestShell(t)

	for _, c := range []struct{ line, want string }{
		{"cd Objects", "/Objects"},
		{"cd .", "/Objects"},
		{"cd ..", "/"},
		{"cd /Scripts", "/Scripts"},
		{"cd ../Objects", "/Objects"},
		{"cd /", "/"},
		// Up from the root stays at the root rather than falling off it.
		{"cd ..", "/"},
	} {
		if got := x.do(t, c.line); got != "" {
			t.Errorf("%q printed %q, and should print nothing", c.line, got)
		}
		if got := x.Pwd(); got != c.want {
			t.Errorf("after %q the working folder is %q, want %q", c.line, got, c.want)
		}
	}

	if got := x.do(t, "cd nowhere"); !strings.Contains(got, `no folder "nowhere"`) {
		t.Errorf("cd into a folder that is not there printed %q", got)
	}
	if got := x.do(t, "cd one two"); !strings.Contains(got, "usage: cd") {
		t.Errorf("cd with two paths printed %q", got)
	}
	if got := x.do(t, "cd --help"); !strings.Contains(got, "[PATH]") {
		t.Errorf("cd --help printed %q", got)
	}
	// The folder is still where it was: a cd that failed must not move.
	if got := x.Pwd(); got != "/" {
		t.Errorf("a refused cd moved the shell to %q", got)
	}
}

// TestCdWithNoPathGoesToTheRoot, which is what it says it does: "no
// path goes to the root" is the whole of its brief, and it is the
// gesture a person makes to get out of wherever they walked to.
//
// It used to stay where it was.  cd with no argument resolved the
// empty path, and an empty path resolved against the working folder is
// the working folder -- so cd on its own was the one command in the
// shell that did nothing at all.  No arguments is the root; it is not
// the same thing as the empty path, which is what ls with no argument
// wants and still gets.
func TestCdWithNoPathGoesToTheRoot(t *testing.T) {
	x := newTestShell(t)
	x.do(t, "cd /Objects")
	x.do(t, "cd")
	if got := x.Pwd(); got != "/" {
		t.Errorf("cd with no path left the shell in %q", got)
	}

	// And the empty path still means here, which is what ls with no
	// argument depends on.
	x.do(t, "cd /Objects")
	if got := x.do(t, "ls"); !strings.Contains(got, "/Objects/") {
		t.Errorf("ls with no path listed something other than here:\n%s", got)
	}
}

// TestLsPrintsOnePathPerLineSoAListingIsAScript.
//
// That is the whole layout argument: a bare listing is a list of paths
// an editor can turn into commands, and -l keeps four columns whatever
// the flags, so anything reading the id out of the third field goes on
// working.
func TestLsPrintsOnePathPerLineSoAListingIsAScript(t *testing.T) {
	x := newTestShell(t)

	got := x.do(t, "ls")
	if want := "/Objects\n/readme\n/Scripts\n/Trash\n"; got != want {
		t.Errorf("ls printed %q, want %q", got, want)
	}

	// -l is kind, date, id and path, and a folder has no date to print.
	got = x.do(t, "ls -l")
	for _, want := range []string{
		"folder     -          " + testObjects.String() + " /Objects",
		"notecard   ", testNote.String() + " /readme",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("ls -l should have a line with %q in it:\n%s", want, got)
		}
	}
	for _, line := range strings.Split(strings.TrimRight(got, "\n"), "\n") {
		if n := len(strings.Fields(line)); n != 4 {
			t.Errorf("a long listing line has %d fields, want 4: %q", n, line)
		}
	}

	// -T widens the date column and does not add one.
	got = x.do(t, "ls -T")
	if !strings.Contains(got, "T") || !strings.Contains(got, testNote.String()) {
		t.Errorf("ls -T printed:\n%s", got)
	}
	for _, line := range strings.Split(strings.TrimRight(got, "\n"), "\n") {
		if n := len(strings.Fields(line)); n != 4 {
			t.Errorf("ls -T should still be 4 fields, got %d: %q", n, line)
		}
	}

	// -r descends, and the paths it prints are still the paths it takes.
	if got := x.do(t, "ls -r"); !strings.Contains(got, "/Objects/a lamp") {
		t.Errorf("ls -r should reach into the folders:\n%s", got)
	}
	// A folder that is not the root has its name in front of everything.
	if got := x.do(t, "ls /Objects"); got != "/Objects/a lamp\n" {
		t.Errorf("ls of a folder printed %q", got)
	}

	// -t is newest first, which puts the undated folders at the end.
	got = x.do(t, "ls -t")
	if !strings.HasPrefix(got, "/readme\n") {
		t.Errorf("ls -t should start with the newest thing:\n%s", got)
	}
}

// TestLsSaysWhyItCannotList rather than printing an empty folder, which
// is what a folder that could not be read looks like otherwise.
func TestLsSaysWhyItCannotList(t *testing.T) {
	x := newTestShell(t)

	if got := x.do(t, "ls nowhere"); !strings.Contains(got, `no folder "nowhere"`) {
		t.Errorf("ls of a folder that is not there printed %q", got)
	}
	if got := x.do(t, "ls one two"); !strings.Contains(got, "only one folder at a time") {
		t.Errorf("ls of two folders printed %q", got)
	}
	if got := x.do(t, "ls --help"); !strings.Contains(got, "[PATH]") {
		t.Errorf("ls --help printed %q", got)
	}

	x.grid.capErr = errors.New("the capability went away")
	if got := x.do(t, "ls"); !strings.Contains(got, "the capability went away") {
		t.Errorf("ls should report the failure, got %q", got)
	}
	// And the same failure a level down, where it is the path being
	// resolved rather than the folder being read.
	if got := x.do(t, "ls Objects"); !strings.Contains(got, "the capability went away") {
		t.Errorf("ls of a subfolder should report the failure, got %q", got)
	}
}

// TestKindOfNamesWhatAThingIsInAWord, and says the number when it has
// no word for it -- an asset type this build has never heard of is
// exactly what a new kind of inventory looks like, and inventing a name
// for it would hide that.
func TestKindOfNamesWhatAThingIsInAWord(t *testing.T) {
	for _, c := range []struct {
		e    sl.Entry
		want string
	}{
		{sl.Entry{Folder: true}, "folder"},
		{sl.Entry{IsLink: true, Type: int(sl.AssetNotecard)}, "link"},
		{sl.Entry{Type: int(sl.AssetTexture)}, "texture"},
		{sl.Entry{Type: int(sl.AssetSound)}, "sound"},
		{sl.Entry{Type: int(sl.AssetSoundWAV)}, "sound"},
		{sl.Entry{Type: int(sl.AssetLandmark)}, "landmark"},
		{sl.Entry{Type: int(sl.AssetClothing)}, "clothing"},
		{sl.Entry{Type: int(sl.AssetObject)}, "object"},
		{sl.Entry{Type: int(sl.AssetNotecard)}, "notecard"},
		{sl.Entry{Type: int(sl.AssetLSLText)}, "script"},
		{sl.Entry{Type: int(sl.AssetBodypart)}, "bodypart"},
		{sl.Entry{Type: int(sl.AssetAnimation)}, "animation"},
		{sl.Entry{Type: int(sl.AssetGesture)}, "gesture"},
		{sl.Entry{Type: int(sl.AssetMesh)}, "mesh"},
		{sl.Entry{Type: int(sl.AssetSettings)}, "settings"},
		{sl.Entry{Type: int(sl.AssetMaterial)}, "material"},
		{sl.Entry{Type: int(sl.AssetGLTF)}, "type58"},
	} {
		if got := kindOf(c.e); got != c.want {
			t.Errorf("kindOf(%+v) = %q, want %q", c.e, got, c.want)
		}
	}
}

// TestSortByTimeSettlesTwoNamesThatDifferOnlyInCase.
//
// Sorting ignores case, so "SAME" and "same" tie on every test above
// this one -- and a tie is an order that changes between listings,
// which is what makes a listing worth writing to a file.
func TestSortByTimeSettlesTwoNamesThatDifferOnlyInCase(t *testing.T) {
	es := []sl.Entry{
		{Name: "same", Created: 5, ID: msg.UUID{15: 2}},
		{Name: "SAME", Created: 5, ID: msg.UUID{15: 1}},
	}
	sortByTime(es)
	if es[0].Name != "SAME" {
		t.Errorf("the exact names should settle it, got %q first", es[0].Name)
	}
}

// TestCatPullsTheTextOutOfTheContainer.
//
// A notecard on the wire is not its text: it is a header, an embedded
// items block and then the text, so printing what arrived would print
// four lines of bookkeeping before every notecard.
func TestCatPullsTheTextOutOfTheContainer(t *testing.T) {
	x := newTestShell(t)
	serveAsset(t, x, "Linden text version 2\n{\nLLEmbeddedItems version 1\n{\ncount 0\n}\n"+
		"Text length 24\nthe text of the notecard\n}\n", false)

	if got, want := x.do(t, "cat readme"), "the text of the notecard\n"; got != want {
		t.Errorf("cat printed %q, want %q", got, want)
	}

	// A script is not in a container and is printed as it stands.
	serveAsset(t, x, "default { state_entry() { } }\n\n", false)
	if got, want := x.do(t, "cat /Scripts/probe"), "default { state_entry() { } }\n"; got != want {
		t.Errorf("cat of a script printed %q, want %q", got, want)
	}

	// A notecard the container of which cannot be read is printed as it
	// stands too, which is better than printing nothing.
	serveAsset(t, x, "not a container at all", false)
	if got, want := x.do(t, "cat readme"), "not a container at all\n"; got != want {
		t.Errorf("cat of an unreadable container printed %q, want %q", got, want)
	}
}

// TestCatSaysWhoRefusedIt: a transfer the simulator turns down is a
// permissions answer and not an empty file, and cat has to say which.
func TestCatSaysWhoRefusedIt(t *testing.T) {
	x := newTestShell(t)
	serveAsset(t, x, "", true)

	if got := x.do(t, "cat readme"); !strings.Contains(got, "insufficient permissions") {
		t.Errorf("cat of a refused asset printed %q", got)
	}

	if got := x.do(t, "cat Objects"); !strings.Contains(got, "is a folder") {
		t.Errorf("cat of a folder printed %q", got)
	}
	if got := x.do(t, "cat nothing-of-the-sort"); !strings.Contains(got, "nothing called") {
		t.Errorf("cat of nothing printed %q", got)
	}
	if got := x.do(t, "cat nowhere/readme"); !strings.Contains(got, `no folder "nowhere"`) {
		t.Errorf("cat under a folder that is not there printed %q", got)
	}
	for _, line := range []string{"cat", "cat one two"} {
		if got := x.do(t, line); !strings.Contains(got, "usage: cat PATH") {
			t.Errorf("%q printed %q", line, got)
		}
	}
	if got := x.do(t, "cat --help"); !strings.Contains(got, "PATH") {
		t.Errorf("cat --help printed %q", got)
	}

	x.grid.capErr = errors.New("the capability went away")
	if got := x.do(t, "cat readme"); !strings.Contains(got, "the capability went away") {
		t.Errorf("cat should report a folder it could not read, got %q", got)
	}
}

// TestNotecardTextNeedsBothHalvesOfTheHeader, and says so rather than
// returning half a notecard when it has only half a container.
func TestNotecardTextNeedsBothHalvesOfTheHeader(t *testing.T) {
	if got, ok := notecardText([]byte("Text length 5\nhello\n}\n")); !ok || got != "hello" {
		t.Errorf("notecardText = %q %v, want %q true", got, ok, "hello")
	}
	// No closing brace: everything after the length line is the text.
	if got, ok := notecardText([]byte("Text length 5\nhello")); !ok || got != "hello" {
		t.Errorf("notecardText without a brace = %q %v", got, ok)
	}
	if _, ok := notecardText([]byte("nothing that looks like a notecard")); ok {
		t.Error("something that is not a container should not be read as one")
	}
	// The length line with nothing after it is a truncated container.
	if _, ok := notecardText([]byte("Text length 5")); ok {
		t.Error("a header with no newline after it should not be read as one")
	}
}

// TestMkdirPrintsTheIdItChose.
//
// The id is chosen here rather than by the grid -- the message carries
// the id the folder is to have -- so printing it is the one moment the
// caller can learn it without listing the parent again.
func TestMkdirPrintsTheIdItChose(t *testing.T) {
	x := newTestShell(t)
	// The grid makes the folder when it is asked to, which is what
	// CreateFolder then waits to see.
	x.grid.onSend = func(m msg.Message) {
		f, ok := m.(*msg.CreateInventoryFolder)
		if !ok {
			return
		}
		x.grid.mu.Lock()
		defer x.grid.mu.Unlock()
		parent := findDir(x.grid.inv, f.FolderData.ParentID)
		if parent == nil {
			return
		}
		parent.Dirs = append(parent.Dirs, &invDir{
			ID: f.FolderData.FolderID, Name: strings.TrimRight(string(f.FolderData.Name), "\x00"),
		})
	}

	got := strings.TrimSpace(x.do(t, `mkdir "/somewhere new"`))
	id, err := msg.ParseUUID(got)
	if err != nil {
		t.Fatalf("mkdir printed %q, want the id of the new folder", got)
	}
	if listed := x.do(t, "ls"); !strings.Contains(listed, "/somewhere new") {
		t.Errorf("the folder should be there afterwards:\n%s", listed)
	}
	// And it is the id the folder actually has.
	if listed := x.do(t, "ls -l"); !strings.Contains(listed, id.String()) {
		t.Errorf("the id printed is not the folder's:\n%s", listed)
	}
}

// TestMkdirNeedsSomewhereToPutItAndSomethingToCallIt.
func TestMkdirNeedsSomewhereToPutItAndSomethingToCallIt(t *testing.T) {
	x := newTestShell(t)

	for _, line := range []string{"mkdir", "mkdir one two"} {
		if got := x.do(t, line); !strings.Contains(got, "usage: mkdir PATH") {
			t.Errorf("%q printed %q", line, got)
		}
	}
	if got := x.do(t, "mkdir /"); !strings.Contains(got, "no name given") {
		t.Errorf("mkdir of the root printed %q", got)
	}
	if got := x.do(t, "mkdir nowhere/deeper"); !strings.Contains(got, `no folder "nowhere"`) {
		t.Errorf("mkdir under a folder that is not there printed %q", got)
	}
	if got := x.do(t, "mkdir --help"); !strings.Contains(got, "PATH") {
		t.Errorf("mkdir --help printed %q", got)
	}

	x.grid.sendErr = errors.New("the circuit is down")
	if got := x.do(t, "mkdir anywhere"); !strings.Contains(got, "the circuit is down") {
		t.Errorf("mkdir should report a refused send, got %q", got)
	}
}

// TestMvMovesOrRenamesDependingOnTheDestination.
//
// One command for both because they are one message for an item: a
// destination that names a folder is a move, and anything else is a new
// name where it stands.
func TestMvMovesOrRenamesDependingOnTheDestination(t *testing.T) {
	x := newTestShell(t)

	// An item into a folder.
	if got := x.do(t, "mv readme Objects"); got != "" {
		t.Errorf("mv printed %q, and should print nothing", got)
	}
	moved, ok := x.grid.Sent()[0].(*msg.MoveInventoryItem)
	if !ok {
		t.Fatalf("moving an item sent %T", x.grid.Sent()[0])
	}
	if moved.InventoryData[0].FolderID != testObjects {
		t.Errorf("the item went to %v, want Objects", moved.InventoryData[0].FolderID)
	}

	// A folder into a folder, which is a different message.
	if got := x.do(t, "mv Scripts Objects"); got != "" {
		t.Errorf("mv of a folder printed %q", got)
	}
	if _, ok := x.grid.Sent()[1].(*msg.MoveInventoryFolder); !ok {
		t.Errorf("moving a folder sent %T", x.grid.Sent()[1])
	}

	// A plain name renames the folder where it is.
	if got := x.do(t, "mv Scripts Snippets"); got != "" {
		t.Errorf("renaming a folder printed %q", got)
	}
	if got := x.do(t, "ls"); !strings.Contains(got, "/Snippets") {
		t.Errorf("the folder should have the new name:\n%s", got)
	}
}

// TestMvRenamesAnItemAndReadsItBack.
//
// Renaming an item is the AIS path, which answers and is then checked
// by reading the item again -- so this waits the second the read-back
// costs rather than believing the request.
func TestMvRenamesAnItemAndReadsItBack(t *testing.T) {
	t.Parallel()
	x := newTestShell(t)

	if got := x.do(t, "mv readme notes"); got != "" {
		t.Errorf("renaming an item printed %q", got)
	}
	if got := x.do(t, "ls"); !strings.Contains(got, "/notes") || strings.Contains(got, "/readme") {
		t.Errorf("the item should have the new name and only that:\n%s", got)
	}
}

// TestMvNeedsAFolderOrAPlainName, and says which it looked for: a
// destination with a slash in it that names no folder is a typo, not a
// name to rename to.
func TestMvNeedsAFolderOrAPlainName(t *testing.T) {
	x := newTestShell(t)

	for _, line := range []string{"mv", "mv one", "mv one two three"} {
		if got := x.do(t, line); !strings.Contains(got, "usage: mv PATH DEST") {
			t.Errorf("%q printed %q", line, got)
		}
	}
	if got := x.do(t, "mv nothing-of-the-sort elsewhere"); !strings.Contains(got, "nothing called") {
		t.Errorf("mv of nothing printed %q", got)
	}
	if got := x.do(t, "mv readme nowhere/notes"); !strings.Contains(got, "is not a plain name") {
		t.Errorf("mv to a path that is not a folder printed %q", got)
	}
	if got := x.do(t, "mv --help"); !strings.Contains(got, "PATH DEST") {
		t.Errorf("mv --help printed %q", got)
	}

	refuseChanges(t, x)
	if got := x.do(t, "mv Scripts Snippets"); !strings.Contains(got, "403") {
		t.Errorf("a refused rename should be reported, got %q", got)
	}
}

// stock puts a run of items of one name in the root, which is the
// situation --remove-all-copies exists for: a folder may hold a dozen
// things wearing the same name, and a path names the first of them.
func stock(t *testing.T, x *testShell, name string, n int) {
	t.Helper()
	x.grid.mu.Lock()
	defer x.grid.mu.Unlock()
	for i := 0; i < n; i++ {
		x.grid.inv.Items = append(x.grid.inv.Items, &invItem{
			ID:      msg.UUID{0: byte(i + 1), 15: 0xdd},
			Name:    name,
			Type:    int(sl.AssetNotecard),
			Created: 1754000300 + int64(i),
		})
	}
}

// TestRmTakesTheFirstOfANameUnlessAskedForAllOfThem.
//
// Deleting is permanent here, so a path that turns out to mean twelve
// items is not something to discover afterwards: the careful reading is
// the default and the other one has to be spelled out.
func TestRmTakesTheFirstOfANameUnlessAskedForAllOfThem(t *testing.T) {
	x := newTestShell(t)
	stock(t, x, "dup", 3)

	if got := x.do(t, "rm dup"); got != "" {
		t.Errorf("rm of one thing printed %q, and should print nothing", got)
	}
	if n := strings.Count(x.do(t, "ls"), "/dup\n"); n != 2 {
		t.Errorf("rm took %d of the three copies", 3-n)
	}

	// The count is the only evidence the flag did what was wanted.
	if got := x.do(t, "rm --remove-all-copies dup"); got != "dup: removed 2 copies\n" {
		t.Errorf("rm --remove-all-copies printed %q", got)
	}
	if got := x.do(t, "ls"); strings.Contains(got, "/dup") {
		t.Errorf("all of them should be gone:\n%s", got)
	}

	// A folder goes as a folder, and takes what is in it.
	if got := x.do(t, "rm Objects"); got != "" {
		t.Errorf("rm of a folder printed %q", got)
	}
	if got := x.do(t, "ls -r"); strings.Contains(got, "/Objects") {
		t.Errorf("the folder should be gone:\n%s", got)
	}
}

// TestRmRefusesTheStarUntilItIsSpeltOut.
//
// * is not a glob -- the shell has no pattern matching -- and it is the
// one pattern rm needs.  Taken to mean one arbitrary item it would be
// surprising; taken to mean all of them unasked it would be worse.
func TestRmRefusesTheStarUntilItIsSpeltOut(t *testing.T) {
	x := newTestShell(t)
	stock(t, x, "dup", 2)

	got := x.do(t, "rm *")
	if !strings.Contains(got, "means every item in this folder") {
		t.Errorf("rm * printed %q", got)
	}
	if !strings.Contains(got, "rm --remove-all-copies *") {
		t.Errorf("the refusal should say what to type instead, got %q", got)
	}

	// Said in full it empties the folder of items and leaves the
	// folders alone.
	if got := x.do(t, "rm --remove-all-copies *"); got != "*: removed 3 items\n" {
		t.Errorf("rm --remove-all-copies * printed %q", got)
	}
	listed := x.do(t, "ls")
	if strings.Contains(listed, "/dup") || strings.Contains(listed, "/readme") {
		t.Errorf("the items should be gone:\n%s", listed)
	}
	if !strings.Contains(listed, "/Objects") {
		t.Errorf("emptying a folder of items must leave its folders:\n%s", listed)
	}
}

// TestRmSaysWhichPathFailedAndStops.
//
// Several paths are removed in one command, so a failure has to name
// the one it happened on: "rm a b c" that said only "forbidden" would
// leave nobody knowing how much of it happened.
func TestRmSaysWhichPathFailedAndStops(t *testing.T) {
	x := newTestShell(t)

	if got := x.do(t, "rm"); !strings.Contains(got, "usage: rm") {
		t.Errorf("rm with no path printed %q", got)
	}
	if got := x.do(t, "rm --help"); !strings.Contains(got, "remove-all-copies") {
		t.Errorf("rm --help printed %q", got)
	}
	if got := x.do(t, "rm nothing-of-the-sort"); !strings.Contains(got, "nothing called") {
		t.Errorf("rm of nothing printed %q", got)
	}
	if got := x.do(t, `rm ""`); !strings.Contains(got, "no path given") {
		t.Errorf("rm of an empty path printed %q", got)
	}

	refuseChanges(t, x)
	if got := x.do(t, "rm readme"); !strings.Contains(got, "readme: ") || !strings.Contains(got, "403") {
		t.Errorf("a refused removal should name the path, got %q", got)
	}
	if got := x.do(t, "rm Objects"); !strings.Contains(got, "Objects: ") {
		t.Errorf("a refused folder removal should name the path, got %q", got)
	}

	x.grid.capErr = errors.New("the capability went away")
	if got := x.do(t, "rm --remove-all-copies *"); !strings.Contains(got, "the capability went away") {
		t.Errorf("rm * should report a folder it could not read, got %q", got)
	}
}

// TestEmptyTrashCountsWhatItThrewAway, because afterwards there is
// nothing left to count.
func TestEmptyTrashCountsWhatItThrewAway(t *testing.T) {
	x := newTestShell(t)

	if got, want := x.do(t, "emptytrash"), "the trash is already empty\n"; got != want {
		t.Errorf("emptytrash printed %q, want %q", got, want)
	}

	x.grid.mu.Lock()
	trash := findDir(x.grid.inv, testTrash)
	trash.Items = []*invItem{
		{ID: msg.UUID{15: 0x71}, Name: "thrown away", Type: int(sl.AssetNotecard)},
		{ID: msg.UUID{15: 0x72}, Name: "also thrown away", Type: int(sl.AssetNotecard)},
	}
	trash.Dirs = []*invDir{{ID: msg.UUID{15: 0x73}, Name: "a folder in the trash"}}
	x.grid.mu.Unlock()

	if got, want := x.do(t, "emptytrash"), "emptied the trash: 3\n"; got != want {
		t.Errorf("emptytrash printed %q, want %q", got, want)
	}
	if got := x.do(t, "ls -r /Trash"); got != "" {
		t.Errorf("the trash should be empty afterwards:\n%s", got)
	}
}

// TestEmptyTrashSaysWhyItCannot rather than reporting an emptiness it
// did not cause.
func TestEmptyTrashSaysWhyItCannot(t *testing.T) {
	x := newTestShell(t)

	if got := x.do(t, "emptytrash please"); !strings.Contains(got, "usage: emptytrash") {
		t.Errorf("emptytrash with an argument printed %q", got)
	}
	if got := x.do(t, "emptytrash --help"); !strings.Contains(got, "emptytrash") {
		t.Errorf("emptytrash --help printed %q", got)
	}

	// A trash that cannot be listed is not a trash that is empty.
	x.grid.ServeCap(t, agent.InventoryCap, func(w http.ResponseWriter, r *http.Request) {
		if capFolderID(r.URL.Path) == testTrash {
			http.Error(w, "the trash is not answering", http.StatusInternalServerError)
			return
		}
		body, status := x.grid.inventoryRequest(r)
		if status != http.StatusOK {
			http.Error(w, body, status)
			return
		}
		w.Header().Set("Content-Type", "application/llsd+xml")
		fmt.Fprint(w, body)
	})
	if got := x.do(t, "emptytrash"); !strings.Contains(got, "the trash is not answering") {
		t.Errorf("emptytrash should report a trash it could not read, got %q", got)
	}

	// One that cannot be emptied, having been counted.
	y := newTestShell(t)
	y.grid.mu.Lock()
	findDir(y.grid.inv, testTrash).Items = []*invItem{{ID: msg.UUID{15: 0x71}, Name: "thrown away"}}
	y.grid.mu.Unlock()
	refuseChanges(t, y)
	if got := y.do(t, "emptytrash"); !strings.Contains(got, "403") {
		t.Errorf("emptytrash should report a refusal, got %q", got)
	}

	y.grid.capErr = errors.New("the capability went away")
	if got := y.do(t, "emptytrash"); !strings.Contains(got, "the capability went away") {
		t.Errorf("emptytrash should report a trash it could not find, got %q", got)
	}
}

// TestFindLooksFromHereDown, and prints paths rather than names, so
// that what it prints is what the other commands take.
func TestFindLooksFromHereDown(t *testing.T) {
	x := newTestShell(t)

	if got, want := x.do(t, "find lamp"), "/Objects/a lamp\n"; got != want {
		t.Errorf("find printed %q, want %q", got, want)
	}
	// The match ignores case, as everything at this prompt does.
	if got := x.do(t, "find LAMP"); got != "/Objects/a lamp\n" {
		t.Errorf("find should ignore case, got %q", got)
	}
	// Given a folder, the paths are still from the root.
	if got, want := x.do(t, "find a /Objects"), "/Objects/a lamp\n"; got != want {
		t.Errorf("find in a folder printed %q, want %q", got, want)
	}
	if got := x.do(t, "find nothing-of-the-sort"); got != "" {
		t.Errorf("find that matches nothing should print nothing, got %q", got)
	}

	for _, line := range []string{"find", "find a b c"} {
		if got := x.do(t, line); !strings.Contains(got, "usage: find TEXT") {
			t.Errorf("%q printed %q", line, got)
		}
	}
	if got := x.do(t, "find --help"); !strings.Contains(got, "TEXT") {
		t.Errorf("find --help printed %q", got)
	}
	if got := x.do(t, "find a nowhere"); !strings.Contains(got, `no folder "nowhere"`) {
		t.Errorf("find in a folder that is not there printed %q", got)
	}

	x.grid.capErr = errors.New("the capability went away")
	if got := x.do(t, "find a"); !strings.Contains(got, "the capability went away") {
		t.Errorf("find should report the failure, got %q", got)
	}
}

// TestAnIdNamesOneThingWhereverItIs.
//
// Names are not unique -- a folder may hold eighteen things called the
// same thing -- so ls -l prints the id beside the path, and every
// command that takes a path takes one of those instead.  It is looked
// for here first because that is nearly always where it is, and then
// anywhere below the root.
func TestAnIdNamesOneThingWhereverItIs(t *testing.T) {
	x := newTestShell(t)
	serveAsset(t, x, "the script", false)

	// In the folder the shell is standing in.
	if got, want := x.do(t, "cat "+testNote.String()), "the script\n"; got != want {
		t.Errorf("cat by id printed %q, want %q", got, want)
	}
	// And two folders down, which costs the whole tree to find.
	if got, want := x.do(t, "cat "+testProbe.String()), "the script\n"; got != want {
		t.Errorf("cat by an id further down printed %q, want %q", got, want)
	}

	nowhere := msg.MustParseUUID("c75d7e57-7e57-c0de-b372-00000000dead")
	if got := x.do(t, "cat "+nowhere.String()); !strings.Contains(got, "nothing here has the id") {
		t.Errorf("cat of an id that is nowhere printed %q", got)
	}

	x.grid.capErr = errors.New("the capability went away")
	if got := x.do(t, "cat "+testProbe.String()); !strings.Contains(got, "the capability went away") {
		t.Errorf("cat by id should report an inventory it cannot read, got %q", got)
	}
}

// TestPwdIsWhereTheNextRelativePathStartsFrom.
func TestPwdIsWhereTheNextRelativePathStartsFrom(t *testing.T) {
	x := newTestShell(t)

	if got, want := x.do(t, "pwd"), "/\n"; got != want {
		t.Errorf("pwd printed %q, want %q", got, want)
	}
	x.do(t, "cd /Objects")
	if got, want := x.do(t, "pwd"), "/Objects\n"; got != want {
		t.Errorf("pwd printed %q, want %q", got, want)
	}
	if got := x.do(t, "pwd --help"); !strings.Contains(got, "pwd") {
		t.Errorf("pwd --help printed %q", got)
	}
}

// TestARelativePathIsFromTheWorkingFolder, which is what makes cd worth
// having at all: the commands that take a path take a short one.
func TestARelativePathIsFromTheWorkingFolder(t *testing.T) {
	x := newTestShell(t)
	x.do(t, "cd Objects")

	if got, want := x.do(t, "ls"), "/Objects/a lamp\n"; got != want {
		t.Errorf("ls in a folder printed %q, want %q", got, want)
	}
	if got := x.do(t, "find lamp"); got != "/Objects/a lamp\n" {
		t.Errorf("find in a folder printed %q", got)
	}
	// Up and over, without going home first.
	if got := x.do(t, "ls ../Scripts"); got != "/Scripts/probe\n" {
		t.Errorf("ls of a sibling folder printed %q", got)
	}

	// A shell standing in a folder that has gone away can still say
	// where it thinks it is, and everything else fails there.
	x.mu.Lock()
	x.cwd = []string{"a folder that went away"}
	x.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := x.Do(ctx, "ls"); err == nil {
		t.Error("a working folder that is not there should fail rather than list the root")
	}
}

// ---------------------------------------------------------------- save

// itemWrite is what one of the two capabilities that write to an
// existing item was asked for.
//
// Both halves are kept, because a save is two requests and each says
// something different: the first names the item being written and is
// the only place the choice between notecard and script shows, and the
// second carries the bytes, which is the only place the file shows.
type itemWrite struct {
	mu           sync.Mutex
	asked, wrote []byte
}

// seen is the description and then the asset, as text.
func (u *itemWrite) seen() (asked, wrote string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return string(u.asked), string(u.wrote)
}

// serveItemWrite answers UpdateNotecardAgentInventory or
// UpdateScriptAgent, which are the same two step upload every
// capability uses: the first request describes what is being written
// and answers with somewhere to put the bytes, and what comes back
// from THERE is the verdict.
func serveItemWrite(t *testing.T, x *testShell, capName, verdict string) *itemWrite {
	t.Helper()
	u := &itemWrite{}
	dest := x.grid.ServeCap(t, "slgo test bytes for "+capName, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.wrote = b
		u.mu.Unlock()
		w.Header().Set("Content-Type", "application/llsd+xml")
		io.WriteString(w, verdict)
	})
	x.grid.ServeCap(t, capName, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.asked = b
		u.mu.Unlock()
		w.Header().Set("Content-Type", "application/llsd+xml")
		fmt.Fprintf(w, `<llsd><map><key>state</key><string>upload</string>`+
			`<key>uploader</key><string>%s/upload</string></map></llsd>`, dest.URL)
	})
	return u
}

// compileOK is what the capability says about a script it liked, and
// savedOK what it says about a notecard, which is not compiled at all.
const (
	compileOK = `<llsd><map><key>state</key><string>complete</string>` +
		`<key>compiled</key><boolean>1</boolean></map></llsd>`
	savedOK = `<llsd><map><key>state</key><string>complete</string></map></llsd>`
)

// aFile puts something on disk for save to read.
func aFile(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestSaveWritesTheFileIntoTheItemThatIsAlreadyThere.
//
// The direction is the whole point: the file is read and the item is
// written, which is the opposite of cat and get, and a command that had
// them the other way round would quietly overwrite the file somebody
// meant to upload.
//
// Which capability is used follows from the item's type and from
// nothing else, so both are served here and each save has to reach
// exactly one of them.  A notecard goes up wrapped in the container
// format and a script goes up as it stands, which is the other thing
// the two calls do differently.
func TestSaveWritesTheFileIntoTheItemThatIsAlreadyThere(t *testing.T) {
	x := newTestShell(t)
	note := serveItemWrite(t, x, "UpdateNotecardAgentInventory", savedOK)
	script := serveItemWrite(t, x, "UpdateScriptAgent", compileOK)

	got := x.do(t, "save "+aFile(t, "notes.txt", "one\ntwo\n")+" readme")
	if want := "readme: 8 bytes written\n"; got != want {
		t.Errorf("save of a notecard printed %q, want %q", got, want)
	}
	asked, wrote := note.seen()
	if !strings.Contains(asked, testNote.String()) {
		t.Errorf("the notecard written was %q, want the item readme names", asked)
	}
	if !strings.Contains(wrote, "one\ntwo\n") || !strings.Contains(wrote, "Text length 8") {
		t.Errorf("the notecard asset was %q, want the file inside the container", wrote)
	}
	if _, wrote := script.seen(); wrote != "" {
		t.Errorf("saving a notecard wrote %q through the script capability", wrote)
	}

	// A script takes the other call, and says what the compiler thought
	// -- saving to inventory compiles even though no object is involved.
	const source = "default { state_entry() { } }\n"
	got = x.do(t, "save "+aFile(t, "hello.lsl", source)+" /Scripts/probe")
	if want := "probe: 30 bytes written, and it compiled\n"; got != want {
		t.Errorf("save of a script printed %q, want %q", got, want)
	}
	asked, wrote = script.seen()
	if !strings.Contains(asked, testProbe.String()) {
		t.Errorf("the script written was %q, want the item probe names", asked)
	}
	if wrote != source {
		t.Errorf("the script asset was %q, want the file unchanged", wrote)
	}

	// A script in inventory is not running anywhere, so nothing here
	// may say it is: an object is the only place a script runs.
	if strings.Contains(got, "running") {
		t.Errorf("save said something about running: %q", got)
	}
}

// TestSaveOfAScriptThatWillNotCompileSaysItIsWrittenAnyway.
//
// That is what happens: the save succeeds and the compile fails, in
// that order, so the item now holds source that will not run.  Saying
// only "did not compile" would leave somebody thinking their old script
// was still in there, and failing outright would report the opposite of
// what the grid did.  The compiler's complaint is the whole answer to
// why, so it is printed rather than counted.
func TestSaveOfAScriptThatWillNotCompileSaysItIsWrittenAnyway(t *testing.T) {
	x := newTestShell(t)
	u := serveItemWrite(t, x, "UpdateScriptAgent",
		`<llsd><map><key>state</key><string>complete</string>`+
			`<key>compiled</key><boolean>0</boolean>`+
			`<key>errors</key><array><string>(1,9) : ERROR : Syntax error</string></array>`+
			`</map></llsd>`)

	const source = "default { oops }\n"
	line := "save " + aFile(t, "broken.lsl", source) + " /Scripts/probe"
	got := x.do(t, line)
	if !strings.Contains(got, "17 bytes written") || !strings.Contains(got, "did not compile") {
		t.Errorf("save of a script that will not compile printed %q", got)
	}
	if !strings.Contains(got, "(1,9) : ERROR : Syntax error") {
		t.Errorf("the compiler's complaint was not printed: %q", got)
	}
	if _, wrote := u.seen(); wrote != source {
		t.Errorf("the item was not written: %q", wrote)
	}
	if err := x.Do(context.Background(), line); err != nil {
		t.Errorf("a save whose compile failed reported %v, and the item was written all the same", err)
	}

	// The capability says this when it is unhappy without failing, and
	// it is the only explanation there will be.
	y := newTestShell(t)
	serveItemWrite(t, y, "UpdateNotecardAgentInventory",
		`<llsd><map><key>state</key><string>error</string>`+
			`<key>message</key><string>Not enough space in inventory</string></map></llsd>`)
	got = y.do(t, "save "+aFile(t, "notes.txt", "hello\n")+" readme")
	if !strings.Contains(got, "Not enough space in inventory") {
		t.Errorf("save printed %q, and said nothing of what the capability complained of", got)
	}
}

// TestSaveTargetPinsWhatCanBeWrittenTo.
//
// It stands here rather than with the other pure helpers because it is
// the whole of what decides which capability a save uses, and getting
// it wrong is not a refusal but a notecard written through the script
// compiler.  A kind that cannot be written has to say what does work,
// and name the command where there is one: an image is "put", which is
// a different command because it costs L$.
func TestSaveTargetPinsWhatCanBeWrittenTo(t *testing.T) {
	for _, c := range []struct {
		what string
		e    sl.Entry
		kind sl.AssetType
		want string // a fragment of the refusal, or "" to be writable
	}{
		{"a notecard", sl.Entry{Name: "readme", Type: int(sl.AssetNotecard)}, sl.AssetNotecard, ""},
		{"a script", sl.Entry{Name: "probe", Type: int(sl.AssetLSLText)}, sl.AssetLSLText, ""},
		{"a script whose asset id the grid withheld",
			sl.Entry{Name: "probe", Type: int(sl.AssetLSLText)}, sl.AssetLSLText, ""},
		{"a script of the long dead kind, which is a script still",
			sl.Entry{Name: "old", Type: int(sl.AssetScriptLegacy)}, sl.AssetLSLText, ""},
		{"a texture, which has a command of its own",
			sl.Entry{Name: "a picture", Type: int(sl.AssetTexture)}, 0, `"put"`},
		{"an object, which nothing here uploads",
			sl.Entry{Name: "a lamp", Type: int(sl.AssetObject)}, 0, "nothing here uploads an object"},
		{"a landmark, likewise",
			sl.Entry{Name: "home", Type: int(sl.AssetLandmark)}, 0, "nothing here uploads a landmark"},
		{"a folder, whose type is what it likes to hold",
			sl.Entry{Name: "Scripts", Folder: true, Type: int(sl.AssetLSLText)}, 0, "is a folder"},
		{"a link, which is not the item it names",
			sl.Entry{Name: "readme", Type: int(sl.AssetNotecard), IsLink: true}, 0, "is a link"},
	} {
		kind, err := saveTarget(c.e)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: should be writable, got %v", c.what, err)
		case c.want == "" && kind != c.kind:
			t.Errorf("%s: goes to %v, want %v", c.what, kind, c.kind)
		case c.want != "" && err == nil:
			t.Errorf("%s: should be refused, was not", c.what)
		case c.want != "" && !strings.Contains(err.Error(), c.want):
			t.Errorf("%s: refusal should mention %q, got %v", c.what, c.want, err)
		}
		if c.want != "" && err != nil && !strings.Contains(err.Error(), "notecard") {
			t.Errorf("%s: refusal should say what save does write, got %v", c.what, err)
		}
	}
}

// TestSaveRefusesBeforeItWritesAnything.
//
// Everything that can be checked without the grid is checked before a
// byte goes anywhere, because a save cannot be undone: the item's
// previous asset is gone.  A filename that is not there is the likeliest
// mistake of the lot -- it is the argument that is not an inventory path
// -- and it must not reach the capability.
func TestSaveRefusesBeforeItWritesAnything(t *testing.T) {
	x := newTestShell(t)
	note := serveItemWrite(t, x, "UpdateNotecardAgentInventory", savedOK)
	file := aFile(t, "notes.txt", "one\n")

	for _, c := range []struct{ line, want string }{
		{"save", "usage: save FILE PATH"},
		{"save " + file, "usage: save FILE PATH"},
		{"save " + filepath.Join(filepath.Dir(file), "not-here.txt") + " readme", "not-here.txt"},
		{"save " + file + " nowhere", `nothing called "nowhere"`},
		{"save " + file + " /Objects/a lamp", "nothing here uploads an object"},
		{"save " + file + " /Scripts", "is a folder"},
	} {
		if got := x.do(t, c.line); !strings.Contains(got, c.want) {
			t.Errorf("%q printed %q, want %q in it", c.line, got, c.want)
		}
	}
	if _, wrote := note.seen(); wrote != "" {
		t.Errorf("a refused save wrote %q", wrote)
	}

	// And the same file into something it can write, so that none of
	// the refusals above is a save failing for some other reason.  The
	// lamp was found at all because the path takes the rest of the line
	// as drop's does, spaces and all.
	if got := x.do(t, "save "+file+" readme"); !strings.Contains(got, "readme: 4 bytes written") {
		t.Errorf("save printed %q", got)
	}
	if got := x.do(t, "save --help"); !strings.Contains(got, "FILE PATH") {
		t.Errorf("save --help printed %q", got)
	}
}
