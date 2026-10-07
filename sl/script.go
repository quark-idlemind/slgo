package sl

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// Script says what to run, where, and how to know it has finished.
type Script struct {
	// In is the object the script runs inside.  A script only runs in
	// an object; there is nowhere else to put one.
	In *Object

	// Name is what the script is called inside the object.  Reusing a
	// name replaces that script rather than adding another, which
	// matters because an object keeps every copy it is given and
	// renames the newcomer.
	Name string

	// Source is the LSL.
	Source string

	// Done is the text that means the script has finished.  Run
	// returns as soon as a line contains it.
	//
	// Without one, Run has no way to tell a script that has finished
	// from one that is about to say something else, and can only wait
	// out the whole timeout.  Have the script say something distinctive
	// at the end and name it here.
	Done string

	// Timeout is how long to wait for Done.  Reaching it is not an
	// error by itself -- the lines heard so far are still returned,
	// with Finished false -- because a script that says nothing is a
	// result worth seeing rather than a failure to report.
	Timeout time.Duration

	// OnLine, if set, is called for each line as it is heard, which is
	// what to use when a run is long enough that waiting for the whole
	// transcript is no good.
	OnLine func(Line)

	// NotRunning installs the script without starting it.  The zero
	// value starts it, which is what running a script means.
	NotRunning bool

	// KeepRunning leaves the script running after the run.
	//
	// By default it is stopped when the run ends, however it ends --
	// finished, timed out, faulted or interrupted -- because a script
	// left running goes on doing whatever it does, chatting included,
	// long after anybody is listening.  It stays in the object, stopped,
	// so that the next run with its name updates it in place.
	KeepRunning bool

	// IgnoreFault stops a run-time fault ending the run.
	//
	// By default a fault the simulator blames on this script ends the
	// run at once: the script has stopped, so the sentinel is never
	// coming and waiting out the timeout only delays the answer.  Set
	// this when something else in the object is expected to carry on
	// talking and is worth hearing.
	IgnoreFault bool
}

// faultGrace bounds the wait for the reason after a fault header.
//
// A fault is two messages: the header naming the script, then exactly
// one line with the reason.  Returning on the header alone would say
// the script died without saying what of, so the run waits -- but only
// until that line arrives, which is the usual case and takes
// milliseconds.  This is the cap for when it does not come at all,
// not the pause a fault normally costs.
const faultGrace = 3 * time.Second

// Result is what happened.
type Result struct {
	// Compiled is the verdict.  Errors holds the compiler's
	// complaints, of which there is at most one: the compiler reports
	// the first error and stops.
	//
	// The line and column are the compiler's own, unchanged, and count
	// from zero -- measured, a bad token on the fifth line reports line
	// 4.  The script is sent as given, so they are offsets into what the
	// caller handed over; the viewer shows the same text and puts its
	// cursor on that 0-based row (llpreviewscript.cpp, onErrorList).
	Compiled bool
	Errors   []string

	// State and Message are the rest of what the capability said, kept
	// for the times the errors alone do not explain themselves.  An
	// upload that reached the compiler empty is not one of those:
	// "(0, 0) : ERROR : Syntax error" is sent again once (see install),
	// and the second answer is the one reported.
	State   string
	Message string

	// Answer is the capability's reply as it arrived, kept only when
	// the script did not compile.  The parsed fields are what a caller
	// acts on; this is for the times they do not add up and somebody has
	// to look at what was actually said.
	Answer []byte

	// Item is the script's id inside the object, which is not the id
	// of the inventory item it was copied from.
	Item msg.UUID

	// Lines is everything the object said, in arrival order, including
	// the debug channel the simulator reports run-time errors on.
	Lines []Line

	// Finished says the sentinel was seen.
	Finished bool

	// Fault is set when the simulator blamed a run-time error on this
	// script, which means it stopped where it was.  A run that faults
	// returns as soon as the reason has been heard rather than waiting
	// out the timeout, so Elapsed is short and Finished is false.
	//
	// Only the fatal kind appears here.  "Could not find texture" and
	// its relatives are complaints the script survives; they are in
	// Lines, on the debug channel, and the script runs on.
	Fault *Fault

	// Blocked is why the land will not run this script, where that
	// could be told before the run: scripts are stopped in the region,
	// or the object is within the 50 m above the ground that a parcel's
	// rules reach, on a parcel that runs only its owner's scripts, or
	// its group's as well, and the object is neither its owner's nor in
	// its group.  The simulator reports such a script running and it
	// says nothing, so a blocked run does not wait for it: it returns
	// once the compile is answered, with Finished false.
	//
	// The 50 m is inferred from one measurement, and a worn object is
	// checked only for the region.
	// Why: doc/ground.md#where-the-land-stops-running-scripts
	Blocked string

	Elapsed time.Duration

	// Warnings are what went wrong around the run without spoiling it:
	// a copy that could not be tidied away, an earlier run's script that
	// could not be confirmed stopped.  Worth saying; not worth failing
	// the run over.
	Warnings []string
}

// Failed reports whether the script did not get to the end: it either
// would not compile, or it faulted, or it never said it had finished --
// which a blocked run has not.
func (r *Result) Failed() bool { return !r.Compiled || r.Fault != nil || !r.Finished }

// Said returns the text of every line, which is usually what a test
// wants to assert against.
func (r *Result) Said() []string {
	out := make([]string, 0, len(r.Lines))
	for _, l := range r.Lines {
		out = append(out, l.Text)
	}
	return out
}

// Debug returns only the lines on the debug channel, where run-time
// errors arrive.
func (r *Result) Debug() []Line {
	var out []Line
	for _, l := range r.Lines {
		if l.Debug() {
			out = append(out, l)
		}
	}
	return out
}

// Contains reports whether anything said includes a string.
func (r *Result) Contains(s string) bool {
	for _, l := range r.Lines {
		if strings.Contains(l.Text, s) {
			return true
		}
	}
	return false
}

// Run puts a script in an object, compiles it, and returns what it
// said.
//
// The ordering is what makes this worth having as one call.  Listening
// starts before the compile, because a script says what it has to say
// the instant it is started and a listener opened afterwards has
// already missed it.  The script is looked up inside the object first,
// because the copy in there has its own item id and that is what the
// capability wants.  And the compile result is checked before waiting,
// because waiting a minute for output from something that did not
// compile is a slow way to learn nothing.
//
// Land that will not run the script is looked for before the install,
// and a run on it is not waited out; see Result.Blocked.
//
// A caller that gives up while the script runs gets the Result so far,
// with what was heard until then, together with the context's error.
func (w *Session) Run(ctx context.Context, s Script) (res *Result, err error) {
	if s.In == nil {
		return nil, fmt.Errorf("sl: Run needs an object to run in")
	}
	if s.Name == "" {
		return nil, fmt.Errorf("sl: Run needs a name for the script")
	}
	if s.Timeout == 0 {
		s.Timeout = 60 * time.Second
	}

	var warnings []string

	// Find or create the copy inside the object.
	task, err := w.FindInObject(ctx, s.In, s.Name)
	if err != nil {
		return nil, err
	}
	if task == nil {
		it, _, err := w.NewScript(ctx, s.Name, s.Source)
		if err == nil {
			err = w.copyIntoObject(ctx, s.In, it)
		}
		if err != nil {
			// The copy in inventory was only the way in, and is not
			// left behind when the way in failed either.
			if it != nil {
				err = errors.Join(err, w.dropScriptCopy(ctx, it, s.Name))
			}
			return nil, err
		}
		// Nor when the way in was taken and the object's copy could
		// not be found after it: deleting this one never touches that.
		if err := w.Settle(ctx, 6*time.Second); err != nil {
			return nil, errors.Join(err, w.dropScriptCopy(ctx, it, s.Name))
		}
		task, err = w.FindInObject(ctx, s.In, s.Name)
		if err != nil {
			return nil, errors.Join(err, w.dropScriptCopy(ctx, it, s.Name))
		}
		if task == nil {
			return nil, errors.Join(fmt.Errorf("sl: %q never turned up inside %s", s.Name, s.In),
				w.dropScriptCopy(ctx, it, s.Name))
		}
		// The copy in the avatar's inventory was only the way in.  The
		// object has its own now, which is the one every later run
		// updates, and one of these was being left behind in inventory
		// for every object a script was ever put into.
		if err := w.DeleteItem(ctx, it.ID); err != nil && !errors.Is(err, ErrGone) {
			warnings = append(warnings, fmt.Sprintf(
				"the copy of %q in inventory could not be deleted: %v", s.Name, err))
		}
	} else if note := w.stopEarlier(ctx, s.In, task.ID, s.Name); note != "" {
		warnings = append(warnings, note)
	}

	// Installed all the same where the land will not run it, as asked;
	// only the wait for its output is not.
	var blocked string
	if !s.NotRunning {
		blocked = w.ScriptsBlocked(ctx, s.In)
	}

	// Listen before compiling.
	// The fault watch is by script name: the header the simulator
	// sends names the script, and an object may hold several.  Another
	// script in the same object faulting is not this run's business.
	col := &collector{
		source: s.In.ID, sentinel: s.Done, fn: s.OnLine, faultFor: s.Name,
	}
	w.startCollector(col)
	defer w.stopCollector(col)

	// Stopped when the run ends, however it ends.  Registered before the
	// install rather than after it succeeds, because an install that
	// failed on the way back may still have started the script.  On a
	// context of its own: an interrupted run is the one that most needs
	// this, and its context is already cancelled.  A stop that fails is
	// a warning on the result, or joined to the error when there is no
	// result to carry it.
	if !s.KeepRunning && !s.NotRunning {
		defer func() {
			sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			serr := w.SetScriptRunning(sctx, s.In, task.ID, false)
			if serr == nil {
				return
			}
			if res != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf(
					"%q could not be stopped and may still be running: %v", s.Name, serr))
				return
			}
			err = errors.Join(err, fmt.Errorf(
				"sl: %q could not be stopped and may still be running: %w", s.Name, serr))
		}()
	}

	start := time.Now()
	up, err := w.install(ctx, task.ID, s.In.ID, s.Source, !s.NotRunning)
	if err != nil {
		return nil, err
	}

	res = &Result{
		Warnings: warnings,
		Compiled: up.Compiled,
		Errors:   up.Errors,
		State:    up.State,
		Message:  up.Message,
		Item:     task.ID,
		Blocked:  blocked,
	}
	if !up.Compiled {
		res.Answer = up.Body
	}
	if !up.Compiled || blocked != "" {
		// Nothing will be said, so do not wait for it.
		res.Lines = col.collected()
		res.Elapsed = time.Since(start)
		return res, nil
	}

	if s.Done == "" {
		// No sentinel: there is nothing to wait for but the clock.
		if err := w.Settle(ctx, s.Timeout); err != nil {
			res.Lines = col.collected()
			res.Elapsed = time.Since(start)
			return res, err
		}
	} else {
		t := time.NewTimer(s.Timeout)
		faulted := col.faulted
		if s.IgnoreFault {
			faulted = nil // a nil channel never fires
		}
		select {
		case <-col.found:
			res.Finished = true
		case <-faulted:
			// The script has stopped, so the sentinel is not coming.
			// One more line carries the reason; return as soon as it
			// does.  The sentinel is still watched for because a
			// script can fault after saying it had finished.
			g := time.NewTimer(faultGrace)
			select {
			case <-col.reasoned:
			case <-col.found:
				res.Finished = true
			case <-g.C:
			case <-ctx.Done():
				// Given up on, as below, with the fault as far as
				// it was heard.
				g.Stop()
				t.Stop()
				res.Fault = col.faultSeen()
				res.Lines = col.collected()
				res.Elapsed = time.Since(start)
				return res, ctx.Err()
			}
			g.Stop()
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			res.Lines = col.collected()
			res.Elapsed = time.Since(start)
			return res, ctx.Err()
		}
		t.Stop()
	}
	res.Fault = col.faultSeen()

	res.Lines = col.collected()
	res.Elapsed = time.Since(start)
	return res, nil
}

// dropScriptCopy deletes the inventory copy of a script whose run failed
// while putting it in its object.  On a context of its own, since a run
// that failed because its caller gave up has a cancelled one.
func (w *Session) dropScriptCopy(ctx context.Context, it *Item, name string) error {
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := w.DeleteItem(dctx, it.ID); err != nil && !errors.Is(err, ErrGone) {
		return fmt.Errorf("sl: the copy of %q in inventory could not be deleted: %w", name, err)
	}
	return nil
}

// earlierAnswer bounds the wait for an object to say whether an earlier
// run's script is still running, and earlierDrain is how long to let
// chat already sent by one that has just been stopped arrive before
// listening -- chat and the answer come by different routes, so the
// answer arriving says nothing about the chat.
var (
	earlierAnswer = 5 * time.Second
	earlierDrain  = time.Second
)

// stopEarlier makes sure the script an earlier run left under this name
// is not running before this run starts listening, and says what it
// could not make sure of.
//
// A run's output is the chat that comes from its object, and chat names
// the object and not the script -- so a copy left running by an earlier
// run that was killed before it could stop it would be heard as this
// one, its "done" included, which can end this run before its own
// script has said a word.  Only this name is touched: the object may
// hold scripts that are nothing to do with running this one, and those
// are not this call's to stop.
//
// A run that ended normally has already stopped its copy, so the usual
// cost is one question.
func (w *Session) stopEarlier(ctx context.Context, o *Object, item msg.UUID, name string) string {
	running, err := w.ScriptRunning(ctx, o, item, earlierAnswer)
	if err != nil {
		if ctx.Err() != nil {
			return ""
		}
		return fmt.Sprintf("could not learn whether an earlier %q is still running, "+
			"so its chat may be heard as this run's: %v", name, err)
	}
	if !running {
		return ""
	}
	if err := w.SetScriptRunning(ctx, o, item, false); err != nil {
		return fmt.Sprintf("an earlier %q is running and could not be stopped, "+
			"so its chat may be heard as this run's: %v", name, err)
	}
	if still, err := w.ScriptRunning(ctx, o, item, earlierAnswer); err != nil || still {
		return fmt.Sprintf("an earlier %q was running and was not confirmed stopped, "+
			"so its chat may be heard as this run's", name)
	}
	_ = w.Settle(ctx, earlierDrain)
	return ""
}

// RemoveScripts deletes scripts from an object whose names match, which
// is how to stop leftovers from an earlier run talking over this one.
func (w *Session) RemoveScripts(ctx context.Context, o *Object, match func(name string) bool) (int, error) {
	items, err := w.TaskInventory(ctx, o)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, it := range items {
		if it.Type != "lsltext" || !match(it.Name) {
			continue
		}
		if err := w.RemoveFromObject(ctx, o, it.ID); err != nil {
			return n, err
		}
		n++
	}
	if n > 0 {
		if err := w.Settle(ctx, 6*time.Second); err != nil {
			return n, err
		}
	}
	return n, nil
}

// SetScriptRunning starts or stops a script already inside an object.
func (w *Session) SetScriptRunning(ctx context.Context, o *Object, item msg.UUID, running bool) error {
	m := &msg.SetScriptRunning{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.Script.ObjectID = o.ID
	m.Script.ItemID = item
	m.Script.Running = running
	return w.Send(ctx, m)
}

// ScriptRunning asks whether a script inside an object is running, and
// waits for the object to say.
//
// This is the other half of SetScriptRunning, which is answered by
// nothing at all: the request goes out and the simulator says neither
// yes nor no, so a caller that reported a start had happened would be
// reporting that it had asked.  Asking afterwards is the only way to
// know, and it is the viewer's way too -- its script editor sends this
// the moment it opens a script in a prim, to decide whether the
// "Running" box is ticked.
//
// The timeout is how long to wait for the reply.  Reaching it is not a
// refusal and must not be reported as one: the question went unanswered,
// which leaves the script's state exactly as unknown as it was before,
// and the caller must be told the object has not agreed, never that it
// has.
//
// The question goes out on the circuit, as the viewer sends it.  The
// reply is UDPDeprecated and Second Life sends it only on the event
// queue, so it is watched for on both relays: Session.scriptRunningEvent
// reads the queue's form, and the type switch in Session.handle is for
// a grid that still answers on the circuit.
// Why: doc/scripts.md#whether-a-script-is-running
func (w *Session) ScriptRunning(ctx context.Context, o *Object, item msg.UUID, timeout time.Duration) (bool, error) {
	if o == nil {
		return false, fmt.Errorf("sl: ScriptRunning needs an object")
	}
	if timeout == 0 {
		timeout = 15 * time.Second
	}

	// Listening before asking, for the same reason Run does: the reply
	// is a message like any other and a listener opened after the
	// request has already missed it.
	got := make(chan bool, 1)
	stop := w.onScriptRunning(func(object, it msg.UUID, running bool) {
		// Both ids, because one object may hold several scripts and one
		// script's name may be in several objects.
		if object != o.ID || it != item {
			return
		}
		select {
		case got <- running:
		default:
		}
	})
	defer stop()

	m := &msg.GetScriptRunning{}
	m.Script.ObjectID = o.ID
	m.Script.ItemID = item
	if err := w.Send(ctx, m); err != nil {
		return false, err
	}

	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case running := <-got:
		return running, nil
	case <-t.C:
		return false, fmt.Errorf("%w: whether %s in %s is running", ErrTimeout, item, o)
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

// onScriptRunning is the internal subscription ScriptRunning waits on.
func (w *Session) onScriptRunning(fn func(object, item msg.UUID, running bool)) (stop func()) {
	w.mu.Lock()
	w.scriptFns = append(w.scriptFns, fn)
	i := len(w.scriptFns) - 1
	w.mu.Unlock()
	return func() {
		w.mu.Lock()
		if i < len(w.scriptFns) {
			w.scriptFns[i] = nil
		}
		w.mu.Unlock()
	}
}

// InstallScript puts a script into an object and starts it, returning
// what the compiler said.
//
// Use this rather than NewScript and a copy through UpdateTaskInventory
// for anything that has to RUN. That copy -- what PutInObject sends for
// anything but a script -- leaves the script there and does not start it,
// and SetScriptRunning does not start it either, because there is
// nothing compiled to start. The viewer's drop of a script is a different
// message, RezScript, which the viewer sends for a script dropped on an
// object, with Enabled set to run it (PutInObject sends that for a script; it is
// the only way in for a script the avatar may not modify, whose source
// cannot be read to upload). Here the script is new and its source is
// in hand, so the copy is kept and the source is saved into the object:
// the compile is the one this call reads the verdict of, and a script
// that is not to run is not started.
// Why: doc/scripts.md#dropping-a-script-into-an-object
//
// The source is uploaded through
// UpdateScriptTask, which compiles it inside the object and starts it,
// and is the same call Run makes -- which is why running a script always
// worked while installing a listener never did.  It goes up as Run's
// does, with the source exactly as given: the compiler's line numbers
// are offsets into it, and an answer of "(0, 0)" is sent again, once
// (see install).
//
// Reusing the name replaces that script rather than adding another: an
// object keeps every copy it is given and renames the newcomer.
//
// A script not in the object yet goes in by way of a copy in the
// avatar's inventory, which is deleted afterwards as Run deletes its
// own, whether or not the install got any further.  A copy that could
// not be deleted is a warning on the result, or part of the error when
// there is no result.
func (w *Session) InstallScript(ctx context.Context, o *Object, name, source string, running bool) (*UploadResult, error) {
	if o == nil {
		return nil, fmt.Errorf("sl: InstallScript needs an object")
	}
	if name == "" {
		return nil, fmt.Errorf("sl: InstallScript needs a name")
	}

	var leftover error // deleting the copy, when that failed
	task, err := w.FindInObject(ctx, o, name)
	if err != nil {
		return nil, err
	}
	if task == nil {
		it, _, err := w.NewScript(ctx, name, source)
		if err == nil {
			err = w.copyIntoObject(ctx, o, it)
		}
		if err != nil {
			if it != nil {
				err = errors.Join(err, w.dropScriptCopy(ctx, it, name))
			}
			return nil, err
		}
		if err := w.Settle(ctx, 6*time.Second); err != nil {
			return nil, errors.Join(err, w.dropScriptCopy(ctx, it, name))
		}
		if task, err = w.FindInObject(ctx, o, name); err != nil {
			return nil, errors.Join(err, w.dropScriptCopy(ctx, it, name))
		}
		if task == nil {
			return nil, errors.Join(fmt.Errorf("sl: %q never turned up inside %s", name, o),
				w.dropScriptCopy(ctx, it, name))
		}
		if err := w.DeleteItem(ctx, it.ID); err != nil && !errors.Is(err, ErrGone) {
			leftover = fmt.Errorf("the copy of %q in inventory could not be deleted: %w", name, err)
		}
	}

	up, err := w.install(ctx, task.ID, o.ID, source, running)
	if err != nil {
		return nil, errors.Join(err, leftover)
	}
	if leftover != nil {
		up.Warnings = append(up.Warnings, leftover.Error())
	}
	return up, nil
}

// emptyUpload is the compiler's answer to a body that did not arrive,
// and also to a script that is wrong at its very first character:
// measured, both say it.  An upload is sent again on it, once.
// Why: doc/slots.md#the-compilers-line-numbers-count-from-zero
const emptyUpload = "(0, 0)"

// install puts a script into an object, sending the source as given and
// asking a second time when the answer is "(0, 0)" throughout.
//
// That answer is an upload that arrived empty, or a script wrong at its
// first character; they cannot be told apart, so it is asked twice and
// the second answer is reported.  A caller's own mistake costs one
// extra upload and is not hidden.  Once: a second "(0, 0)" is worth
// reporting rather than chasing.
func (w *Session) install(ctx context.Context, item, object msg.UUID, source string, running bool) (*UploadResult, error) {
	send := func() (*UploadResult, error) {
		return w.upload(ctx, "UpdateScriptTask", map[string]any{
			"item_id":           item.String(),
			"task_id":           object.String(),
			"is_script_running": running,
			"target":            "mono",
		}, scriptBody(source))
	}

	up, err := send()
	if err != nil || up.Compiled || !arrivedEmpty(up) {
		return up, err
	}
	return send()
}

// scriptBody is what goes up for a script's source: the text as given,
// except that an empty one is a single space, as the viewer does
// (LLScriptEdCore::writeToFile: "a completely empty script - stuff in
// one space so it can store properly", SL-46889).
func scriptBody(source string) []byte {
	if source == "" {
		return []byte(" ")
	}
	return []byte(source)
}

// arrivedEmpty is whether the compiler was handed nothing.
func arrivedEmpty(up *UploadResult) bool {
	if len(up.Errors) == 0 {
		return false
	}
	for _, e := range up.Errors {
		if !strings.HasPrefix(strings.TrimSpace(e), emptyUpload) {
			return false
		}
	}
	return true
}
