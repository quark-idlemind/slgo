package sl

import (
	"context"
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

	// Running sets whether the script is started.  The zero value
	// starts it, which is what running a script means.
	NotRunning bool

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
	Compiled bool
	Errors   []string

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

	Elapsed time.Duration
}

// Failed reports whether the script did not get to the end: it either
// would not compile, or it faulted, or it never said it had finished.
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
func (w *Session) Run(ctx context.Context, s Script) (*Result, error) {
	if s.In == nil {
		return nil, fmt.Errorf("sl: Run needs an object to run in")
	}
	if s.Name == "" {
		return nil, fmt.Errorf("sl: Run needs a name for the script")
	}
	if s.Timeout == 0 {
		s.Timeout = 60 * time.Second
	}

	// Find or create the copy inside the object.
	task, err := w.FindInObject(ctx, s.In, s.Name)
	if err != nil {
		return nil, err
	}
	if task == nil {
		it, _, err := w.NewScript(ctx, s.Name, s.Source)
		if err != nil {
			return nil, err
		}
		if err := w.PutInObject(ctx, s.In, it); err != nil {
			return nil, err
		}
		if err := w.Settle(ctx, 6*time.Second); err != nil {
			return nil, err
		}
		task, err = w.FindInObject(ctx, s.In, s.Name)
		if err != nil {
			return nil, err
		}
		if task == nil {
			return nil, fmt.Errorf("sl: %q never turned up inside %s", s.Name, s.In)
		}
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

	start := time.Now()
	up, err := w.upload(ctx, "UpdateScriptTask", map[string]any{
		"item_id":           task.ID.String(),
		"task_id":           s.In.ID.String(),
		"is_script_running": !s.NotRunning,
		"target":            "mono",
	}, []byte(s.Source))
	if err != nil {
		return nil, err
	}

	res := &Result{
		Compiled: up.Compiled,
		Errors:   up.Errors,
		Item:     task.ID,
	}
	if !up.Compiled {
		// Nothing will be said, so do not wait for it.
		res.Lines = col.collected()
		res.Elapsed = time.Since(start)
		return res, nil
	}

	if s.Done == "" {
		// No sentinel: there is nothing to wait for but the clock.
		if err := w.Settle(ctx, s.Timeout); err != nil {
			return nil, err
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
// which leaves the script's state exactly as unknown as it was before.
//
// # The request is not deprecated, and the reply is
//
// GetScriptRunning is an ordinary template message, sent over the
// circuit, and the viewer still sends it that way rather than through
// any capability: it packs one and sends it reliably to the region's
// host at llpreviewscript.cpp:2875-2881, and it has no entry of its own
// in message.xml, so it takes the server default flavour -- "template",
// at message.xml:4-10 -- which is what chooses the template builder over
// the LLSD one (message.cpp:3427-3452).
//
// The REPLY has moved off the circuit.  ScriptRunningReply is marked
// UDPDeprecated in the template (message_template.msg:5510) and
// message.xml gives it the llsd flavour, under a heading that says
// "UDPDeprecated Messages" (message.xml:590-597).  The tell is a field:
// the template's Mono is commented out with "Added to LLSD message"
// (message_template.msg:5516), and the viewer reads Mono by name when a
// reply arrives (llpreviewscript.cpp:3328).  It could not do that off
// the circuit, since the template reader kills the viewer outright when
// asked for a variable its template does not have
// (lltemplatemessagereader.cpp:98-103).  So the reply the viewer
// actually handles is the LLSD one off the event queue, dispatched by
// name into the same handler either transport reaches
// (lleventpoll.cpp:110, llstartup.cpp:3857).
//
// So the question goes out on the circuit -- there is no capability for
// it in the viewer's list or in this grid's -- and the answer is watched
// for on both relays.  Which of them it arrives on is the simulator's
// choice and not this call's, and a grid that still answers on the
// circuit is handled by the type switch in Session.handle.
//
// # What Agni actually does, measured
//
// Second Life answers only on the event queue.  Measured on Agni as hobb,
// in Pelmar Reach, with a second client attached to the same slgod watching
// both relays: a script was installed and started in a rezzed box, "stop"
// was asked for, and NOTHING arrived on the circuit.  Every reply came
// over the queue, in this shape:
//
//	<llsd><map><key>Script</key><array><map>
//	  <key>Running</key><boolean>1</boolean>
//	  <key>ItemID</key><string>d1a87e57-...</string>
//	  <key>Luau</key><boolean>0</boolean>
//	  <key>LuauLanguage</key><boolean>0</boolean>
//	  <key>Mono</key><boolean>1</boolean>
//	  <key>ObjectID</key><string>785f7e57-...</string>
//	</map></array></map></llsd>
//
// Two things in that are worth writing down.  The Script block arrives
// as an ARRAY of maps although the template declares it Single, so it is
// read as a list; and the map carries fields the template has never had
// -- Mono, which the template at least mentions, and Luau and
// LuauLanguage, which it does not and which Agni had grown by August
// 2026.  Only ObjectID, ItemID and Running are read, so the next field
// Linden Lab adds goes past unlooked at.  See Session.scriptRunningEvent.
//
// The first reply said Running 1 and every later one said 0: the stop
// had worked all along and only the confirmation was deaf.  That is the
// reason this waits for a real answer rather than reporting the request
// as the outcome -- and the reason the timeout below still exists.  A
// question can go unanswered, and when it does the caller must be told
// the object has not agreed, never that it has.
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
// Use this rather than NewScript and PutInObject for anything that has
// to RUN. Copying an inventory script into an object over the protocol
// leaves it there and does not start it -- and SetScriptRunning does not
// start it either, because there is nothing compiled to start. Dropping
// a script in by hand looks like one action and is really two: the copy,
// and the save that compiles it into the object.
//
// This is the second half. The source is uploaded through
// UpdateScriptTask, which compiles it inside the object and starts it,
// and is the same call Run makes -- which is why running a script always
// worked while installing a listener never did.
//
// Reusing the name replaces that script rather than adding another: an
// object keeps every copy it is given and renames the newcomer.
func (w *Session) InstallScript(ctx context.Context, o *Object, name, source string, running bool) (*UploadResult, error) {
	if o == nil {
		return nil, fmt.Errorf("sl: InstallScript needs an object")
	}
	if name == "" {
		return nil, fmt.Errorf("sl: InstallScript needs a name")
	}

	task, err := w.FindInObject(ctx, o, name)
	if err != nil {
		return nil, err
	}
	if task == nil {
		it, _, err := w.NewScript(ctx, name, source)
		if err != nil {
			return nil, err
		}
		if err := w.PutInObject(ctx, o, it); err != nil {
			return nil, err
		}
		if err := w.Settle(ctx, 6*time.Second); err != nil {
			return nil, err
		}
		if task, err = w.FindInObject(ctx, o, name); err != nil {
			return nil, err
		}
		if task == nil {
			return nil, fmt.Errorf("sl: %q never turned up inside %s", name, o)
		}
	}

	return w.upload(ctx, "UpdateScriptTask", map[string]any{
		"item_id":           task.ID.String(),
		"task_id":           o.ID.String(),
		"is_script_running": running,
		"target":            "mono",
	}, []byte(source))
}
