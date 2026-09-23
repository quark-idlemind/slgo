package main

// What is inside a rezzed object, over a grid that is not there.
//
// Listing inside an object is still not exercised here; renaming and
// deleting are, at the end, for the nil the two of them used to
// dereference.  The rest, since AnswerInside taught the fake to speak
// an object's contents, is start and stop -- which need the contents and
// two more messages besides, and which make a claim about the world that
// nothing replies to.  That claim is the point of most of what follows:
// SetScriptRunning is answered by silence, so every "started" printed
// here has to have been established by asking, and a command that
// printed it for having asked would pass no test in this file.

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// The prims and the scripts these tests work with.
var (
	aBox      = msg.MustParseUUID("e2a17e57-7e57-c0de-b864-09116b98f7c6")
	aGreeter  = msg.MustParseUUID("df8f7e57-7e57-c0de-8a30-80d6e78abfcb")
	aListener = msg.MustParseUUID("e0b57e57-7e57-c0de-398e-bee79e38dd6f")
	aWatcher  = msg.MustParseUUID("e1307e57-7e57-c0de-7684-a7b26150cdf3")
	aReadme   = msg.MustParseUUID("e1697e57-7e57-c0de-1068-228efc6bfe1b")
)

// aBoxHolding puts one object in the region and fills it, which is the
// setup every test below wants.
func aBoxHolding(t *testing.T, held ...*heldItem) *testShell {
	t.Helper()
	x := newTestShell(t)
	standing(x, aPrim(aBox, 21, "Box1", 0))
	x.grid.AnswerInside(t, aBox, held...)
	return x
}

// TestLsInTakesNoPath: an object holds no folders, so a path alongside
// --in is a person expecting something this cannot do.
func TestLsInTakesNoPath(t *testing.T) {
	x := newTestShell(t)
	got := x.do(t, "ls --in Box1 Objects/thing")
	if !strings.Contains(got, "takes no path") {
		t.Errorf("ls --in with a path printed %q", got)
	}
}

// TestDropWantsBoth: an object and an item, since neither can be
// guessed at.
func TestDropWantsBoth(t *testing.T) {
	x := newTestShell(t)
	if got := x.do(t, "drop"); !strings.Contains(got, "usage: drop") {
		t.Errorf("drop with nothing printed %q", got)
	}
	if got := x.do(t, "drop Box1"); !strings.Contains(got, "usage: drop") {
		t.Errorf("drop with only an object printed %q", got)
	}
}

// TestNewRefusesAKindItCannotMake: a texture goes up through the upload
// that charges L$, and quietly making a notecard instead would be a
// surprise of the worst kind.
func TestNewRefusesAKindItCannotMake(t *testing.T) {
	x := newTestShell(t)
	got := x.do(t, "new --kind texture Objects/thing")
	if !strings.Contains(got, "notecard or script") {
		t.Errorf("new --kind texture printed %q", got)
	}
}

// TestNewPutsItWhereTheWorkingFolderIs.
//
// new used to resolve its path from the root whatever cd had said, and
// it was the only path-taking command that did: it called folderAt
// directly instead of going through resolveDir.  Measured against a
// live shell sitting in /new-check, "new README" made the notecard at
// /README, and "new sub/note" was refused with no folder "sub" while
// /new-check/sub was there to be written into.  It now splits off the
// last name and resolves the rest the way mkdir does.
func TestNewPutsItWhereTheWorkingFolderIs(t *testing.T) {
	x := newTestShell(t)

	// A folder below Objects, so a relative path has something to
	// travel through: the fake's tree is otherwise one deep.
	nest := msg.MustParseUUID("ab6c7e57-7e57-c0de-e0ce-9f8b1da5cee8")
	x.grid.inv.Dirs[0].Dirs = append(x.grid.inv.Dirs[0].Dirs, &invDir{ID: nest, Name: "nest"})

	// The grid confirms the item, which is what CreateItem waits for,
	// and the move that follows is the message these checks read: it
	// carries the folder the shell resolved.
	made := msg.MustParseUUID("e1c87e57-7e57-c0de-3ad9-0cdb6c24e8f4")
	x.grid.mu.Lock()
	x.grid.onSend = func(m msg.Message) {
		c, ok := m.(*msg.CreateInventoryItem)
		if !ok {
			return
		}
		x.grid.Relay(t, &msg.UpdateCreateInventoryItem{
			InventoryData: []msg.UpdateCreateInventoryItem_InventoryData{{
				CallbackID: c.InventoryBlock.CallbackID,
				ItemID:     made,
				Name:       c.InventoryBlock.Name,
				Type:       c.InventoryBlock.Type,
				InvType:    c.InventoryBlock.InvType,
			}},
		})
	}
	x.grid.mu.Unlock()

	// wentTo runs one new and answers with the folder the item was
	// moved into, which is the whole of what is being asked here.
	wentTo := func(line string) msg.UUID {
		t.Helper()
		if got := x.do(t, line); !strings.Contains(got, made.String()) {
			t.Fatalf("%q printed %q, want the id of the new item", line, got)
		}
		sent := x.grid.Sent()
		for i := len(sent) - 1; i >= 0; i-- {
			if mv, ok := sent[i].(*msg.MoveInventoryItem); ok {
				return mv.InventoryData[0].FolderID
			}
		}
		t.Fatalf("%q moved nothing", line)
		return msg.UUID{}
	}

	x.do(t, "cd Objects")
	if got := wentTo("new note"); got != testObjects {
		t.Errorf("a bare name in /Objects went to %v, want Objects", got)
	}
	if got := wentTo("new nest/note"); got != nest {
		t.Errorf("a relative path through a subfolder went to %v, want nest", got)
	}
	// A leading slash still means the root, wherever the shell stands.
	if got := wentTo("new /note"); got != testRoot {
		t.Errorf("an absolute path went to %v, want the root", got)
	}
	if got := wentTo("new ../Scripts/note"); got != testScripts {
		t.Errorf("a path back up went to %v, want Scripts", got)
	}

	// And a folder that is genuinely not there still names itself.
	if got := x.do(t, "new nowhere/note"); !strings.Contains(got, `no folder "nowhere"`) {
		t.Errorf("new under a folder that is not there printed %q", got)
	}
}

// TestStartingEveryScriptSaysWhatBecameOfEachOne.
//
// "start Box1" is one request per script and they need not agree with
// each other: one may be stopped, one may be running already, and which
// was which is the whole of what somebody needs to know next.  So a line
// each, and "already" is a line rather than an error -- a script running
// when somebody asked for it to run is in the state they wanted, and
// "started" would be a claim about something that did not happen.
//
// The notecard is here to be left alone.  Starting one is not a thing,
// so an object's contents are filtered down to its scripts before any of
// this begins.
func TestStartingEveryScriptSaysWhatBecameOfEachOne(t *testing.T) {
	x := aBoxHolding(t,
		&heldItem{Name: "greeter", ID: aGreeter},
		&heldItem{Name: "listener", ID: aListener, Running: true},
		&heldItem{Name: "readme", ID: aReadme, Kind: "notecard"},
	)

	got := x.do(t, "start Box1")
	if !strings.Contains(got, "started    greeter") {
		t.Errorf("the script that was stopped printed %q", got)
	}
	if !strings.Contains(got, "already    listener") {
		t.Errorf("the script that was already running printed %q", got)
	}
	if strings.Contains(got, "readme") {
		t.Errorf("start touched the notecard: %q", got)
	}

	// Nothing was sent for the script that was already running.  The
	// request would have been harmless and the report would not: it is
	// the answer to the question asked first that makes "already"
	// something this knows rather than something it guessed.
	sent := sentOfShell[*msg.SetScriptRunning](x)
	if len(sent) != 1 {
		t.Fatalf("%d scripts were told to start, want the one that was not running", len(sent))
	}
	if sent[0].Script.ItemID != aGreeter || !sent[0].Script.Running {
		t.Errorf("the request was %+v", sent[0].Script)
	}
	if sent[0].Script.ObjectID != aBox {
		t.Errorf("the request named object %s", sent[0].Script.ObjectID)
	}
}

// TestStopAsksForTheOtherStateAndSaysSo: stop is start with the state
// reversed, and a stop that sent Running true would leave every script
// in the object running and report that it had stopped them.
func TestStopAsksForTheOtherStateAndSaysSo(t *testing.T) {
	x := aBoxHolding(t,
		&heldItem{Name: "greeter", ID: aGreeter, Running: true},
		&heldItem{Name: "listener", ID: aListener},
	)

	got := x.do(t, "stop Box1")
	if !strings.Contains(got, "stopped    greeter") {
		t.Errorf("the script that was running printed %q", got)
	}
	if !strings.Contains(got, "already    listener") {
		t.Errorf("the script that was already stopped printed %q", got)
	}

	sent := sentOfShell[*msg.SetScriptRunning](x)
	if len(sent) != 1 || sent[0].Script.ItemID != aGreeter || sent[0].Script.Running {
		t.Errorf("stop sent %+v", sent)
	}
}

// TestOneScriptThatWillNotMoveDoesNotStopTheOthers.
//
// A script the simulator will not start is reported as exactly that --
// the request went out and the object has not agreed -- and the scripts
// after it are still asked.  Stopping at the first failure would leave
// somebody with one line of output about one script and no idea whether
// the other two moved, which is the state this is written to prevent.
func TestOneScriptThatWillNotMoveDoesNotStopTheOthers(t *testing.T) {
	x := aBoxHolding(t,
		&heldItem{Name: "greeter", ID: aGreeter, Stuck: true},
		&heldItem{Name: "listener", ID: aListener},
		&heldItem{Name: "watcher", ID: aWatcher},
	)

	got := x.do(t, "start -w 1 Box1")
	if !strings.Contains(got, "no answer  greeter") {
		t.Errorf("the script that would not start printed %q", got)
	}
	for _, want := range []string{"started    listener", "started    watcher"} {
		if !strings.Contains(got, want) {
			t.Errorf("the scripts after it printed %q, want %q among them", got, want)
		}
	}
	if !strings.Contains(got, "1 of 3 scripts") {
		t.Errorf("start should fail saying how many did not agree, printed %q", got)
	}

	// The request did go out for it, which is why the report says the
	// object has not agreed rather than that the start was refused.
	var told bool
	for _, m := range sentOfShell[*msg.SetScriptRunning](x) {
		told = told || m.Script.ItemID == aGreeter
	}
	if !told {
		t.Error("nothing was ever sent for the script reported as unanswered")
	}
}

// TestAScriptWhoseAnswerNeverArrivesIsNotReportedAsStopped.
//
// A region can take the request and say nothing about it, on either
// relay, and that has to read as not knowing.  Reporting it as a start
// that happened would be the claim the confirmation exists to prevent,
// and reporting it as a refusal would blame the region for something it
// may well have done: measured on Agni, a stop whose confirmation never
// reached this client had in fact stopped the script.
func TestAScriptWhoseAnswerNeverArrivesIsNotReportedAsStopped(t *testing.T) {
	x := aBoxHolding(t, &heldItem{Name: "greeter", ID: aGreeter, Deaf: true})

	got := x.do(t, "start -w 1 Box1")
	if !strings.Contains(got, "no answer  greeter") {
		t.Errorf("a script nothing answered about printed %q", got)
	}
	if strings.Contains(got, "started") || strings.Contains(got, "already") {
		t.Errorf("a script nothing answered about was reported as changed: %q", got)
	}
	if !strings.Contains(got, "the request went out") {
		t.Errorf("the failure should say the request went out, printed %q", got)
	}
}

// TestStartingOneNamedScriptLeavesTheRestAlone: naming a script is the
// whole reason the argument is there, and a command that started them
// all anyway would be worse than one that refused the name.
func TestStartingOneNamedScriptLeavesTheRestAlone(t *testing.T) {
	x := aBoxHolding(t,
		&heldItem{Name: "greeter", ID: aGreeter},
		&heldItem{Name: "listener", ID: aListener},
	)

	got := x.do(t, "start Box1 listener")
	if !strings.Contains(got, "started    listener") {
		t.Errorf("start of one script printed %q", got)
	}
	if strings.Contains(got, "greeter") {
		t.Errorf("start of one script touched the other: %q", got)
	}
	sent := sentOfShell[*msg.SetScriptRunning](x)
	if len(sent) != 1 || sent[0].Script.ItemID != aListener {
		t.Errorf("start of one script sent %+v", sent)
	}
}

// TestStartRefusesWhatIsNotThere.
//
// A name that is not in the object is a typo or a script somebody
// deleted, and either way the answer is what the object does hold.  An
// object with no scripts at all gets its own sentence: "no script called
// x" would read as a denial that the box has any, which is the thing
// most worth saying when it is true.
func TestStartRefusesWhatIsNotThere(t *testing.T) {
	x := aBoxHolding(t,
		&heldItem{Name: "greeter", ID: aGreeter},
	)
	got := x.do(t, "start Box1 footstool")
	if !strings.Contains(got, `no script called "footstool"`) {
		t.Errorf("start of a script that is not there printed %q", got)
	}
	if !strings.Contains(got, "ls --in") {
		t.Errorf("the refusal should say how to see what is there, printed %q", got)
	}
	if got := sentOfShell[*msg.SetScriptRunning](x); len(got) != 0 {
		t.Errorf("a refused start still sent %d requests", len(got))
	}

	x = aBoxHolding(t, &heldItem{Name: "readme", ID: aReadme, Kind: "notecard"})
	if got := x.do(t, "start Box1"); !strings.Contains(got, "holds no scripts") {
		t.Errorf("start of an object with nothing to start printed %q", got)
	}

	// The object itself is the region's answer rather than this
	// command's, and comes back as it stands.
	x = newTestShell(t)
	if got := x.do(t, "stop footstool"); !strings.Contains(got, `nothing called "footstool"`) {
		t.Errorf("stop of an object that is not there printed %q", got)
	}
	for _, line := range []string{"start", "stop"} {
		if got := x.do(t, line); !strings.Contains(got, "usage: "+line) {
			t.Errorf("%q with no object printed %q", line, got)
		}
		if got := x.do(t, line+" --help"); !strings.Contains(got, "Usage:") {
			t.Errorf("%s --help printed %q", line, got)
		}
	}
}

// TestNewInsideAnObjectCompilesItAndSaysItIsRunning.
//
// Copying a script into an object leaves it there and does not start it;
// InstallScript uploads the source through UpdateScriptTask, which
// compiles it inside the object and starts it.  That is a different
// outcome from "new" on its own, so the output says so -- somebody who
// read "hello.lsl 97c27e57" and walked away would have no idea whether
// anything was running.
func TestNewInsideAnObjectCompilesItAndSaysItIsRunning(t *testing.T) {
	x := aBoxHolding(t, &heldItem{Name: "greeter", ID: aGreeter})
	serveScriptUpload(t, x, `<llsd><map><key>state</key><string>complete</string>`+
		`<key>compiled</key><boolean>1</boolean></map></llsd>`)

	got := x.do(t, "new --in Box1 greeter")
	if !strings.Contains(got, "greeter is in Box1 and running") {
		t.Errorf("new --in printed %q", got)
	}

	// A script that will not compile is in there and is not running,
	// and the compiler's complaint is the whole answer to why.
	x = aBoxHolding(t, &heldItem{Name: "greeter", ID: aGreeter})
	serveScriptUpload(t, x, `<llsd><map><key>state</key><string>complete</string>`+
		`<key>compiled</key><boolean>0</boolean>`+
		`<key>errors</key><array><string>(1,1) : ERROR : Syntax error</string></array>`+
		`</map></llsd>`)
	got = x.do(t, "new --in Box1 greeter")
	if !strings.Contains(got, "did not compile") || !strings.Contains(got, "Syntax error") {
		t.Errorf("new --in of something that will not compile printed %q", got)
	}
	if strings.Contains(got, "running") {
		t.Errorf("a script that did not compile was reported as running: %q", got)
	}
}

// TestNewInsideAnObjectRefusesANotecardByName.
//
// Nothing in the sl package writes a notecard into a prim, so this is a
// call that does not exist rather than a kind that is not a kind -- and
// "--kind is notecard or script" would be a lie, since notecard is
// exactly what was asked for and is what "new" makes everywhere else.
// The refusal says which two commands do it in two steps instead.
func TestNewInsideAnObjectRefusesANotecardByName(t *testing.T) {
	x := aBoxHolding(t)
	got := x.do(t, "new --in Box1 --kind notecard readme")
	if !strings.Contains(got, "nothing here can write a notecard inside an object") {
		t.Errorf("new --in --kind notecard printed %q", got)
	}
	if !strings.Contains(got, "drop") {
		t.Errorf("the refusal should say what to do instead, printed %q", got)
	}

	// An object holds no folders, so a path names nothing it could go
	// into; ls --in refuses one for the same reason.
	if got := x.do(t, "new --in Box1 Scripts/greeter"); !strings.Contains(got, "holds no folders") {
		t.Errorf("new --in with a path printed %q", got)
	}
	if got := x.do(t, "new --in Box1 --kind texture greeter"); !strings.Contains(got, "notecard or script") {
		t.Errorf("new --in with a kind that is neither printed %q", got)
	}
}

// serveScriptUpload answers UpdateScriptTask, which is the two step
// upload every capability uses: the first request names somewhere to put
// the bytes, and what comes back from THERE is the compiler's verdict.
func serveScriptUpload(t *testing.T, x *testShell, verdict string) {
	t.Helper()
	dest := x.grid.ServeCap(t, "slgo test script upload", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/llsd+xml")
		io.WriteString(w, verdict)
	})
	x.grid.ServeCap(t, "UpdateScriptTask", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/llsd+xml")
		fmt.Fprintf(w, `<llsd><map><key>state</key><string>upload</string>`+
			`<key>uploader</key><string>%s/upload</string></map></llsd>`, dest.URL)
	})
}

// TestRmAndMvInsideRefuseWhatIsNotThere.
//
// Both of these used to panic.  sl.FindInObject answers "it is not in
// there" with a nil item and no error -- which is the answer sl.Run and
// sl.InstallScript act on, since not finding the script is how they
// decide to put one in -- and rm --in and mv --in read the item straight
// back, one for its id and one for a copy of the whole of it.  A
// mistyped name took the shell down with a nil dereference.
//
// So the refusal is the shell's rather than the sl package's, and it
// says both halves of what went wrong: which object was looked in, and
// which name was not in it.
func TestRmAndMvInsideRefuseWhatIsNotThere(t *testing.T) {
	x := aBoxHolding(t,
		&heldItem{Name: "greeter", ID: aGreeter},
		&heldItem{Name: "readme", ID: aReadme, Kind: "notecard"},
	)

	for _, line := range []string{"rm --in Box1 footstool", "mv --in Box1 footstool warden"} {
		got := x.do(t, line)
		if !strings.Contains(got, `Box1 holds nothing called "footstool"`) {
			t.Errorf("%q printed %q", line, got)
		}
		if !strings.Contains(got, "ls --in") {
			t.Errorf("%q should say how to see what is there, printed %q", line, got)
		}
	}

	// Nothing went out for either of them.  A name that is not in the
	// object is decided here, out of the contents already read, so a
	// refusal that had sent a message would have deleted or renamed
	// something on the strength of a zero id.
	if got := sentOfShell[*msg.RemoveTaskInventory](x); len(got) != 0 {
		t.Errorf("a refused rm --in sent %d removals", len(got))
	}
	if got := sentOfShell[*msg.UpdateTaskInventory](x); len(got) != 0 {
		t.Errorf("a refused mv --in sent %d renames", len(got))
	}

	// And what the object does hold is still deleted and still renamed:
	// the check is for the nil and not for every name.  A notecard, for
	// rm, because these two verbs work on whatever is in there rather
	// than on scripts alone.
	if got := x.do(t, "rm --in Box1 readme"); !strings.Contains(got, `deleted "readme" from Box1`) {
		t.Errorf("rm --in of something that is there printed %q", got)
	}
	sent := sentOfShell[*msg.RemoveTaskInventory](x)
	if len(sent) != 1 || sent[0].InventoryData.ItemID != aReadme {
		t.Errorf("rm --in sent %+v", sent)
	}

	if got := x.do(t, "mv --in Box1 greeter warden"); !strings.Contains(got, `"greeter" in Box1 is now "warden"`) {
		t.Errorf("mv --in of something that is there printed %q", got)
	}
	renames := sentOfShell[*msg.UpdateTaskInventory](x)
	if len(renames) != 1 {
		t.Fatalf("mv --in sent %d renames, want one", len(renames))
	}
	if got := strings.TrimRight(string(renames[0].InventoryData.Name), "\x00"); got != "warden" {
		t.Errorf("the rename carried the name %q", got)
	}
	if renames[0].InventoryData.ItemID != aGreeter {
		t.Errorf("the rename named item %s", renames[0].InventoryData.ItemID)
	}
}

// TestCatInReadsTheObjectsCopy: the object's own copy is read, through
// the object, and printed the way cat prints one from inventory.
func TestCatInReadsTheObjectsCopy(t *testing.T) {
	x := aBoxHolding(t,
		&heldItem{Name: "greeter", ID: aGreeter, Text: "default { state_entry() { } }\n"},
		&heldItem{Name: "read me", ID: aReadme, Kind: "notecard",
			Text: "Linden text version 2\n{\nLLEmbeddedItems version 1\n{\ncount 0\n}\n" +
				"Text length 9\nthe words\n}\n"},
		&heldItem{Name: "a picture", ID: aWatcher, Kind: "texture"},
	)

	if got, want := x.do(t, "cat --in Box1 greeter"), "default { state_entry() { } }\n"; got != want {
		t.Errorf("cat --in of a script printed %q, want %q", got, want)
	}
	if got, want := x.do(t, `cat --in Box1 "read me"`), "the words\n"; got != want {
		t.Errorf("cat --in of a notecard printed %q, want %q", got, want)
	}
	// By the id the object gives its copy, which is how one of two of a
	// name is chosen.
	if got, want := x.do(t, "cat --in "+aBox.String()+" "+aGreeter.String()), "default { state_entry() { } }\n"; got != want {
		t.Errorf("cat --in by uuids printed %q, want %q", got, want)
	}
	if got := x.do(t, "cat --in Box1 a picture"); !strings.Contains(got, "usage: cat") {
		t.Errorf("cat --in with an unquoted name printed %q", got)
	}
	if got := x.do(t, `cat --in Box1 "a picture"`); !strings.Contains(got, "not text") {
		t.Errorf("cat --in of a texture printed %q", got)
	}
	if got := x.do(t, "cat --in Box1 nothing"); !strings.Contains(got, `nothing called "nothing"`) {
		t.Errorf("cat --in of something not there printed %q", got)
	}
}

// TestCatInSaysWhyANoCopyNotecardCannotBeRead: the region refuses one
// with "insufficient permissions", and the permission missing is copy,
// which a person holding every other right to it would not guess.
func TestCatInSaysWhyANoCopyNotecardCannotBeRead(t *testing.T) {
	x := aBoxHolding(t, &heldItem{Name: "only one", ID: aReadme, Kind: "notecard",
		OwnerMask: sl.PermAll &^ sl.PermCopy, Text: "unseen"})
	got := x.do(t, `cat --in Box1 "only one"`)
	if !strings.Contains(got, "may not be copied") || !strings.Contains(got, "insufficient permissions") {
		t.Errorf("cat --in of a no-copy notecard printed %q", got)
	}
}

// TestFetchCopiesIntoTheWorkingFolder: drop the other way round, and
// the object keeps what it had.
func TestFetchCopiesIntoTheWorkingFolder(t *testing.T) {
	x := aBoxHolding(t, &heldItem{Name: "read me", ID: aReadme, Kind: "notecard"})

	x.do(t, "cd Scripts")
	got := x.do(t, "fetch Box1 read me")
	if !strings.Contains(got, `copied "read me" from Box1 into /Scripts`) {
		t.Fatalf("fetch printed %q", got)
	}
	if got := x.do(t, "ls"); !strings.Contains(got, "read me") {
		t.Errorf("/Scripts after the fetch lists %q", got)
	}
	if got := x.do(t, "ls --in Box1"); !strings.Contains(got, "read me") {
		t.Errorf("the box lost what was copied out of it: %q", got)
	}
	m := sentOfShell[*msg.MoveTaskInventory](x)
	if len(m) != 1 || m[0].AgentData.FolderID != testScripts || m[0].InventoryData.ItemID != aReadme {
		t.Errorf("sent %+v", m)
	}
}

func TestFetchIntoAnotherFolder(t *testing.T) {
	x := aBoxHolding(t, &heldItem{Name: "greeter", ID: aGreeter})
	if got := x.do(t, "fetch --into /Scripts Box1 greeter"); !strings.Contains(got, "into /Scripts") {
		t.Errorf("fetch --into printed %q", got)
	}
	if got := x.do(t, "ls /Scripts"); !strings.Contains(got, "greeter") {
		t.Errorf("/Scripts lists %q", got)
	}
}

// TestFetchWillNotEmptyAnObjectUnasked: an item that may not be copied
// is moved out rather than copied, and only --move asks for that.
func TestFetchWillNotEmptyAnObjectUnasked(t *testing.T) {
	x := aBoxHolding(t, &heldItem{Name: "only one", ID: aReadme, Kind: "notecard",
		OwnerMask: sl.PermAll &^ sl.PermCopy})

	got := x.do(t, "fetch Box1 only one")
	if !strings.Contains(got, "may not be copied") || !strings.Contains(got, "--move") {
		t.Errorf("fetch of a no-copy item printed %q", got)
	}
	if n := len(sentOfShell[*msg.MoveTaskInventory](x)); n != 0 {
		t.Fatalf("asked for it anyway, %d times", n)
	}

	got = x.do(t, "fetch --move Box1 only one")
	if !strings.Contains(got, `moved "only one" out of Box1`) {
		t.Errorf("fetch --move printed %q", got)
	}
	if got := x.do(t, "ls --in Box1"); strings.Contains(got, "only one") {
		t.Errorf("the box still lists what was moved out: %q", got)
	}
}

// TestFetchRefusesWhatIsNotOursToTake: somebody else's item that may not
// be copied cannot be moved to us either, and that is the object's
// owner's business rather than a flag's.
func TestFetchRefusesWhatIsNotOursToTake(t *testing.T) {
	x := aBoxHolding(t,
		&heldItem{Name: "theirs", ID: aReadme, Kind: "notecard", Owner: aListener},
		&heldItem{Name: "free", ID: aWatcher, Kind: "notecard", Owner: aListener,
			EveryoneMask: sl.PermCopy},
	)
	if got := x.do(t, "fetch --move Box1 theirs"); !strings.Contains(got, "not yours") {
		t.Errorf("fetch of somebody else's no-copy item printed %q", got)
	}
	if got := x.do(t, "fetch Box1 free"); !strings.Contains(got, `copied "free"`) {
		t.Errorf("fetch of an item everyone may copy printed %q", got)
	}
}

func TestFetchWantsBoth(t *testing.T) {
	x := newTestShell(t)
	for _, line := range []string{"fetch", "fetch Box1"} {
		if got := x.do(t, line); !strings.Contains(got, "usage: fetch") {
			t.Errorf("%q printed %q", line, got)
		}
	}
}
