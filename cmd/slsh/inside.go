package main

// What is inside a rezzed object.
//
// An object holds inventory of its own -- scripts, notecards, whatever
// was dropped in it -- and it is a different place from the avatar's
// inventory, with its own ids.  The copy of a script inside a prim is
// not the item it came from, and every operation on it wants the copy's
// id rather than the original's.
//
// So the same three verbs take --in OBJECT and work there instead:
//
//	ls --in Box1              what the object holds
//	rm --in Box1 hello.lsl    delete one of them
//	mv --in Box1 old new      rename one of them
//
// A flag rather than three more words, because these are not different
// operations: they are listing, deleting and renaming, in a container
// that happens to be a prim.  What is not the same is putting something
// in, and that has its own command: "give" offers an item to a person
// and waits for them to accept, while dropping one into your own object
// happens at once and asks nobody.
//
// # Why start and stop are verbs rather than two more --in flags
//
// The flag works above because each of those three is an operation the
// shell already has somewhere else, and --in only says which container
// to perform it in.  Starting a script has no counterpart: a script in
// inventory does not run and cannot be made to, because an object is the
// only place a script runs at all.  So there is nothing for a flag to
// choose between.  "run --in Box1 hello.lsl" would be a flag with one
// legal value, which is a verb spelled at length -- and it would put the
// object, the one argument that is never optional, behind a flag.
//
// So the object is an argument, in the place drop puts it:
//
//	start Box1 hello.lsl   start one script
//	start Box1             start every script in the object
//	stop Box1 hello.lsl
//
// and, as in drop, the first word is the object and everything after it
// is one name.  An object whose name has a space in it is quoted; a
// script whose name has one need not be.
//
// The names are the plainest words for it.  The viewer has no verb to
// borrow -- its script editor shows a "Running" tick box, and "running"
// and "unrunning" are not a pair of commands -- and "run" is worse than
// it looks, because sl.Run means putting a script in, compiling it and
// waiting for what it says, which is a different and much longer act
// than flipping a switch on one that is already there.
//
// # Why "new --in" is a flag after all
//
// It goes the other way round because making a script IS an operation
// that exists in both places, and --in says which.  What differs is what
// happens afterwards: a script made in inventory sits there, and a
// script put into an object is compiled and started by the same call
// that puts it there (see sl.InstallScript), so the two report different
// things and the command says which of them it did.
//
// A notecard cannot be made inside an object at all -- nothing here can
// write one into a prim -- so --in without --kind means a script, since
// a script is the only thing it could mean, and --kind notecard with
// --in is refused rather than quietly made in inventory instead.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

var insideCommands = map[string]*command{
	"drop": {
		params: "OBJECT PATH",
		flags:  func() any { return new(helpOnly) },
		brief:  "put an inventory item inside a rezzed object",
		man:    "drop",
		run:    cmdDrop,
	},
	"new": {
		params: "PATH",
		flags:  func() any { return new(newFlags) },
		brief:  "make a notecard or a script; --in puts a script in an object and starts it",
		man:    "new",
		run:    cmdNew,
	},
	"start": {
		params: "OBJECT [SCRIPT]",
		flags:  func() any { return new(runningFlags) },
		brief:  "start a script inside a rezzed object, or every script in it",
		man:    "start",
		run:    cmdStart,
	},
	"stop": {
		params: "OBJECT [SCRIPT]",
		flags:  func() any { return new(runningFlags) },
		brief:  "stop a script inside a rezzed object, or every script in it",
		man:    "stop",
		run:    cmdStop,
	},
}

// insideObject resolves what --in named, and says so in the error when
// it is not there: a mistyped object name and an empty object are very
// different answers to "why did nothing happen".
//
// wait is seconds to let the region describe itself, and zero is the
// thirty waitFor gives everything else that looks something up.
func (sh *Shell) insideObject(ctx context.Context, what string, wait int) (*sl.Object, error) {
	o, err := sh.objectNamed(ctx, strings.TrimSpace(what), wait)
	if err != nil {
		return nil, err
	}
	return o, nil
}

// listInside prints an object's contents, in the columns ls uses: name
// last, so that a name with spaces in it cannot run into anything.
func (sh *Shell) listInside(ctx context.Context, out io.Writer, what string, long bool) error {
	o, err := sh.insideObject(ctx, what, 0)
	if err != nil {
		return err
	}
	items, err := sh.s.TaskInventory(ctx, o)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		fmt.Fprintf(out, "%s holds nothing\n", o.Name)
		return nil
	}
	// The columns ls uses: kind first, id where ls puts it, and the
	// name last so that a name with spaces cannot run into anything.
	for _, it := range items {
		if long {
			fmt.Fprintf(out, "%-10s %-36s %s\n", it.Type, it.ID, it.Name)
			continue
		}
		fmt.Fprintf(out, "%-10s %s\n", it.Type, it.Name)
	}
	return nil
}

// findInside is sl.FindInObject for the two commands that must find
// something, and is the shell's sentence for not finding it.
//
// sl.FindInObject reports "it is not in there" as a nil item and no
// error, and does it deliberately: sl.Run and sl.InstallScript ask
// whether a script is in the object in order to decide whether to put
// one there, so not being there is an answer they act on rather than a
// failure -- sl/inventory_test.go pins that down by name.  Teaching it
// to return an error would break both of those to save a nil check
// here.  So the check belongs at the call sites that did mean to find
// something, and rm --in and mv --in are both of them: each read the
// item straight back and panicked on the nil.
//
// The refusal is scriptsIn's sentence with a wider noun.  An object
// holds notecards and textures beside its scripts and these two verbs
// work on all of them, so it says "nothing called" rather than "no
// script called", and it names the object as well as the missing item:
// with several boxes about, which one was asked is half the answer.
func (sh *Shell) findInside(ctx context.Context, o *sl.Object, name string) (*sl.TaskItem, error) {
	it, err := sh.s.FindInObject(ctx, o, name)
	if err != nil {
		return nil, err
	}
	if it == nil {
		return nil, fmt.Errorf("%s holds nothing called %q; \"ls --in\" lists what it does hold",
			o.Name, name)
	}
	return it, nil
}

// removeInside deletes items from inside an object.
//
// Deleting, not taking: the message says remove and the copy is gone.
// Anything wanted back has to come from the original in inventory,
// which is why this says what it deleted rather than reporting a count.
func (sh *Shell) removeInside(ctx context.Context, out io.Writer, what string, names []string) error {
	o, err := sh.insideObject(ctx, what, 0)
	if err != nil {
		return err
	}
	for _, name := range names {
		it, err := sh.findInside(ctx, o, name)
		if err != nil {
			return err
		}
		if err := sh.s.RemoveFromObject(ctx, o, it.ID); err != nil {
			return err
		}
		fmt.Fprintf(out, "deleted %q from %s\n", it.Name, o.Name)
	}
	return nil
}

// renameInside renames one item inside an object.
func (sh *Shell) renameInside(ctx context.Context, out io.Writer, what string, from, to string) error {
	o, err := sh.insideObject(ctx, what, 0)
	if err != nil {
		return err
	}
	it, err := sh.findInside(ctx, o, from)
	if err != nil {
		return err
	}
	if err := sh.s.RenameInObject(ctx, o, *it, to); err != nil {
		return err
	}
	fmt.Fprintf(out, "%q in %s is now %q\n", from, o.Name, to)
	return nil
}

func cmdDrop(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o helpOnly
	args, done, err := subOptions("drop", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) < 2 {
		return usageError("drop")
	}

	obj, err := sh.insideObject(ctx, args[0], 0)
	if err != nil {
		return err
	}
	// thingAt and not entryAt: the id is going into the object as the
	// thing itself, so a link is followed to what it points at.  The
	// viewer refuses this case outright instead -- "No giving away
	// links", lltooldraganddrop.cpp:2147-2148 -- which is defensible,
	// since a link inside an object points at nothing an object can
	// reach.  Putting in what was meant is better than refusing.
	e, err := sh.thingAt(ctx, strings.Join(args[1:], " "))
	if err != nil {
		return err
	}
	if e.Folder {
		return fmt.Errorf("%s is a folder; drop takes one item", args[1])
	}
	// The item itself, not the entry: what goes over the wire is every
	// field of it, and anything left out is set to zero -- which for a
	// permission mask means taking the rights away.
	it, err := sh.itemAt(ctx, e)
	if err != nil {
		return err
	}
	if err := sh.s.PutInObject(ctx, obj, it); err != nil {
		return err
	}
	fmt.Fprintf(out, "put %q in %s\n", it.Name, obj.Name)
	return nil
}

type newFlags struct {
	In   string `getopt:"--in=OBJECT   make it inside a rezzed object, where a script is compiled and started"`
	Kind string `getopt:"--kind=KIND   notecard or script [notecard, or script with --in]"`
	From string `getopt:"--from=FILE   its contents; without this it is empty"`
	Wait int    `getopt:"--wait -w=SECONDS  how long to let the region describe itself [30]"`
	Help bool   `getopt:"--help -h     show what this command takes"`
}

// cmdNew makes a notecard or a script.
//
// Both are free, and that is not a detail: an upload of a texture costs
// L$ and this does not, because a notecard and a script go up through
// their own capability rather than through the asset upload that
// charges.
func cmdNew(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o newFlags
	args, done, err := subOptions("new", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) < 1 {
		return usageError("new")
	}

	body := ""
	if o.From != "" {
		b, err := os.ReadFile(o.From)
		if err != nil {
			return err
		}
		body = string(b)
	}

	path := strings.Join(args, " ")
	names := sl.SplitPath(path)
	if len(names) == 0 {
		return fmt.Errorf("new: no name given")
	}
	if o.In != "" {
		return sh.newInside(ctx, out, o, names, body)
	}

	// The folder part goes through resolveDir, the way mkdir's does,
	// so that a path without a leading slash is read from the working
	// folder.  Calling folderAt straight was the bug, and this was the
	// only command with it: with the shell in /new-check, "new README"
	// made the notecard at /README, and "new sub/note" was refused
	// with no folder "sub" while /new-check/sub was there.
	name := names[len(names)-1]
	parentPath := ""
	if strings.HasPrefix(path, "/") {
		parentPath = "/"
	}
	parentPath += sl.JoinPath(names[:len(names)-1]...)
	_, parent, err := sh.resolveDir(ctx, parentPath)
	if err != nil {
		return err
	}

	switch strings.ToLower(o.Kind) {
	case "", "notecard":
		it, err := sh.s.CreateItem(ctx, name, "", int8(sl.AssetNotecard), int8(sl.AssetNotecard))
		if err != nil {
			return err
		}
		if err := sh.s.MoveItem(ctx, it.ID, parent); err != nil {
			return err
		}
		if body != "" {
			if _, err := sh.s.SaveNotecard(ctx, it.ID, body); err != nil {
				return err
			}
		}
		fmt.Fprintf(out, "%s %s\n", it.Name, it.ID)
	case "script":
		// NewScript makes the item and writes the source in one go,
		// because a script with no source is a compile error waiting
		// to happen the moment anything runs it.
		if body == "" {
			body = emptyScript
		}
		it, res, err := sh.s.NewScript(ctx, name, body)
		if err != nil {
			return err
		}
		if err := sh.s.MoveItem(ctx, it.ID, parent); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s %s\n", it.Name, it.ID)
		if res != nil && !res.Compiled {
			fmt.Fprintf(out, "it did not compile\n")
		}
	default:
		return fmt.Errorf("--kind is notecard or script, not %q", o.Kind)
	}
	return nil
}

// emptyScript is what a script with no source is given, because a script
// with none at all is a compile error waiting to happen.
const emptyScript = "default\n{\n    state_entry()\n    {\n    }\n}\n"

// newInside makes a script inside a rezzed object.
//
// InstallScript is what does it, and it does more than the inventory
// half: copying a script into an object leaves it there without
// compiling it, so the source is uploaded through UpdateScriptTask,
// which compiles it inside the object and starts it.  That is why this
// says the script is running and "new" on its own does not -- the
// difference is real and belongs in the output rather than in the
// manual.
//
// Reusing a name replaces that script rather than adding another, which
// is InstallScript's doing and worth knowing before it happens: an
// object renames every copy it is given, so a command that added would
// leave "hello.lsl" and "hello.lsl 1" both running.
func (sh *Shell) newInside(ctx context.Context, out io.Writer, o newFlags, names []string, body string) error {
	switch strings.ToLower(o.Kind) {
	case "", "script":
	case "notecard":
		// Not a generic complaint about the kind: the kind is a real one
		// and what is missing is a call.  A notecard inside a prim is
		// written through the object's own inventory rather than the
		// agent's, and nothing in the sl package does that -- so saying
		// "notecard or script" here would be a lie about what --kind
		// takes.
		return fmt.Errorf("nothing here can write a notecard inside an object, only a script; " +
			"make the notecard in inventory with \"new\" and put it in with \"drop\"")
	default:
		return fmt.Errorf("--kind is notecard or script, not %q", o.Kind)
	}

	// An object holds no folders, so a path names nothing it could go
	// into; ls --in refuses one for the same reason.
	if len(names) > 1 {
		return fmt.Errorf("an object holds no folders, so new --in takes a name and not a path")
	}
	name := names[0]
	if body == "" {
		body = emptyScript
	}

	obj, err := sh.insideObject(ctx, o.In, o.Wait)
	if err != nil {
		return err
	}
	res, err := sh.s.InstallScript(ctx, obj, name, body, true)
	if err != nil {
		return err
	}
	if res != nil && !res.Compiled {
		// The script is in there and is not running: nothing compiled,
		// so there is nothing to start.  The compiler reports the first
		// error and stops, so this is one line and worth printing whole.
		fmt.Fprintf(out, "%s is in %s and did not compile\n", name, obj.Name)
		for _, e := range res.Errors {
			fmt.Fprintf(out, "  %s\n", e)
		}
		return nil
	}
	fmt.Fprintf(out, "%s is in %s and running\n", name, obj.Name)
	return nil
}

type runningFlags struct {
	Wait int  `getopt:"--wait -w=SECONDS  how long to give the object to agree it changed [15]"`
	Help bool `getopt:"--help -h          show what this command takes"`
}

func cmdStart(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	return sh.setRunning(ctx, out, args, true)
}

func cmdStop(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	return sh.setRunning(ctx, out, args, false)
}

// setRunning is start and stop, which differ only in the state they ask
// for and in the word they print.
//
// # What it prints, and why every script gets a line
//
// One line per script, in the columns a listing uses, with the name
// last:
//
//	started    hello.lsl
//	already    listener
//	no answer  watcher
//
// A single count would not do.  "start Box1" on an object of six is one
// request per script and they do not have to agree with each other: a
// script may already be running, and a script may not answer at all, and
// which of the six that was is the whole of what somebody needs to know
// next.  So nothing stops at the first script that will not change --
// every one of them is asked, and every one of them gets its line --
// and the count of those that did not agree is what the command finally
// fails with, once the listing that explains it is already on the
// screen.
//
// "already" is a line and not an error.  A script that is running when
// somebody asks for it to run is in the state they wanted; saying
// "started" would be a claim about something that did not happen, and
// failing would be a complaint about getting what was asked for.
func (sh *Shell) setRunning(ctx context.Context, out io.Writer, args []string, running bool) error {
	verb, changed := "stop", "stopped"
	if running {
		verb, changed = "start", "started"
	}

	var o runningFlags
	args, done, err := subOptions(verb, &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) < 1 {
		return usageError(verb)
	}

	obj, err := sh.insideObject(ctx, args[0], 0)
	if err != nil {
		return err
	}
	scripts, err := sh.scriptsIn(ctx, obj, strings.Join(args[1:], " "))
	if err != nil {
		return err
	}

	stuck := 0
	for _, it := range scripts {
		word, agreed, err := sh.changeScript(ctx, obj, it, running, o.Wait, changed)
		if err != nil {
			// The circuit rather than the script: nothing after this
			// would go out either, so stopping says so once instead of
			// once per script.  What has already been decided is
			// printed above and stands.
			return err
		}
		if !agreed {
			stuck++
		}
		fmt.Fprintf(out, "%-10s %s\n", word, it.Name)
	}

	if stuck > 0 {
		// One script is named and several are counted, because "1 of 1
		// scripts has not agreed" is a sentence about arithmetic and the
		// name is what somebody would go and look at.
		what := fmt.Sprintf("%d of %d scripts %s", stuck, len(scripts), plural(stuck, "has", "have"))
		if len(scripts) == 1 {
			what = fmt.Sprintf("%q has", scripts[0].Name)
		}
		return fmt.Errorf("%s not agreed to %s; the request went out for each of them, "+
			"so it may yet have happened -- %q again says what the object thinks now",
			what, verb, verb+" "+args[0])
	}
	return nil
}

// scriptsIn is the scripts inside an object: the one named, or all of
// them when nothing is named.
//
// "lsltext" is what the contents file calls a script, and is what tells
// one from the notecards and textures beside it; sl.RemoveScripts
// filters on the same word.  Starting a notecard is not a thing, so a
// command given no name asks about scripts rather than about everything
// the object holds.
func (sh *Shell) scriptsIn(ctx context.Context, o *sl.Object, name string) ([]sl.TaskItem, error) {
	items, err := sh.s.TaskInventory(ctx, o)
	if err != nil {
		return nil, err
	}
	var scripts []sl.TaskItem
	for _, it := range items {
		if it.Type != "lsltext" {
			continue
		}
		if name != "" && it.Name != name {
			continue
		}
		scripts = append(scripts, it)
	}
	switch {
	case len(scripts) > 0:
		return scripts, nil
	case name == "":
		return nil, fmt.Errorf("%s holds no scripts; \"ls --in\" lists what it does hold", o.Name)
	}
	return nil, fmt.Errorf("%s holds no script called %q; \"ls --in\" lists what it does hold", o.Name, name)
}

// scriptAsk is how long one GetScriptRunning is given to be answered.
//
// Short, and short on purpose: what is waited for is the script reaching
// a state rather than any one reply, so a question nobody answered is
// followed by another question instead of by a longer wait.  It is also
// the smallest --wait that means anything, since a question already in
// flight is not abandoned when the deadline passes.
const scriptAsk = 3 * time.Second

// changeScript starts or stops one script and says what happened to it.
//
// The state is read BEFORE anything is sent, so that a script already in
// the state asked for is reported as that rather than as a change that
// did not happen.  A question that goes unanswered does not stop the
// request: the answer arrives on the event queue, which is a long poll
// and may be between rounds -- see sl.ScriptRunning -- so treating one
// slow answer as a verdict would turn a working start into a refusal.
//
// The error is for the circuit going away, which is not this script's
// business and ends the command.  A script that will not change state is
// not an error here: it is a word in the listing, so that the scripts
// after it are still asked.
func (sh *Shell) changeScript(ctx context.Context, o *sl.Object, it sl.TaskItem,
	running bool, wait int, changed string) (word string, agreed bool, err error) {

	if was, err := sh.s.ScriptRunning(ctx, o, it.ID, scriptAsk); err == nil && was == running {
		return "already", true, nil
	}
	if err := sh.s.SetScriptRunning(ctx, o, it.ID, running); err != nil {
		return "", false, err
	}
	ok, err := sh.confirmRunning(ctx, o, it.ID, running, wait)
	if err != nil {
		return "", false, err
	}
	if !ok {
		return "no answer", false, nil
	}
	return changed, true, nil
}

// confirmRunning waits until the object agrees a script is running, or
// is not, and says whether it ever did.
//
// Nothing replies to SetScriptRunning, so this is the whole of what
// stands between the command and a sentence it has not established --
// the mistake TeleportLocal's doc comment was written about.  Not
// agreeing is not the same as failing: the request went out and may yet
// take effect, which is what the caller prints, so this reports it as
// false rather than as an error.
//
// The error is the context's alone.  A question that fails or goes
// unanswered is only this one question, and the answer to that is to ask
// again until the deadline.
func (sh *Shell) confirmRunning(ctx context.Context, o *sl.Object, item msg.UUID, want bool, seconds int) (bool, error) {
	if seconds <= 0 {
		seconds = 15
	}
	deadline := time.Now().Add(time.Duration(seconds) * time.Second)
	for {
		running, err := sh.s.ScriptRunning(ctx, o, item, scriptAsk)
		if err == nil && running == want {
			return true, nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return false, err
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		select {
		case <-time.After(300 * time.Millisecond):
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
}
