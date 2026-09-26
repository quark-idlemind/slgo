package sl

// Running a script inside an object, which is what this package was
// built for.
//
// Run is the one call here that has to get an ordering right rather
// than a message right, and the ordering is the whole point: the
// listener has to be open before the compile, because a script says
// what it has to say the instant it is started; the script has to be
// looked up INSIDE the object first, because the copy in there has its
// own item id and that is what the capability wants; and the verdict
// has to be read before the wait, because waiting a minute for output
// from something that did not compile is a slow way to learn nothing.
// Each of those was got wrong at some point, and none of them shows up
// as a wrong message on the wire -- only as a run that hangs, or one
// that returns having heard nothing.
//
// Reaching any of it means standing in for three protocols at once: the
// object's contents arrive over xfer, the compile is an http upload to a
// capability, and the output is chat relayed back.  The calls wait, so
// they run aside; see fake_test.go.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"sync/atomic"
)

// objectSaid is one line of chat from an object, which is the only way a
// script says anything.
func objectSaid(from msg.UUID, chatType uint8, text string) *msg.ChatFromSimulator {
	m := &msg.ChatFromSimulator{}
	m.ChatData.SourceID = from
	m.ChatData.OwnerID = testAgentID
	m.ChatData.FromName = append([]byte("a prim"), 0)
	m.ChatData.SourceType = SourceObject
	m.ChatData.ChatType = chatType
	m.ChatData.Message = append([]byte(text), 0)
	return m
}

// contents stands in for the simulator's side of reading what an object
// holds: the request is answered with the name of a file, and the file
// comes over xfer.
//
// It counts the rounds because Run reads the contents twice when the
// script has to be put in first, and the two answers differ -- the first
// says the script is not in there.  Answering the second read as though
// it were the first hands the call an answer it has already had.  The
// two counts are kept apart because a read that says the object holds
// nothing has no transfer after it.
type contents struct {
	f    *fakeBackend
	task msg.UUID

	reads, xfers int
}

func objectHolding(f *fakeBackend, task msg.UUID) *contents {
	return &contents{f: f, task: task}
}

// answer plays one read.  An empty file is the object holding nothing.
func (c *contents) answer(t *testing.T, file string) {
	t.Helper()
	c.reads++
	waitSentN[*msg.RequestTaskInventory](t, c.f, c.reads)
	if file == "" {
		c.f.Relay(t, replyTaskInventory(c.task, ""))
		return
	}
	c.f.Relay(t, replyTaskInventory(c.task, "inventory_37c9.tmp"))
	c.xfers++
	x := waitSentN[*msg.RequestXfer](t, c.f, c.xfers)
	c.f.Relay(t, xferPacket(x.XferID.ID, 0, true, []byte(file)))
}

// answerContents is one read of an object that holds the usual file,
// for a call that only reads once.
func answerContents(t *testing.T, f *fakeBackend, task msg.UUID, file string) {
	t.Helper()
	objectHolding(f, task).answer(t, file)
}

// compiles is an upload that says the source compiled, which is what
// UpdateScriptTask answers for a script the simulator accepted.
func compiles() (int, string) {
	return 200, `<llsd><map><key>state</key><string>complete</string>` +
		`<key>compiled</key><boolean>1</boolean></map></llsd>`
}

// TestAResultSaysWhichWayTheRunEnded: a run has three ways of not
// getting to the end and a caller that treated them alike would report
// a compile error as a timeout.
func TestAResultSaysWhichWayTheRunEnded(t *testing.T) {
	finished := &Result{Compiled: true, Finished: true}
	if finished.Failed() {
		t.Error("a script that compiled and said it had finished was reported as failed")
	}
	for name, r := range map[string]*Result{
		"did not compile": {Compiled: false, Finished: true},
		"faulted":         {Compiled: true, Finished: true, Fault: &Fault{Script: "s"}},
		"never finished":  {Compiled: true},
	} {
		if !r.Failed() {
			t.Errorf("a run that %s was reported as having got to the end", name)
		}
	}
}

// TestAResultReadsBackWhatWasHeard: the lines are what a probe asserts
// against, and the debug channel is where the simulator puts the
// complaints a script survives -- so a reader that could not separate
// them would have to grep.
func TestAResultReadsBackWhatWasHeard(t *testing.T) {
	r := &Result{Lines: []Line{
		{Text: "starting", Type: ChatSay},
		{Text: "Could not find texture", Type: ChatDebug},
		{Text: "FINISHED", Type: ChatSay},
	}}
	if got := r.Said(); len(got) != 3 || got[0] != "starting" || got[2] != "FINISHED" {
		t.Errorf("Said = %q", got)
	}
	d := r.Debug()
	if len(d) != 1 || d[0].Text != "Could not find texture" {
		t.Errorf("Debug = %v", d)
	}
	if !r.Contains("FINISH") {
		t.Error("Contains missed a line it was given part of")
	}
	if r.Contains("never said") {
		t.Error("Contains found something nothing said")
	}
}

// TestRunListensBeforeItCompiles: a script says what it has to say the
// instant it is started, so a listener opened after the upload has
// already missed the first line -- which is usually the one that says
// what the script is.
func TestRunListensBeforeItCompiles(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	up := serveUpload(t, f, "UpdateScriptTask", compiles)
	o := foundHere(w, &Object{ID: thePrim, Local: 77})

	var mu sync.Mutex
	var heard []string
	wait := aside(t, func() (*Result, error) {
		return w.Run(context.Background(), Script{
			In: o, Name: "a script", Source: "default {}", Done: "FINISHED",
			OnLine: func(l Line) {
				mu.Lock()
				heard = append(heard, l.Text)
				mu.Unlock()
			},
		})
	})

	answerContents(t, f, thePrim, theContentsFile)

	// The upload names the copy inside the object rather than the
	// inventory item it came from: the capability compiles into a task,
	// and the task's item id is the one the contents file gave.
	if got := string(<-up.asked); !strings.Contains(got, theChild.String()) ||
		!strings.Contains(got, thePrim.String()) || !strings.Contains(got, "mono") {
		t.Errorf("the capability was asked %q", got)
	}
	// With a newline in front of it, which is what makes an upload that
	// arrived empty tell itself apart from a script that is wrong at its
	// first character.  See leadingNewline.
	if got := string(<-up.body); got != "\ndefault {}" {
		t.Errorf("uploaded %q", got)
	}

	// Something else in the region talking is not this run's business,
	// and a transcript that held it would report lines the script never
	// said.
	f.Relay(t, objectSaid(theOther, ChatSay, "not from the script"))
	f.Relay(t, objectSaid(thePrim, ChatSay, "starting"))
	f.Relay(t, objectSaid(thePrim, ChatSay, "FINISHED"))

	res, err := wait()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Compiled || !res.Finished || res.Fault != nil {
		t.Errorf("Run = %+v", res)
	}
	if res.Item != theChild {
		t.Errorf("the run names item %s, want the copy inside the object", res.Item)
	}
	if got := res.Said(); len(got) != 2 || got[0] != "starting" {
		t.Errorf("the transcript is %q", got)
	}
	if res.Elapsed <= 0 {
		t.Error("the run took no measurable time at all")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(heard) != 2 {
		t.Errorf("OnLine was called with %q", heard)
	}
}

// TestRunWillNotWaitForOutputFromSomethingThatDidNotCompile: nothing is
// going to be said, so waiting out the timeout only delays an answer
// that is already known.
func TestRunWillNotWaitForOutputFromSomethingThatDidNotCompile(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	serveUpload(t, f, "UpdateScriptTask", func() (int, string) {
		return 200, `<llsd><map><key>state</key><string>complete</string>` +
			`<key>compiled</key><boolean>0</boolean>` +
			`<key>errors</key><array><string>(1,1) : ERROR : Syntax error</string></array>` +
			`</map></llsd>`
	})

	wait := aside(t, func() (*Result, error) {
		return w.Run(context.Background(), Script{
			In: foundHere(w, &Object{ID: thePrim, Local: 77}), Name: "a script",
			Source: "default {", Done: "FINISHED", Timeout: time.Minute,
		})
	})
	answerContents(t, f, thePrim, theContentsFile)

	res, err := wait()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Compiled || res.Finished {
		t.Errorf("Run = %+v", res)
	}
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0], "Syntax error") {
		t.Errorf("the compiler said %q", res.Errors)
	}
}

// TestRunStopsWhenTheScriptFaults: a run-time fault stops the script
// where it is, so the sentinel is never coming and the whole timeout
// would be spent waiting for it.  The reason arrives as a second line
// and is worth the moment it takes.
func TestRunStopsWhenTheScriptFaults(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	up := serveUpload(t, f, "UpdateScriptTask", compiles)

	wait := aside(t, func() (*Result, error) {
		return w.Run(context.Background(), Script{
			In: foundHere(w, &Object{ID: thePrim, Local: 77}), Name: "a script",
			Source: "default {}", Done: "FINISHED", Timeout: time.Minute,
		})
	})
	answerContents(t, f, thePrim, theContentsFile)
	<-up.body

	f.Relay(t, objectSaid(thePrim, ChatDebug,
		"Test HUD [script:a script] Script run-time error"))
	f.Relay(t, objectSaid(thePrim, ChatDebug, "Math Error"))

	res, err := wait()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Fault == nil {
		t.Fatalf("a faulted script came back with no fault: %+v", res)
	}
	if res.Fault.Script != "a script" || res.Fault.Reason != "Math Error" {
		t.Errorf("fault = %+v", res.Fault)
	}
	if !res.Failed() || res.Finished {
		t.Error("a faulted run was reported as having got to the end")
	}
	if res.Elapsed > 30*time.Second {
		t.Errorf("the run waited %s for a script that had already stopped", res.Elapsed)
	}
}

// TestRunCarriesOnPastAFaultWhenAskedTo: the object may hold other
// scripts, and one of them dying is not a reason to stop listening to
// the rest.
func TestRunCarriesOnPastAFaultWhenAskedTo(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	up := serveUpload(t, f, "UpdateScriptTask", compiles)

	wait := aside(t, func() (*Result, error) {
		return w.Run(context.Background(), Script{
			In: foundHere(w, &Object{ID: thePrim, Local: 77}), Name: "a script",
			Source: "default {}", Done: "FINISHED", IgnoreFault: true,
			Timeout: time.Minute,
		})
	})
	answerContents(t, f, thePrim, theContentsFile)
	<-up.body

	f.Relay(t, objectSaid(thePrim, ChatDebug,
		"Test HUD [script:a script] Script run-time error"))
	f.Relay(t, objectSaid(thePrim, ChatDebug, "Math Error"))
	// The fault did not end the run, so the sentinel still does.
	f.Relay(t, objectSaid(thePrim, ChatSay, "FINISHED"))

	res, err := wait()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Finished {
		t.Error("a run told to ignore faults stopped at one anyway")
	}
	if res.Fault == nil {
		t.Error("the fault was ignored so thoroughly it was not even reported")
	}
}

// TestRunWithNoSentinelHasNothingToWaitForButTheClock: a script that
// never says it has finished cannot be told from one about to speak, so
// the timeout is spent rather than shortened -- and reaching it is a
// result rather than an error.
func TestRunWithNoSentinelHasNothingToWaitForButTheClock(t *testing.T) {
	t.Parallel()

	t.Run("no sentinel", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		up := serveUpload(t, f, "UpdateScriptTask", compiles)

		wait := aside(t, func() (*Result, error) {
			return w.Run(context.Background(), Script{
				In: foundHere(w, &Object{ID: thePrim, Local: 77}), Name: "a script",
				Source: "default {}", Timeout: 50 * time.Millisecond,
			})
		})
		answerContents(t, f, thePrim, theContentsFile)
		<-up.body

		res, err := wait()
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if res.Finished {
			t.Error("a run with nothing to look for said it had found it")
		}
	})

	t.Run("a sentinel nobody said", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		up := serveUpload(t, f, "UpdateScriptTask", compiles)

		wait := aside(t, func() (*Result, error) {
			return w.Run(context.Background(), Script{
				In: foundHere(w, &Object{ID: thePrim, Local: 77}), Name: "a script",
				Source: "default {}", Done: "FINISHED",
				Timeout: 50 * time.Millisecond,
			})
		})
		answerContents(t, f, thePrim, theContentsFile)
		<-up.body

		res, err := wait()
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if res.Finished || res.Failed() != true {
			t.Errorf("Run = %+v, want a run that timed out", res)
		}
	})
}

// TestRunReportsWhatDidNotHappen: every step of a run is somewhere it
// can stop, and a caller told only that the run failed cannot tell a
// missing object from a compile that never went out.
func TestRunReportsWhatDidNotHappen(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	if _, err := w.Run(context.Background(), Script{Name: "a script"}); err == nil {
		t.Error("Run ran a script in no object at all")
	}
	if _, err := w.Run(context.Background(), Script{In: &Object{ID: thePrim}}); err == nil {
		t.Error("Run ran a script with no name")
	}

	t.Run("the object could not be read", func(t *testing.T) {
		f.FailSends(errors.New("the circuit is gone"))
		_, err := w.Run(context.Background(), Script{
			In: foundHere(w, &Object{ID: thePrim, Local: 77}), Name: "a script", Source: "default {}",
		})
		if err == nil {
			t.Error("Run compiled into an object it could not read")
		}
	})

	t.Run("there is nowhere to compile", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		// No UpdateScriptTask capability, which is what a session that
		// never asked for one looks like.
		wait := aside(t, func() (*Result, error) {
			return w.Run(context.Background(), Script{
				In: foundHere(w, &Object{ID: thePrim, Local: 77}), Name: "a script", Source: "default {}",
			})
		})
		answerContents(t, f, thePrim, theContentsFile)
		if _, err := wait(); err == nil {
			t.Error("Run reported a result without compiling anything")
		}
	})

	t.Run("the caller gave up while listening", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		up := serveUpload(t, f, "UpdateScriptTask", compiles)
		// A deadline rather than a cancel from here, because giving up
		// while the compile is still in flight is a different branch:
		// this one has to land while the run is listening.
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()

		wait := aside(t, func() (*Result, error) {
			return w.Run(ctx, Script{
				In: foundHere(w, &Object{ID: thePrim, Local: 77}), Name: "a script",
				Source: "default {}", Done: "FINISHED", Timeout: time.Minute,
			})
		})
		answerContents(t, f, thePrim, theContentsFile)
		<-up.body

		res, err := wait()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Run = %v, want the context's reason", err)
		}
		// The lines heard before giving up are still worth having.
		if res == nil {
			t.Error("a cancelled run came back with no transcript at all")
		}
	})
}

// TestRunPutsTheScriptInWhenItIsNotThere: copying an inventory script
// into an object does not start it, so the copy is made and then
// compiled through the same capability a rerun uses.  Getting the item
// id from the second read rather than the first is the point: the copy
// inside the object has its own, and the inventory item's is no use to
// the capability.
func TestRunPutsTheScriptInWhenItIsNotThere(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	serveUpload(t, f, "UpdateScriptAgent", compiles)
	up := serveUpload(t, f, "UpdateScriptTask", compiles)

	wait := aside(t, func() (*Result, error) {
		return w.Run(context.Background(), Script{
			In: foundHere(w, &Object{ID: thePrim, Local: 77}), Name: "a script",
			Source: "default {}", Done: "FINISHED", Timeout: time.Minute,
		})
	})

	// Not in the object: the read answers with a file holding nothing.
	held := objectHolding(f, thePrim)
	held.answer(t, "")

	m := waitSent[*msg.CreateInventoryItem](t, f)
	relayCreated(t, f, m.InventoryBlock.CallbackID)
	put := waitSent[*msg.UpdateTaskInventory](t, f)
	if put.UpdateData.LocalID != 77 || put.InventoryData.ItemID != theChild {
		t.Errorf("the copy went into %+v", put.UpdateData)
	}

	// Then the settle, and the read that finds it.
	held.answer(t, theContentsFile)
	<-up.body
	f.Relay(t, objectSaid(thePrim, ChatSay, "FINISHED"))

	res, err := wait()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Item != theChild || !res.Finished {
		t.Errorf("Run = %+v", res)
	}
}

// TestRunSaysWhenTheCopyNeverArrived: an object keeps what it is given,
// so a script that is still not in there after a copy and a settle means
// the copy was refused -- and compiling against a nil item would be a
// nil dereference rather than a message.
func TestRunSaysWhenTheCopyNeverArrived(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	serveUpload(t, f, "UpdateScriptAgent", compiles)

	wait := aside(t, func() (*Result, error) {
		return w.Run(context.Background(), Script{
			In: foundHere(w, &Object{ID: thePrim, Local: 77}), Name: "a script", Source: "default {}",
		})
	})
	held := objectHolding(f, thePrim)
	held.answer(t, "")
	m := waitSent[*msg.CreateInventoryItem](t, f)
	relayCreated(t, f, m.InventoryBlock.CallbackID)
	waitSent[*msg.UpdateTaskInventory](t, f)
	held.answer(t, "")

	_, err := wait()
	if err == nil || !strings.Contains(err.Error(), "never turned up") {
		t.Errorf("Run = %v, want it to say the copy did not arrive", err)
	}
}

// TestInstallScriptIsTheHalfThatCompiles: copying a script into an
// object leaves it there and does not start it, and SetScriptRunning
// cannot start it either because there is nothing compiled to start.
// This is the save that compiles it in place, which is why running a
// script always worked while installing a listener never did.
func TestInstallScriptIsTheHalfThatCompiles(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	up := serveUpload(t, f, "UpdateScriptTask", compiles)
	o := foundHere(w, &Object{ID: thePrim, Local: 77})

	wait := aside(t, func() (*UploadResult, error) {
		return w.InstallScript(context.Background(), o, "a script", "default {}", true)
	})
	answerContents(t, f, thePrim, theContentsFile)

	if got := string(<-up.asked); !strings.Contains(got, theChild.String()) {
		t.Errorf("the capability was asked %q, want the item inside the object", got)
	}
	res, err := wait()
	if err != nil {
		t.Fatalf("InstallScript: %v", err)
	}
	if !res.Compiled {
		t.Errorf("InstallScript = %+v", res)
	}
}

// TestInstallScriptCopiesItInFirstWhenItHasTo: the same two steps Run
// takes, and for the same reason -- there is nothing to compile into
// until the object holds a copy.
func TestInstallScriptCopiesItInFirstWhenItHasTo(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	serveUpload(t, f, "UpdateScriptAgent", compiles)
	up := serveUpload(t, f, "UpdateScriptTask", compiles)
	o := foundHere(w, &Object{ID: thePrim, Local: 77})

	wait := aside(t, func() (*UploadResult, error) {
		return w.InstallScript(context.Background(), o, "a script", "default {}", false)
	})
	held := objectHolding(f, thePrim)
	held.answer(t, "")
	m := waitSent[*msg.CreateInventoryItem](t, f)
	relayCreated(t, f, m.InventoryBlock.CallbackID)
	waitSent[*msg.UpdateTaskInventory](t, f)
	held.answer(t, theContentsFile)

	if got := string(<-up.asked); !strings.Contains(got, "is_script_running") {
		t.Errorf("the capability was asked %q", got)
	}
	if _, err := wait(); err != nil {
		t.Fatalf("InstallScript: %v", err)
	}
}

// TestInstallScriptRefusesWhatItCannotDo: the object and the name are
// what the whole call is addressed to, and a missing one would be a
// message about an item id rather than about the argument.
func TestInstallScriptRefusesWhatItCannotDo(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	if _, err := w.InstallScript(context.Background(), nil, "a script", "", true); err == nil {
		t.Error("InstallScript installed into no object")
	}
	o := foundHere(w, &Object{ID: thePrim, Local: 77})
	if _, err := w.InstallScript(context.Background(), o, "", "", true); err == nil {
		t.Error("InstallScript installed something with no name")
	}

	f.FailSends(errors.New("the circuit is gone"))
	if _, err := w.InstallScript(context.Background(), o, "a script", "", true); err == nil {
		t.Error("InstallScript compiled into an object it could not read")
	}

	t.Run("the copy never arrived", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		serveUpload(t, f, "UpdateScriptAgent", compiles)
		wait := aside(t, func() (*UploadResult, error) {
			return w.InstallScript(context.Background(), foundHere(w, &Object{ID: thePrim, Local: 77}),
				"a script", "default {}", true)
		})
		held := objectHolding(f, thePrim)
		held.answer(t, "")
		m := waitSent[*msg.CreateInventoryItem](t, f)
		relayCreated(t, f, m.InventoryBlock.CallbackID)
		waitSent[*msg.UpdateTaskInventory](t, f)
		held.answer(t, "")
		if _, err := wait(); err == nil || !strings.Contains(err.Error(), "never turned up") {
			t.Errorf("InstallScript = %v", err)
		}
	})
}

// TestRemoveScriptsTakesOutOnlyTheScripts: an object holds textures and
// notecards as well, and a run that cleared everything matching a name
// would take out whatever the object needs to work.
func TestRemoveScriptsTakesOutOnlyTheScripts(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	o := foundHere(w, &Object{ID: thePrim, Local: 77})

	wait := aside(t, func() (int, error) {
		return w.RemoveScripts(context.Background(), o, func(name string) bool {
			return strings.HasPrefix(name, "a ")
		})
	})
	answerContents(t, f, thePrim, theContentsFile)

	rm := waitSent[*msg.RemoveTaskInventory](t, f)
	if rm.InventoryData.ItemID != theChild || rm.InventoryData.LocalID != 77 {
		t.Errorf("removed %+v", rm.InventoryData)
	}

	n, err := wait()
	if err != nil {
		t.Fatalf("RemoveScripts: %v", err)
	}
	if n != 1 {
		t.Errorf("removed %d scripts, want 1", n)
	}
}

// TestRemoveScriptsLeavesWhatDoesNotMatch: a match that says no is the
// ordinary case -- most of what an object holds is not the leftover
// being cleared -- and it must not cost a settle either.
func TestRemoveScriptsLeavesWhatDoesNotMatch(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	o := foundHere(w, &Object{ID: thePrim, Local: 77})

	wait := aside(t, func() (int, error) {
		return w.RemoveScripts(context.Background(), o, func(string) bool { return false })
	})
	answerContents(t, f, thePrim, theContentsFile)

	n, err := wait()
	if err != nil {
		t.Fatalf("RemoveScripts: %v", err)
	}
	if n != 0 {
		t.Errorf("removed %d scripts from an object nothing matched in", n)
	}
	if got := sentOf[*msg.RemoveTaskInventory](f); len(got) != 0 {
		t.Errorf("%d removals went out for a match that said no", len(got))
	}
}

// TestRemoveScriptsReportsWhatDidNotHappen: it reads over one protocol
// and removes over another, and a caller told nothing was removed would
// go looking for the scripts.
func TestRemoveScriptsReportsWhatDidNotHappen(t *testing.T) {
	t.Parallel()
	oAt := func(w *Session) *Object { return foundHere(w, &Object{ID: thePrim, Local: 77}) }

	t.Run("the object could not be read", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.FailSends(errors.New("the circuit is gone"))
		if _, err := w.RemoveScripts(context.Background(), oAt(w), func(string) bool { return true }); err == nil {
			t.Error("RemoveScripts reported on an object it could not read")
		}
	})

	t.Run("the removal never went", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		wait := aside(t, func() (int, error) {
			return w.RemoveScripts(context.Background(), oAt(w), func(string) bool { return true })
		})
		answerContents(t, f, thePrim, theContentsFile)
		f.FailSends(errors.New("the circuit is gone"))

		n, err := wait()
		if err == nil {
			t.Error("RemoveScripts reported a removal that never went out")
		}
		if n != 0 {
			t.Errorf("counted %d removed", n)
		}
	})
}

// TestSetScriptRunningNamesBothTheObjectAndTheScript: an object may hold
// several scripts, so the pair is what says which one to start.
func TestSetScriptRunningNamesBothTheObjectAndTheScript(t *testing.T) {
	w, f := newFakeSession(t)
	o := foundHere(w, &Object{ID: thePrim, Local: 77})

	if err := w.SetScriptRunning(context.Background(), o, theChild, true); err != nil {
		t.Fatalf("SetScriptRunning: %v", err)
	}
	m := onlySent[*msg.SetScriptRunning](t, f)
	if m.Script.ObjectID != thePrim || m.Script.ItemID != theChild || !m.Script.Running {
		t.Errorf("SetScriptRunning sent %+v", m.Script)
	}
	if m.AgentData.AgentID != testAgentID || m.AgentData.SessionID != testSessionID {
		t.Errorf("the message names %s", m.AgentData.AgentID)
	}

	f.Forget()
	f.FailSends(errors.New("the circuit is gone"))
	if err := w.SetScriptRunning(context.Background(), o, theChild, false); err == nil {
		t.Error("SetScriptRunning reported a stop that never went out")
	}
}

// TestRunStopsAtWhicheverStepOfPuttingTheScriptInFailed: getting a
// script into an object is four things -- create the item, upload the
// source, copy it in, and read the object again -- and a caller told
// only that the run failed cannot tell which of them to look at.
func TestRunStopsAtWhicheverStepOfPuttingTheScriptInFailed(t *testing.T) {
	t.Parallel()
	oAt := func(w *Session) *Object { return foundHere(w, &Object{ID: thePrim, Local: 77}) }
	gone := errors.New("the circuit is gone")

	t.Run("the item was never created", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		failSendsAfter[*msg.RequestTaskInventory](f, gone)
		wait := aside(t, func() (*Result, error) {
			return w.Run(context.Background(), Script{In: oAt(w), Name: "a script"})
		})
		objectHolding(f, thePrim).answer(t, "")
		if _, err := wait(); err == nil {
			t.Error("Run compiled into an object with no script in it")
		}
	})

	t.Run("the copy never went in", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		serveUpload(t, f, "UpdateScriptAgent", compiles)
		failSendsAfter[*msg.CreateInventoryItem](f, gone)
		wait := aside(t, func() (*Result, error) {
			return w.Run(context.Background(), Script{In: oAt(w), Name: "a script"})
		})
		held := objectHolding(f, thePrim)
		held.answer(t, "")
		m := waitSent[*msg.CreateInventoryItem](t, f)
		relayCreated(t, f, m.InventoryBlock.CallbackID)
		if _, err := wait(); err == nil {
			t.Error("Run compiled into an object the copy never reached")
		}
	})

	t.Run("the caller gave up while it settled", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		serveUpload(t, f, "UpdateScriptAgent", compiles)
		// The settle after a copy is six seconds, so a caller with less
		// patience than that gives up inside it.
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer cancel()
		wait := aside(t, func() (*Result, error) {
			return w.Run(ctx, Script{In: oAt(w), Name: "a script"})
		})
		held := objectHolding(f, thePrim)
		held.answer(t, "")
		m := waitSent[*msg.CreateInventoryItem](t, f)
		relayCreated(t, f, m.InventoryBlock.CallbackID)
		waitSent[*msg.UpdateTaskInventory](t, f)
		if _, err := wait(); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Run = %v, want the context's reason", err)
		}
	})

	t.Run("the object could not be read again", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		serveUpload(t, f, "UpdateScriptAgent", compiles)
		failSendsAfter[*msg.UpdateTaskInventory](f, gone)
		wait := aside(t, func() (*Result, error) {
			return w.Run(context.Background(), Script{In: oAt(w), Name: "a script"})
		})
		held := objectHolding(f, thePrim)
		held.answer(t, "")
		m := waitSent[*msg.CreateInventoryItem](t, f)
		relayCreated(t, f, m.InventoryBlock.CallbackID)
		if _, err := wait(); err == nil {
			t.Error("Run reported a result for an object it could not read")
		}
	})
}

// TestARunThatCouldNotPutTheScriptInLeavesNoCopyInInventory: the copy
// in the avatar's inventory is only the way in, and is deleted when the
// way in fails as it is when it succeeds -- after the copy was sent into
// the object as well as before.
func TestARunThatCouldNotPutTheScriptInLeavesNoCopyInInventory(t *testing.T) {
	t.Parallel()
	oAt := func(w *Session) *Object { return foundHere(w, &Object{ID: thePrim, Local: 77}) }
	gone := errors.New("the circuit is gone")
	sent := func(t *testing.T, f *fakeBackend, _ *contents) { waitSent[*msg.UpdateTaskInventory](t, f) }

	for _, c := range []struct {
		name  string
		stage func(t *testing.T, f *fakeBackend)
		// after plays what follows the item being made; patience is the
		// caller's, when it gives up.
		after    func(t *testing.T, f *fakeBackend, held *contents)
		patience time.Duration
	}{
		// No upload capability: the item is made and its source is not
		// saved to it.
		{name: "the source could not be saved", stage: func(*testing.T, *fakeBackend) {}},
		{name: "the copy never went in", stage: func(t *testing.T, f *fakeBackend) {
			serveUpload(t, f, "UpdateScriptAgent", compiles)
			failSendsAfter[*msg.CreateInventoryItem](f, gone)
		}},
		// The settle after a copy is six seconds, so a caller with less
		// patience than that gives up inside it.
		{name: "the caller gave up while it settled", stage: func(t *testing.T, f *fakeBackend) {
			serveUpload(t, f, "UpdateScriptAgent", compiles)
		}, after: sent, patience: 1500 * time.Millisecond},
		{name: "the object could not be read again", stage: func(t *testing.T, f *fakeBackend) {
			serveUpload(t, f, "UpdateScriptAgent", compiles)
			failSendsAfter[*msg.UpdateTaskInventory](f, gone)
		}, after: sent},
		{name: "the copy never turned up", stage: func(t *testing.T, f *fakeBackend) {
			serveUpload(t, f, "UpdateScriptAgent", compiles)
		}, after: func(t *testing.T, f *fakeBackend, held *contents) {
			sent(t, f, held)
			held.answer(t, "")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			w, f := newFakeSession(t)
			c.stage(t, f)
			deleted := make(chan string, 4)
			f.ServeCap(t, "InventoryAPIv3", func(rw http.ResponseWriter, r *http.Request) {
				deleted <- r.Method + " " + r.URL.Path
				rw.WriteHeader(http.StatusOK)
			})

			ctx := context.Background()
			if c.patience != 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, c.patience)
				defer cancel()
			}
			wait := aside(t, func() (*Result, error) {
				return w.Run(ctx, Script{In: oAt(w), Name: "a script"})
			})
			held := objectHolding(f, thePrim)
			held.answer(t, "")
			m := waitSent[*msg.CreateInventoryItem](t, f)
			relayCreated(t, f, m.InventoryBlock.CallbackID)
			if c.after != nil {
				c.after(t, f, held)
			}
			_, err := wait()
			if err == nil {
				t.Fatal("Run went on without the script in the object")
			}
			if c.patience != 0 && !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("Run = %v, want the context's reason", err)
			}

			select {
			case got := <-deleted:
				if want := "DELETE /item/" + theChild.String(); got != want {
					t.Errorf("asked %q, want %q", got, want)
				}
			default:
				t.Error("the copy in inventory was left behind")
			}
		})
	}
}

// TestInstallScriptStopsAtTheSameStepsForTheSameReasons: it is Run's
// first half without the listening, so the ways it can stop are the same
// ones -- and a caller of this is usually installing something another
// call is about to depend on.
func TestInstallScriptStopsAtTheSameStepsForTheSameReasons(t *testing.T) {
	t.Parallel()
	oAt := func(w *Session) *Object { return foundHere(w, &Object{ID: thePrim, Local: 77}) }
	gone := errors.New("the circuit is gone")

	t.Run("the item was never created", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		failSendsAfter[*msg.RequestTaskInventory](f, gone)
		wait := aside(t, func() (*UploadResult, error) {
			return w.InstallScript(context.Background(), oAt(w), "a script", "default {}", true)
		})
		objectHolding(f, thePrim).answer(t, "")
		if _, err := wait(); err == nil {
			t.Error("InstallScript compiled into an object with no script in it")
		}
	})

	t.Run("the copy never went in", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		serveUpload(t, f, "UpdateScriptAgent", compiles)
		failSendsAfter[*msg.CreateInventoryItem](f, gone)
		wait := aside(t, func() (*UploadResult, error) {
			return w.InstallScript(context.Background(), oAt(w), "a script", "default {}", true)
		})
		objectHolding(f, thePrim).answer(t, "")
		m := waitSent[*msg.CreateInventoryItem](t, f)
		relayCreated(t, f, m.InventoryBlock.CallbackID)
		if _, err := wait(); err == nil {
			t.Error("InstallScript compiled into an object the copy never reached")
		}
	})

	t.Run("the caller gave up while it settled", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		serveUpload(t, f, "UpdateScriptAgent", compiles)
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer cancel()
		wait := aside(t, func() (*UploadResult, error) {
			return w.InstallScript(ctx, oAt(w), "a script", "default {}", true)
		})
		objectHolding(f, thePrim).answer(t, "")
		m := waitSent[*msg.CreateInventoryItem](t, f)
		relayCreated(t, f, m.InventoryBlock.CallbackID)
		waitSent[*msg.UpdateTaskInventory](t, f)
		if _, err := wait(); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("InstallScript = %v, want the context's reason", err)
		}
	})

	t.Run("the object could not be read again", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		serveUpload(t, f, "UpdateScriptAgent", compiles)
		failSendsAfter[*msg.UpdateTaskInventory](f, gone)
		wait := aside(t, func() (*UploadResult, error) {
			return w.InstallScript(context.Background(), oAt(w), "a script", "default {}", true)
		})
		objectHolding(f, thePrim).answer(t, "")
		m := waitSent[*msg.CreateInventoryItem](t, f)
		relayCreated(t, f, m.InventoryBlock.CallbackID)
		if _, err := wait(); err == nil {
			t.Error("InstallScript reported a result for an object it could not read")
		}
	})
}

// TestARunWithNoSentinelStillAnswersToTheCaller: there is nothing to
// wait for but the clock, and a caller that has changed its mind must
// not have to wait out a timeout it no longer cares about.
func TestARunWithNoSentinelStillAnswersToTheCaller(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	up := serveUpload(t, f, "UpdateScriptTask", compiles)
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()

	wait := aside(t, func() (*Result, error) {
		return w.Run(ctx, Script{
			In: foundHere(w, &Object{ID: thePrim, Local: 77}), Name: "a script",
			Source: "default {}", Timeout: time.Minute,
		})
	})
	answerContents(t, f, thePrim, theContentsFile)
	<-up.body
	f.Relay(t, objectSaid(thePrim, ChatSay, "part of the way"))

	res, err := wait()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Run = %v, want the context's reason", err)
	}
	// What was heard before the caller gave up is still the caller's,
	// as it is on the run with a sentinel.
	if res == nil || !res.Contains("part of the way") {
		t.Errorf("Run = %+v, want the line heard before the caller gave up", res)
	}
}

// TestAStopThatFailsAfterAFailedInstallIsNotLost: the stop is there for
// an install that failed on the way back, and that run has no Result to
// carry a warning, so the stop's failure is joined to the install's.
func TestAStopThatFailsAfterAFailedInstallIsNotLost(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	gone := errors.New("the circuit is gone")
	serveUpload(t, f, "UpdateScriptTask", func() (int, string) {
		// The upload may have started the script, and by the time
		// the stop goes out the circuit has gone.
		f.FailSends(gone)
		return http.StatusBadRequest, "no"
	})

	wait := aside(t, func() (*Result, error) {
		return w.Run(context.Background(), Script{
			In: foundHere(w, &Object{ID: thePrim, Local: 77}), Name: "a script",
			Source: "default {}", Done: "FINISHED", Timeout: time.Minute,
		})
	})
	answerContents(t, f, thePrim, theContentsFile)

	res, err := wait()
	if res != nil {
		t.Errorf("Run = %+v for an install that failed", res)
	}
	var ce *CapError
	if !errors.As(err, &ce) {
		t.Errorf("Run = %v, want the install's failure", err)
	}
	if !errors.Is(err, gone) || !strings.Contains(fmt.Sprint(err), "may still be running") {
		t.Errorf("Run = %v, want the failed stop joined to it", err)
	}
}

// TestAScriptCanFaultAfterSayingItHadFinished: the sentinel is still
// watched for during the grace after a fault, because a script that says
// it is done and then dies on the way out of the handler has still done
// what it was asked.
func TestAScriptCanFaultAfterSayingItHadFinished(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	up := serveUpload(t, f, "UpdateScriptTask", compiles)

	wait := aside(t, func() (*Result, error) {
		return w.Run(context.Background(), Script{
			In: foundHere(w, &Object{ID: thePrim, Local: 77}), Name: "a script",
			Source: "default {}", Done: "FINISHED", Timeout: time.Minute,
		})
	})
	answerContents(t, f, thePrim, theContentsFile)
	<-up.body

	// The fault first, and then the sentinel rather than a reason.
	//
	// The pause is what makes this the branch it is meant to be.  Both
	// the fault and the sentinel end the wait, so a sentinel relayed
	// straight after the fault leaves the run choosing between two
	// things that are both ready -- and half the time it would take the
	// sentinel without ever entering the grace.  The grace is three
	// seconds, so a tenth of one inside it is not a race.
	f.Relay(t, objectSaid(thePrim, ChatDebug,
		"Test HUD [script:a script] Script run-time error"))
	time.Sleep(100 * time.Millisecond)
	f.Relay(t, objectSaid(thePrim, ChatSay, "FINISHED"))

	res, err := wait()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Finished {
		t.Error("a sentinel said during the fault grace was not counted")
	}
	if res.Fault == nil || res.Fault.Reason != "" {
		t.Errorf("fault = %+v, want one that never said why", res.Fault)
	}
}

// TestRemoveScriptsSettlesBeforeSayingItIsDone: an object's contents
// take a moment to catch up with a removal, so a caller that read them
// straight afterwards would see the script it had just taken out.
func TestRemoveScriptsSettlesBeforeSayingItIsDone(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	o := foundHere(w, &Object{ID: thePrim, Local: 77})
	// Less patience than the settle takes, which is where a caller that
	// has given up lands.
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()

	wait := aside(t, func() (int, error) {
		return w.RemoveScripts(ctx, o, func(string) bool { return true })
	})
	answerContents(t, f, thePrim, theContentsFile)
	waitSent[*msg.RemoveTaskInventory](t, f)

	n, err := wait()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("RemoveScripts = %v, want the context's reason", err)
	}
	// The count is still the truth: the script was removed, and only
	// the wait for it to take effect was cut short.
	if n != 1 {
		t.Errorf("RemoveScripts counted %d removed", n)
	}
}

// scriptRunningReply is the simulator answering GetScriptRunning.
func scriptRunningReply(object, item msg.UUID, running bool) *msg.ScriptRunningReply {
	m := &msg.ScriptRunningReply{}
	m.Script.ObjectID = object
	m.Script.ItemID = item
	m.Script.Running = running
	return m
}

// running is what ScriptRunning answered, for a call driven from
// another goroutine: the whole point of these tests is what the call
// does before it returns, which a wait for its answer cannot see.
type running struct {
	is  bool
	err error
}

func askRunning(w *Session, ctx context.Context, o *Object, item msg.UUID, timeout time.Duration) chan running {
	got := make(chan running, 1)
	go func() {
		is, err := w.ScriptRunning(ctx, o, item, timeout)
		got <- running{is, err}
	}()
	return got
}

// TestScriptRunningAnswersOnlyForTheScriptItWasAskedAbout.
//
// Nothing replies to SetScriptRunning, so a caller that printed "it is
// running" would be printing that it had asked.  This is the question
// that turns that into an answer, and both ids are in it: one object
// holds several scripts, and one script's name is in several objects, so
// a reply matched on either id alone would answer about something else
// entirely -- and answering early is worse than not answering, because
// the wrong answer looks like a confirmation.
func TestScriptRunningAnswersOnlyForTheScriptItWasAskedAbout(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	f.quietScripts = true // the answers here are relayed by hand
	o := foundHere(w, &Object{ID: thePrim, Local: 77})

	got := askRunning(w, context.Background(), o, theChild, 30*time.Second)
	q := waitSent[*msg.GetScriptRunning](t, f)
	if q.Script.ObjectID != thePrim || q.Script.ItemID != theChild {
		t.Errorf("the question named %+v", q.Script)
	}

	// The same script in another object, then another script in this
	// one.  Neither is what was asked.
	f.Relay(t, scriptRunningReply(theOther, theChild, true))
	f.Relay(t, scriptRunningReply(thePrim, theOther, true))
	select {
	case a := <-got:
		t.Fatalf("ScriptRunning answered %v (%v) from a reply about another script", a.is, a.err)
	case <-time.After(300 * time.Millisecond):
	}

	f.Relay(t, scriptRunningReply(thePrim, theChild, true))
	select {
	case a := <-got:
		if a.err != nil {
			t.Fatalf("ScriptRunning: %v", a.err)
		}
		if !a.is {
			t.Error("ScriptRunning said the script was stopped, and the object said it was running")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("ScriptRunning never answered, though the object had said it was running")
	}
}

// TestScriptRunningBelievesAStoppedScriptToo.
//
// "Not running" is an answer and not the absence of one, which is the
// whole difference between a script that will not start and a reply this
// client never heard.  A query that treated false as nothing would leave
// stop with no way ever to confirm itself.
func TestScriptRunningBelievesAStoppedScriptToo(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	f.quietScripts = true // the answers here are relayed by hand
	o := foundHere(w, &Object{ID: thePrim, Local: 77})

	got := askRunning(w, context.Background(), o, theChild, 30*time.Second)
	waitSent[*msg.GetScriptRunning](t, f)
	f.Relay(t, scriptRunningReply(thePrim, theChild, false))

	select {
	case a := <-got:
		if a.err != nil {
			t.Fatalf("ScriptRunning: %v", a.err)
		}
		if a.is {
			t.Error("ScriptRunning said the script was running, and the object said it was stopped")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("ScriptRunning treated a stopped script as no answer at all")
	}
}

// TestScriptRunningReportsNotKnowingRatherThanGuessing.
//
// Every way this can fail has to be distinguishable from "the script is
// stopped", because the caller prints one of them and acts on the other.
// The reply is marked UDPDeprecated in the message template, so a region
// that has moved it to the event queue would leave this unanswered for
// ever -- and reporting that as a stopped script would have somebody
// told their script is not running when nothing here can say either way.
func TestScriptRunningReportsNotKnowingRatherThanGuessing(t *testing.T) {
	oAt := func(w *Session) *Object { return foundHere(w, &Object{ID: thePrim, Local: 77}) }

	t.Run("nothing answered", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		f.quietScripts = true // the answers here are relayed by hand
		is, err := w.ScriptRunning(context.Background(), oAt(w), theChild, 200*time.Millisecond)
		if !errors.Is(err, ErrTimeout) {
			t.Errorf("ScriptRunning = %v, %v; want a timeout", is, err)
		}
		if is {
			t.Error("a question nobody answered came back as a running script")
		}
		// The question still went out, which is what says the silence
		// is the region's and not this call's.
		if got := sentOf[*msg.GetScriptRunning](f); len(got) != 1 {
			t.Errorf("%d questions went out", len(got))
		}
	})

	t.Run("the question never went", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		f.quietScripts = true // the answers here are relayed by hand
		f.FailSends(errors.New("the circuit is gone"))
		if is, err := w.ScriptRunning(context.Background(), oAt(w), theChild, 30*time.Second); err == nil {
			t.Errorf("ScriptRunning = %v with the circuit gone", is)
		}
		if got := f.Sent(); len(got) != 0 {
			t.Errorf("a question that failed to send was recorded as %s", f.describe())
		}
	})

	t.Run("the caller gave up waiting", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		f.quietScripts = true // the answers here are relayed by hand
		ctx, cancel := context.WithCancel(context.Background())
		got := askRunning(w, ctx, oAt(w), theChild, 30*time.Second)
		waitSent[*msg.GetScriptRunning](t, f)
		cancel()
		select {
		case a := <-got:
			if !errors.Is(a.err, context.Canceled) {
				t.Errorf("ScriptRunning = %v, %v; want the context's reason", a.is, a.err)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("ScriptRunning waited out its own timeout after the context was cancelled")
		}
	})

	t.Run("no object to ask about", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		f.quietScripts = true // the answers here are relayed by hand
		if _, err := w.ScriptRunning(context.Background(), nil, theChild, time.Second); err == nil {
			t.Error("ScriptRunning asked about no object at all")
		}
		if got := f.Sent(); len(got) != 0 {
			t.Errorf("a question about nothing still sent %s", f.describe())
		}
	})
}

// TestScriptRunningListensBeforeItAsks.
//
// The reply is a message like any other and can be handled before Send
// has returned, since the region is not obliged to wait for anything.  A
// call that subscribed after asking would miss exactly the fast answer
// and report a timeout for a script the object had already described.
func TestScriptRunningListensBeforeItAsks(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	o := foundHere(w, &Object{ID: thePrim, Local: 77})

	f.mu.Lock()
	f.onSend = func(m msg.Message) {
		if q, ok := m.(*msg.GetScriptRunning); ok {
			f.Relay(t, scriptRunningReply(q.Script.ObjectID, q.Script.ItemID, true))
		}
	}
	f.mu.Unlock()

	select {
	case a := <-askRunning(w, context.Background(), o, theChild, 5*time.Second):
		if a.err != nil || !a.is {
			t.Errorf("ScriptRunning = %v, %v; the object answered while the question was still being sent", a.is, a.err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("ScriptRunning never answered")
	}
}

// agniScriptRunning is the answer Second Life sends, as it was captured
// on Agni: the Script block as an ARRAY where the template declares a
// single block, and fields the template has never had.
//
// Written out as text rather than built from the generated type, because
// the generated type cannot express any of what makes this worth
// testing.  Running is "1" or "0".
func agniScriptRunning(object, item msg.UUID, running string) string {
	return `<llsd><map><key>Script</key><array><map>` +
		`<key>Running</key><boolean>` + running + `</boolean>` +
		`<key>ItemID</key><string>` + item.String() + `</string>` +
		`<key>Luau</key><boolean>0</boolean><key>LuauLanguage</key><boolean>0</boolean>` +
		`<key>Mono</key><boolean>1</boolean>` +
		`<key>ObjectID</key><string>` + object.String() + `</string>` +
		`</map></array></map></llsd>`
}

// TestTheAnswerAboutAScriptComesOffTheEventQueue.
//
// This is the shape a live grid sends and the only place it sends it.
// Measured on Agni: a stop was asked for with a second client watching
// both relays, nothing at all came back on the circuit, and every reply
// arrived on the queue in the body this builds.
//
// Three things about that body are load bearing.  The Script block is an
// ARRAY although the template declares it Single, so a decoder reading
// the value as one map finds nothing.  The map carries Mono, Luau and
// LuauLanguage, which the template does not have, so a decoder that
// insisted on the template's fields would reject the whole thing -- and
// a decoder that reached for the wrong key would answer with Luau's
// false for a script that is running.  And the ids are strings rather
// than the uuid element, which is what llsd renders either as.
func TestTheAnswerAboutAScriptComesOffTheEventQueue(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	f.quietScripts = true // the answers here are relayed by hand
	o := foundHere(w, &Object{ID: thePrim, Local: 77})

	got := askRunning(w, context.Background(), o, theChild, 30*time.Second)
	waitSent[*msg.GetScriptRunning](t, f)
	f.RelayEvent(t, "ScriptRunningReply", agniScriptRunning(thePrim, theChild, "1"))

	select {
	case a := <-got:
		if a.err != nil {
			t.Fatalf("ScriptRunning: %v", a.err)
		}
		if !a.is {
			t.Error("the queue said the script was running and ScriptRunning did not")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("ScriptRunning never heard the answer the event queue carried")
	}

	// And the other way, since a stop is confirmed by exactly this
	// answer with one character changed.
	w, f = newFakeSession(t)
	got = askRunning(w, context.Background(), o, theChild, 30*time.Second)
	waitSent[*msg.GetScriptRunning](t, f)
	f.RelayEvent(t, "ScriptRunningReply", agniScriptRunning(thePrim, theChild, "0"))
	select {
	case a := <-got:
		if a.err != nil || a.is {
			t.Errorf("ScriptRunning = %v, %v; the queue said the script was stopped", a.is, a.err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("ScriptRunning never heard the answer the event queue carried")
	}
}

// TestAnEventQueueAnswerIsSiftedTheWayAMessageIs.
//
// The queue carries everything the grid has to say and hands it over by
// name, so what would be a message number on the circuit is a string
// here and nothing else stands between this session and somebody else's
// business.  An answer about another script, an event of another name,
// and a body that is not the shape at all all have to go past without
// answering the question that was asked -- and without bringing the
// reader down, since the reader is the whole session.
func TestAnEventQueueAnswerIsSiftedTheWayAMessageIs(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	f.quietScripts = true // the answers here are relayed by hand
	o := foundHere(w, &Object{ID: thePrim, Local: 77})

	got := askRunning(w, context.Background(), o, theChild, 30*time.Second)
	waitSent[*msg.GetScriptRunning](t, f)

	for _, wrong := range []struct {
		what string
		name string
		body string
	}{
		{"another script in this object", "ScriptRunningReply", agniScriptRunning(thePrim, theOther, "1")},
		{"this script in another object", "ScriptRunningReply", agniScriptRunning(theOther, theChild, "1")},
		{"an event nothing here reads", "TeleportFinish", agniScriptRunning(thePrim, theChild, "1")},
		{"a body with no block in it", "ScriptRunningReply", `<llsd><map/></llsd>`},
		{"a block with no ids in it", "ScriptRunningReply",
			`<llsd><map><key>Script</key><array><map><key>Running</key><boolean>1</boolean></map></array></map></llsd>`},
		{"a body that is not LLSD at all", "ScriptRunningReply", `not llsd`},
	} {
		f.RelayEvent(t, wrong.name, wrong.body)
		select {
		case a := <-got:
			t.Fatalf("%s answered the question: %v, %v", wrong.what, a.is, a.err)
		case <-time.After(100 * time.Millisecond):
		}
	}

	// The reader survived all of it and still answers the real thing,
	// which is what says the sifting drops rather than derails.
	f.RelayEvent(t, "ScriptRunningReply", agniScriptRunning(thePrim, theChild, "1"))
	select {
	case a := <-got:
		if a.err != nil || !a.is {
			t.Errorf("ScriptRunning = %v, %v after the bodies it had to ignore", a.is, a.err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the reader stopped reading the queue after a body it could not use")
	}
}

// TestABlockThatIsNotAnArrayIsStillABlock.
//
// Agni sends the array and the template says Single, so the two disagree
// and this package cannot insist on either.  A grid that sent the bare
// map -- an older simulator, another grid entirely -- would otherwise be
// read as an answer with no blocks in it, which is the failure that
// looks exactly like the message never arriving.
func TestABlockThatIsNotAnArrayIsStillABlock(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	f.quietScripts = true // the answers here are relayed by hand
	o := foundHere(w, &Object{ID: thePrim, Local: 77})

	got := askRunning(w, context.Background(), o, theChild, 30*time.Second)
	waitSent[*msg.GetScriptRunning](t, f)
	f.RelayEvent(t, "ScriptRunningReply", `<llsd><map><key>Script</key><map>`+
		`<key>ObjectID</key><string>`+thePrim.String()+`</string>`+
		`<key>ItemID</key><string>`+theChild.String()+`</string>`+
		`<key>Running</key><boolean>1</boolean></map></map></llsd>`)

	select {
	case a := <-got:
		if a.err != nil || !a.is {
			t.Errorf("ScriptRunning = %v, %v for a block sent as one map", a.is, a.err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("a block sent as one map rather than an array of one was read as no block at all")
	}
}

// TestTheEventQueueEndingIsNotTheSessionEnding.
//
// A simulator answers 404 to a queue it has finished with, and the
// circuit carries on regardless -- so a reader that treated the queue
// closing as the session closing would take down chat, every waiter and
// every subscription because a long poll had been retired.
func TestTheEventQueueEndingIsNotTheSessionEnding(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	f.quietScripts = true // the answers here are relayed by hand
	o := foundHere(w, &Object{ID: thePrim, Local: 77})

	f.EndEvents()

	// The circuit still works, which is the whole claim.  A grid that
	// answers there is answered from Session.handle rather than from
	// the queue, so this exercises the other arm as well.
	got := askRunning(w, context.Background(), o, theChild, 30*time.Second)
	waitSent[*msg.GetScriptRunning](t, f)
	f.Relay(t, scriptRunningReply(thePrim, theChild, true))
	select {
	case a := <-got:
		if a.err != nil || !a.is {
			t.Errorf("ScriptRunning = %v, %v with the event queue closed", a.is, a.err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the reader stopped when the event queue closed, and the circuit was still up")
	}
	select {
	case <-w.readDone:
		t.Error("the session ended because its event queue did")
	default:
	}
}

// TestAnUploadThatArrivedEmptyIsSentAgain: measured on Agni, an upload
// sometimes reaches Second Life's compiler with nothing in it -- the
// body leaves here whole, goes to an uploader URL nothing else is using,
// and the capability answers that it completed and made an asset that
// compiles as nothing.  Three runs in eight of thirty scripts hit it.
//
// It is worth asking again exactly because it cannot be the caller's
// fault: every script goes out with a newline in front, so nothing of
// theirs is on the line the compiler names.
func TestAnUploadThatArrivedEmptyIsSentAgain(t *testing.T) {
	w, f := newFakeSession(t)
	var tries atomic.Int32
	up := serveUpload(t, f, "UpdateScriptTask", func() (int, string) {
		if tries.Add(1) == 1 {
			return 200, `<llsd><map><key>state</key><string>complete</string>` +
				`<key>compiled</key><boolean>0</boolean>` +
				`<key>errors</key><array><string>(0, 0) : ERROR : Syntax error` +
				`</string></array></map></llsd>`
		}
		return 200, `<llsd><map><key>state</key><string>complete</string>` +
			`<key>compiled</key><boolean>1</boolean></map></llsd>`
	})
	_ = up

	res, err := w.install(context.Background(), msg.UUID{15: 1}, msg.UUID{15: 2},
		"default { state_entry() {} }", true)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !res.Compiled {
		t.Errorf("an upload that arrived empty was not sent again: %+v", res)
	}
	if got := tries.Load(); got != 2 {
		t.Errorf("the script was uploaded %d times, want the empty one and one more", got)
	}
}

// TestAScriptWrongAtItsFirstCharacterIsNotSentAgain: the newline in
// front is what makes this distinguishable.  Without it the compiler
// says "(0, 0)" about a caller's own mistake as well, and asking again
// would cost an upload and answer the same thing.
func TestAScriptWrongAtItsFirstCharacterIsNotSentAgain(t *testing.T) {
	w, f := newFakeSession(t)
	var tries atomic.Int32
	serveUpload(t, f, "UpdateScriptTask", func() (int, string) {
		tries.Add(1)
		// Line ONE, because the newline this package adds pushed the
		// caller's first line down.
		return 200, `<llsd><map><key>state</key><string>complete</string>` +
			`<key>compiled</key><boolean>0</boolean>` +
			`<key>errors</key><array><string>(1, 0) : ERROR : Syntax error` +
			`</string></array></map></llsd>`
	})

	res, err := w.install(context.Background(), msg.UUID{15: 1}, msg.UUID{15: 2},
		"123 456", true)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if res.Compiled {
		t.Fatal("a script that will not compile was reported as having")
	}
	if got := tries.Load(); got != 1 {
		t.Errorf("a caller's own mistake was uploaded %d times", got)
	}
}

// TestTheScriptGoesOutWithANewlineInFrontOfIt: the whole of the above
// rests on it, and it is one character that nothing else would notice
// going missing.
func TestTheScriptGoesOutWithANewlineInFrontOfIt(t *testing.T) {
	w, f := newFakeSession(t)
	up := serveUpload(t, f, "UpdateScriptTask", func() (int, string) {
		return 200, `<llsd><map><key>state</key><string>complete</string>` +
			`<key>compiled</key><boolean>1</boolean></map></llsd>`
	})

	const src = "default { state_entry() {} }"
	if _, err := w.install(context.Background(), msg.UUID{15: 1}, msg.UUID{15: 2},
		src, true); err != nil {
		t.Fatalf("install: %v", err)
	}
	select {
	case got := <-up.body:
		if string(got) != "\n"+src {
			t.Errorf("sent %q, want it with a newline in front", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("nothing was uploaded")
	}
}

// stopsSent is every SetScriptRunning sent for this script that stops it.
func stopsSent(f *fakeBackend, item msg.UUID) int {
	n := 0
	for _, m := range sentOf[*msg.SetScriptRunning](f) {
		if m.Script.ItemID == item && !m.Script.Running {
			n++
		}
	}
	return n
}

// TestARunStopsItsScriptHoweverItEnds.
//
// A script left running goes on doing what it does -- chatting,
// listening, holding memory -- long after anybody is listening, and an
// object that has one would have it heard as the next run's output.  So
// the run stops it at the end: finished, timed out or interrupted, the
// last on a context of its own, since the run's own is cancelled by then.
func TestARunStopsItsScriptHoweverItEnds(t *testing.T) {
	oAt := func(w *Session) *Object { return foundHere(w, &Object{ID: thePrim, Local: 77}) }
	for _, c := range []struct {
		name string
		end  func(t *testing.T, f *fakeBackend, cancel func())
		keep bool
		want int
	}{
		{"it finished", func(t *testing.T, f *fakeBackend, _ func()) {
			f.Relay(t, objectSaid(thePrim, ChatSay, "FINISHED"))
		}, false, 1},
		{"it timed out", func(*testing.T, *fakeBackend, func()) {}, false, 1},
		{"it was interrupted", func(_ *testing.T, _ *fakeBackend, cancel func()) { cancel() }, false, 1},
		{"it was asked to keep running", func(t *testing.T, f *fakeBackend, _ func()) {
			f.Relay(t, objectSaid(thePrim, ChatSay, "FINISHED"))
		}, true, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			w, f := newFakeSession(t)
			up := serveUpload(t, f, "UpdateScriptTask", compiles)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			wait := aside(t, func() (*Result, error) {
				return w.Run(ctx, Script{
					In: oAt(w), Name: "a script", Source: "default {}", Done: "FINISHED",
					Timeout: 300 * time.Millisecond, KeepRunning: c.keep,
				})
			})
			answerContents(t, f, thePrim, theContentsFile)
			<-up.body
			c.end(t, f, cancel)
			wait()

			if got := stopsSent(f, theChild); got != c.want {
				t.Errorf("%d stops sent for the script, want %d", got, c.want)
			}
		})
	}
}

// TestAnEarlierCopyStillRunningIsStoppedBeforeTheRunListens.
//
// A run's output is the chat from its object, and chat names the object
// and not the script.  A copy left running by a run that was killed
// before it could stop it would be heard as this one -- its "done"
// included -- so it is stopped, and confirmed stopped, before this run
// starts listening and long before the new script goes in.
func TestAnEarlierCopyStillRunningIsStoppedBeforeTheRunListens(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	f.mu.Lock()
	f.scripts[theChild] = true // left running by a run that was killed
	f.mu.Unlock()
	up := serveUpload(t, f, "UpdateScriptTask", compiles)

	wait := aside(t, func() (*Result, error) {
		return w.Run(context.Background(), Script{
			In: foundHere(w, &Object{ID: thePrim, Local: 77}), Name: "a script",
			Source: "default {}", Done: "FINISHED", Timeout: time.Minute,
		})
	})
	answerContents(t, f, thePrim, theContentsFile)

	<-up.asked // the install has begun
	if got := stopsSent(f, theChild); got != 1 {
		t.Errorf("%d stops sent before the install, want the earlier copy stopped", got)
	}
	if got := len(sentOf[*msg.GetScriptRunning](f)); got != 2 {
		t.Errorf("asked %d times whether it was running, want before and after the stop", got)
	}

	<-up.body
	f.Relay(t, objectSaid(thePrim, ChatSay, "FINISHED"))
	res, err := wait()
	if err != nil || !res.Finished || len(res.Warnings) != 0 {
		t.Errorf("Run = %+v, %v", res, err)
	}
}

// TestAnEarlierCopyThatStoppedCostsOneQuestion: a run that ended
// normally stopped its own copy, so the next one only has to ask.
func TestAnEarlierCopyThatStoppedCostsOneQuestion(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	up := serveUpload(t, f, "UpdateScriptTask", compiles)

	wait := aside(t, func() (*Result, error) {
		return w.Run(context.Background(), Script{
			In: foundHere(w, &Object{ID: thePrim, Local: 77}), Name: "a script",
			Source: "default {}", Done: "FINISHED", Timeout: time.Minute,
		})
	})
	answerContents(t, f, thePrim, theContentsFile)
	<-up.asked
	if got := stopsSent(f, theChild); got != 0 {
		t.Errorf("%d stops sent for a copy that was not running", got)
	}
	<-up.body
	f.Relay(t, objectSaid(thePrim, ChatSay, "FINISHED"))
	wait()
}

// TestTheCopyInInventoryIsDeletedOnceTheObjectHasItsOwn: the script goes
// into an object by way of the avatar's inventory, and the copy there has
// done its job once the object holds one.  It used to be left behind, one
// for every object a script was ever put into.
func TestTheCopyInInventoryIsDeletedOnceTheObjectHasItsOwn(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	serveUpload(t, f, "UpdateScriptAgent", compiles)
	up := serveUpload(t, f, "UpdateScriptTask", compiles)
	deleted := make(chan string, 4)
	f.ServeCap(t, "InventoryAPIv3", func(rw http.ResponseWriter, r *http.Request) {
		deleted <- r.Method + " " + r.URL.Path
		rw.WriteHeader(http.StatusOK)
	})

	wait := aside(t, func() (*Result, error) {
		return w.Run(context.Background(), Script{
			In: foundHere(w, &Object{ID: thePrim, Local: 77}), Name: "a script",
			Source: "default {}", Done: "FINISHED", Timeout: time.Minute,
		})
	})
	held := objectHolding(f, thePrim)
	held.answer(t, "")
	m := waitSent[*msg.CreateInventoryItem](t, f)
	relayCreated(t, f, m.InventoryBlock.CallbackID)
	waitSent[*msg.UpdateTaskInventory](t, f)
	held.answer(t, theContentsFile)

	select {
	case got := <-deleted:
		if want := "DELETE /item/" + theChild.String(); got != want {
			t.Errorf("asked %q, want %q", got, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the copy in inventory was never deleted")
	}
	<-up.body
	f.Relay(t, objectSaid(thePrim, ChatSay, "FINISHED"))
	if res, err := wait(); err != nil || len(res.Warnings) != 0 {
		t.Errorf("Run = %+v, %v", res, err)
	}
}
