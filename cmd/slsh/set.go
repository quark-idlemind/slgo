package main

// The settings, from inside the shell.
//
// Everything slsh can be told lived in one hand-edited file and nowhere
// else: to find out what could be set you read the head of config.go,
// and to change one you left the shell, opened an editor, and started
// again.  A person who has just been drawn a map that is the wrong
// shape for their font should be able to say so where they are.
//
// # Why it is called set
//
// Because that is the word a shell user reaches for, and because bare
// "set" listing everything is exactly what a shell's set does.  The
// obvious objection is that a shell's set is about variables and this
// one is not, and it comes to nothing here: slsh has no variables for
// it to be confused with, and if it ever does, they will be the thing
// somebody types "set" expecting to see.
//
// # What it writes
//
// The file, every time, because a setting that lasted until the shell
// was closed would be a setting somebody had to type again after every
// crash and every reboot -- and the whole complaint was about having to
// say the same thing twice.  See saveSetting for what it does to the
// rest of the file, which is nothing.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

var setCommands = map[string]*command{
	"set": {
		params: "[NAME [VALUE]]",
		flags:  func() any { return new(helpOnly) },
		brief:  "the settings and their values; \"set NAME VALUE\" changes one and remembers it",
		man:    "set",
		run:    cmdSet,
	},
}

// cmdSet lists the settings, one setting, or changes one.
//
// The three forms are told apart by how many words follow, which is
// what a shell's set does and what makes it typeable: "set" is the
// question somebody has when they cannot remember the name, "set NAME"
// when they cannot remember the value, and "set NAME VALUE" when they
// know both.
func cmdSet(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o helpOnly
	args, done, err := subOptions("set", &o, out, args)
	if err != nil || done {
		return err
	}

	if len(args) == 0 {
		for _, s := range sortedSettings() {
			showSetting(out, sh.cfg, s)
		}
		fmt.Fprintf(out, "\"set NAME VALUE\" changes one, and writes it to %s\n", configPathOr())
		return nil
	}

	s, ok := findSetting(args[0])
	if !ok {
		// The names rather than "try set", because the commonest way to
		// arrive here is a near miss -- map_colour for
		// map_friend_colour -- and the answer is one line away.
		return fmt.Errorf("no setting called %q; there is %s",
			args[0], strings.Join(settingNames(), ", "))
	}
	if len(args) == 1 {
		showSetting(out, sh.cfg, s)
		return nil
	}

	// The rest of the line, joined with single spaces.  Several of these
	// are commands with arguments in them -- viewer_launch is a whole
	// command line -- and quoting the lot is a thing to have to
	// remember; "set viewer_grid my grid" means what it looks like.
	value := strings.Join(args[1:], " ")

	// "auto" belongs to one setting, and typing it at any other is
	// refused here, in the command, rather than left to a row of the
	// table.  A table where any row might turn out to have a magic word
	// in it is a table nobody can predict: the only way to find out
	// whether "set addr auto" measured something or set the address to
	// the word "auto" would be to type it and look afterwards.  So
	// there is exactly one row the word means anything for, it is named
	// here, and everywhere else it is an error with that name in it.
	// The price is a viewer_grid that cannot be called "auto", which is
	// a nickname nobody has, against a rule that can be stated in one
	// line and holds for every setting there will ever be.
	var note string
	if strings.EqualFold(strings.TrimSpace(value), autoWord) {
		if s.name != autoSetting {
			return fmt.Errorf("%s: only %s takes %q; every other setting takes the value itself",
				s.name, autoSetting, autoWord)
		}
		measured, err := measureCellRatio(sh.term)
		if err != nil {
			return fmt.Errorf("%s: %w", s.name, err)
		}
		value, note = measured, autoNote
	}

	// Parsed into a copy first, so that a value the setting will not
	// take changes neither this shell nor the file.  What is printed
	// afterwards is read back OUT of that copy rather than echoed:
	// "set escape ^g" answers ^G, which is the spelling the file has,
	// and a person who typed something that means the same as what was
	// already there can see that it does.
	next := sh.cfg
	if err := s.parse(&next, value); err != nil {
		return fmt.Errorf("%s: %w", s.name, err)
	}
	// Written before it is applied, and the shell left alone when the
	// writing fails.  A change this shell had taken and the file had
	// not is the one outcome nobody could work out afterwards: the
	// picture would be drawn one way today and another way tomorrow,
	// with nothing on the screen having said so.
	path, err := saveSetting(s, s.show(&next))
	if err != nil {
		if path == "" {
			// There is no settings file to name, which is a machine
			// with no home directory rather than a file that would not
			// open.
			return fmt.Errorf("%s: %w", s.name, err)
		}
		return fmt.Errorf("%s: %s: %w", s.name, path, err)
	}

	if !s.startup {
		sh.cfg = next
	}
	fmt.Fprintf(out, "%s = %s\n", s.name, valueOrEmpty(s.show(&next)))
	fmt.Fprintf(out, "written to %s\n", path)
	if s.startup {
		fmt.Fprintf(out, "%s\n", startupNote)
	}
	if note != "" {
		fmt.Fprintf(out, "%s\n", note)
	}
	return nil
}

// autoWord is the word that says "measure it" where a value would go,
// and autoSetting is the one setting it means anything for.  See
// cmdSet, which is where that is enforced and why.
const (
	autoWord    = "auto"
	autoSetting = "map_ratio"
)

// autoNote is what "auto" says after it has worked.
//
// It says the number is a starting point, because it is one: what the
// terminal reports is the cell it hands the font, which is not always
// the shape the letters look.  On the machine this was written on the
// terminal said 18:10 and 18:9 drew the squarer picture.  So the last
// word is a square region looked at, which takes one line and one look
// -- and somebody who has just been given a number is exactly who is
// in a position to try it.
const autoNote = "what the terminal reports is where to start, not the answer:\n" +
	"draw a region with \"map --region\" and tweak until a square one looks square"

// measureCellRatio asks the terminal how big a character cell is and
// writes the answer the way map_ratio is written.
//
// # The numbers are the ones reported, and are not reduced
//
// 18:10 and 9:5 are the same ratio and are not the same starting point.
// The reported number is where somebody begins, not where they end --
// see autoNote -- and the person who measured this went from a reported
// 18:10 to 18:9 by changing one digit.  Reduced to 9:5 that same tweak
// is a sum to do first and a bigger step to take, for nothing gained:
// the picture is drawn from the shape of the ratio and not from the
// size of its numbers, so carrying the reported ones costs nothing at
// all.
//
// # Why it goes through ParseCellRatio like everything else
//
// So that what is written is a value the file can be read back with.
// A terminal that reports its cells in the pixels of a very dense
// screen can name numbers outside what map_ratio takes, and the honest
// end of that is a refusal here, with the pixels in it, rather than a
// settings file that will not load tomorrow morning.
func measureCellRatio(t *Term) (string, error) {
	if t.Plain() {
		// "slsh -c", "slsh -f" and a piped session all land here.  There
		// is a terminal in none of them to ask, and "man set" has the
		// by-eye method that does not need one.
		return "", errors.New("no terminal here to ask; see \"man set\" for measuring it by eye")
	}
	tall, wide, err := t.CellSize(cellSizeWait)
	if err != nil {
		return "", fmt.Errorf("%w; see \"man set\" for measuring it by eye", err)
	}
	r, err := ParseCellRatio(fmt.Sprintf("%d:%d", tall, wide))
	if err != nil {
		return "", fmt.Errorf("the terminal reports a cell %d pixels tall and %d wide: %w",
			tall, wide, err)
	}
	return r.String(), nil
}

// showSetting is one setting in the listing: its value on one line and
// what it is for on the next.
//
// Two lines rather than three columns.  A value can be a whole command
// line -- viewer_launch is seventy characters of it -- and a column
// wide enough for that leaves no room for the sentence beside it, while
// a column too narrow to hold it turns the one setting somebody is
// most likely to be reading into something they cannot read.
func showSetting(out io.Writer, cfg Config, s setting) {
	fmt.Fprintf(out, "%-17s %s\n", s.name, valueOrEmpty(s.show(&cfg)))
	about := s.about
	if s.startup {
		// Said in the listing as well as when one is changed, because
		// this is where somebody decides what to type: finding out
		// afterwards that it will not take until next time is finding
		// out too late to do anything else.
		about += " -- at startup only"
	}
	fmt.Fprintf(out, "    %s\n", about)
}

// valueOrEmpty is how a value with nothing in it is printed.
//
// Empty is a real answer for several of these -- no address means ask
// sl-host, no viewer_running means do not check -- and a blank space
// after the name reads as a listing that has gone wrong rather than as
// a setting nobody has filled in.
func valueOrEmpty(v string) string {
	if v == "" {
		return "(empty)"
	}
	return v
}

// configPathOr names the file for the listing's last line, and says so
// vaguely rather than failing when there is no home directory to put it
// in.  Nothing has been written at that point, so there is nothing to
// report going wrong; the writing itself says so if it comes to that.
func configPathOr() string {
	path, err := ConfigPath()
	if err != nil {
		return "the settings file"
	}
	return path
}
