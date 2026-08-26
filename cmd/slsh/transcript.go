package main

// A transcript of the session: what was heard, what was said, and every
// command that was run.
//
// It is written here rather than in slgod because this is where the
// three of them exist at once.  The daemon relays messages as undecoded
// bytes and says so in as many words -- "it does not decode message
// bodies, hold an inventory, understand chat, or know what a script is"
// (server/server.go) -- and it never sees a command line at all, since
// parsing one and running it is the whole of what slsh does.  What the
// daemon could log is packets; what a person wants is what they saw.
//
// The price is stated so nobody has to discover it: an avatar nobody is
// attached to writes nothing.  slgod stays logged in and keeps hearing,
// and none of that reaches a file until a shell is there to hear it.
//
// # One line, one write
//
// Two shells may be attached to one avatar, and both then hold the same
// file open.  Every line is one Write to a file opened O_APPEND, which
// is what keeps two of them from interleaving halfway through a
// sentence: the offset and the write are one operation under POSIX, so
// concurrent appends of a whole line each arrive as whole lines.  The
// mutex here is for this process only, and is not what makes that true.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// transcript is an open log file.  A nil one is a shell that is not
// logging, and every method on it does nothing, so no caller has to ask
// whether logging is on.
type transcript struct {
	mu   sync.Mutex
	f    *os.File
	path string
}

// logDir is where transcripts go when nobody has said otherwise:
// $XDG_DATA_HOME/slgo, or ~/.local/share/slgo, which is the same place
// by the same rule the configuration file follows one directory over.
func logDir() (string, error) {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "slgo"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "slgo"), nil
}

// unsafeInName is everything a filename will not be made of here.  An
// avatar's name is two words a stranger chose, and it becomes a path.
var unsafeInName = regexp.MustCompile(`[^a-z0-9._-]+`)

// logName is what to call the file for an avatar.
//
// The avatar's name, downcased, with a hyphen where the space was:
// "Example Resident" is example-resident.log.  The profile is the
// fallback for a session with no name yet, and "slsh" the fallback for
// that -- a transcript in one odd file being better than none.
//
// Nothing here trusts the name to be a filename.  It arrives from the
// grid, and a name with a slash in it would otherwise write the
// transcript into a directory of somebody else's choosing.
func logName(avatar, profile string) string {
	for _, s := range []string{avatar, profile, "slsh"} {
		s = unsafeInName.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-")
		if s = strings.Trim(s, "-."); s != "" {
			return s + ".log"
		}
	}
	return "slsh.log"
}

// openTranscript opens the log for an avatar, making the directory if it
// is not there.  dir empty means the default place.
func openTranscript(dir, avatar, profile string) (*transcript, error) {
	if dir == "" {
		d, err := logDir()
		if err != nil {
			return nil, err
		}
		dir = d
	}
	// 0700 and 0600: this is a record of what somebody said in private
	// and who said it to them, and the machine may have other people on
	// it.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, logName(avatar, profile))
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &transcript{f: f, path: path}, nil
}

// alreadyStamped matches the time a line has already been given.  The
// lines this logs are the lines a person saw, and most of those were
// printed with stamp() on the front of them; the file wants a date as
// well, so the time is taken off and put back as part of a whole one
// rather than printed twice.
var alreadyStamped = regexp.MustCompile(`^\d\d:\d\d:\d\d `)

// line writes one line of the transcript.
func (t *transcript) line(s string) {
	if t == nil {
		return
	}
	s = alreadyStamped.ReplaceAllString(s, "")
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return
	}
	when := time.Now().Format("2006-01-02 15:04:05")
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.f == nil {
		return // closed under us; a line lost at shutdown is not worth a panic
	}
	// One Write, so that two shells on one avatar cannot interleave.
	fmt.Fprintf(t.f, "%s %s\n", when, s)
}

// Path is where the transcript is being written, for anything that has
// to say so.
func (t *transcript) Path() string {
	if t == nil {
		return ""
	}
	return t.path
}

// Close closes the file.  Safe on a nil transcript and safe twice.
func (t *transcript) Close() error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.f == nil {
		return nil
	}
	err := t.f.Close()
	t.f = nil
	return err
}
