package world

import (
	"context"
	"fmt"
	"strings"
	"time"

	"slgo/msg"
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
}

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

	// Finished says the sentinel was seen.  When false, Elapsed is the
	// timeout and the script either was still going or had already
	// stopped without saying so.
	Finished bool
	Elapsed  time.Duration
}

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
func (w *World) Run(ctx context.Context, s Script) (*Result, error) {
	if s.In == nil {
		return nil, fmt.Errorf("world: Run needs an object to run in")
	}
	if s.Name == "" {
		return nil, fmt.Errorf("world: Run needs a name for the script")
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
			return nil, fmt.Errorf("world: %q never turned up inside %s", s.Name, s.In)
		}
	}

	// Listen before compiling.
	col := &collector{source: s.In.ID, sentinel: s.Done, fn: s.OnLine}
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
		select {
		case <-col.found:
			res.Finished = true
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			res.Lines = col.collected()
			res.Elapsed = time.Since(start)
			return res, ctx.Err()
		}
		t.Stop()
	}

	res.Lines = col.collected()
	res.Elapsed = time.Since(start)
	return res, nil
}

// RemoveScripts deletes scripts from an object whose names match, which
// is how to stop leftovers from an earlier run talking over this one.
func (w *World) RemoveScripts(ctx context.Context, o *Object, match func(name string) bool) (int, error) {
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
func (w *World) SetScriptRunning(ctx context.Context, o *Object, item msg.UUID, running bool) error {
	m := &msg.SetScriptRunning{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.Script.ObjectID = o.ID
	m.Script.ItemID = item
	m.Script.Running = running
	return w.Send(ctx, m)
}
