package main

// Where each avatar was last sitting, kept across logins.
//
// The daemon restores a seat at login and writes one down when it
// changes; see internal/server/seat.go for why that is worth doing and how the
// watching works.  This is only the half that decides where it is
// kept, which is this command's business and not the server's -- the
// same division the profile list is under.
//
// # Why not in the profile
//
// A profile under ~/.config/slgo is written by a person and read by the
// daemon.  This is written by the daemon, several times a day, without
// anybody asking.  Putting it in the same file would mean rewriting an
// operator's own configuration underneath them -- reordering it,
// dropping their comments -- to record something no operator typed.
//
// So it goes in slgod's own directory, beside the machine identity,
// which is already the place for things the daemon keeps rather than
// things it is told.
//
// # The format
//
// One line per avatar: the profile name, a space, the id of the
// object it was sitting on, and optionally a space and the id of the
// region the avatar was in.  A line without a region is one written
// before regions were kept, and reads as a region not known.  Blank lines and # comments are skipped, so
// somebody can leave a note in it or comment a line out to stop an
// avatar being put back in its chair.
//
// Written whole, to a temporary file, and renamed over the old one, so
// that a daemon stopped mid-write leaves the previous version rather
// than half of the new one.

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/quark-idlemind/slgo/msg"
)

// seatStore is server.Seats backed by a file.
type seatStore struct {
	path string

	mu sync.Mutex
	on map[string]seatEntry

	// complained is set once a write has failed, so that a directory
	// that cannot be written says so once rather than every time an
	// avatar sits down.
	complained bool

	// log is where a write that failed is reported.  Nil is silence,
	// which is what a test wants.
	log func(format string, v ...any)
}

// seatEntry is a seat and the region the avatar was in; the region is
// zero when not known.
type seatEntry struct{ on, region msg.UUID }

// seatsPath is where the file lives.
func seatsPath() (string, error) {
	dir, err := machineConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "seats"), nil
}

// openSeats reads the file, or starts an empty one.
//
// A file that is not there is not an error: nothing has sat down yet.
// A file that cannot be READ is, because carrying on would quietly
// forget every seat in it and then overwrite it with the forgetting.
func openSeats(path string, log func(string, ...any)) (*seatStore, error) {
	s := &seatStore{path: path, on: map[string]seatEntry{}, log: log}

	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for line := 1; sc.Scan(); line++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		f := strings.Fields(text)
		if len(f) < 2 || len(f) > 3 {
			return nil, fmt.Errorf("%s:%d: want a profile name, an id and optionally a region id", path, line)
		}
		var e seatEntry
		var err error
		if e.on, err = msg.ParseUUID(f[1]); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, line, err)
		}
		if len(f) == 3 {
			if e.region, err = msg.ParseUUID(f[2]); err != nil {
				return nil, fmt.Errorf("%s:%d: %w", path, line, err)
			}
		}
		s.on[f[0]] = e
	}
	return s, sc.Err()
}

// Seat is what this profile was last sitting on, and the region it was in.
func (s *seatStore) Seat(profile string) (on, region msg.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.on[profile]
	return e.on, e.region
}

// SetSeat writes it down.  The zero id forgets.
//
// A failure to write is reported and not returned: the caller is a
// watch loop with nothing useful to do about it, and an avatar that
// goes on sitting where it is sitting is not made worse by the note
// about it being lost.
func (s *seatStore) SetSeat(profile string, on, region msg.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if on.IsZero() {
		delete(s.on, profile)
	} else {
		s.on[profile] = seatEntry{on, region}
	}
	if err := s.writeLocked(); err != nil && !s.complained {
		s.complained = true
		if s.log != nil {
			s.log("cannot write %s, so seats will not survive a restart: %v", s.path, err)
		}
	}
}

func (s *seatStore) writeLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	names := make([]string, 0, len(s.on))
	for name := range s.on {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString("# What each avatar was last sitting on, written by slgod.\n")
	b.WriteString("# One profile, one object id and, if known, one region id per line.\n")
	for _, name := range names {
		e := s.on[name]
		if e.region.IsZero() {
			fmt.Fprintf(&b, "%s %s\n", name, e.on)
		} else {
			fmt.Fprintf(&b, "%s %s %s\n", name, e.on, e.region)
		}
	}

	tmp, err := os.CreateTemp(filepath.Dir(s.path), "seats-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}
