package main

// A session is somewhere to talk: the region's open chat, or one
// person.  The prompt names the current one, tab on an empty line moves
// to the next, and everything typed that is not a command goes to it.
//
// Sessions appear on their own as well as on request -- an instant
// message from somebody new opens one, so that answering is a tab away
// rather than a command.

import (
	"sort"
	"strings"
	"sync"

	"github.com/quark-idlemind/slgo/msg"
)

// Session is one conversation.
type Session struct {
	// Target is the other person, or zero for the region's open
	// chat.
	Target msg.UUID
	Name   string
}

// Local reports whether this is open chat rather than an instant
// message session.
func (s *Session) Local() bool { return s.Target.IsZero() }

// Label is what the prompt and the message prefixes call it.
func (s *Session) Label() string {
	if s.Local() {
		return "Local"
	}
	return s.Name
}

// Sessions is the list, always with open chat first.
type Sessions struct {
	mu   sync.Mutex
	list []*Session
	cur  int
}

// NewSessions starts with open chat, which cannot be closed.
func NewSessions() *Sessions {
	return &Sessions{list: []*Session{{Name: "Local"}}}
}

// Current is the session being typed to.
func (s *Sessions) Current() *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.list[s.cur]
}

// Next moves to the following session and returns it, wrapping round.
func (s *Sessions) Next() *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cur = (s.cur + 1) % len(s.list)
	return s.list[s.cur]
}

// Open returns the session with this person, making one if there is
// none, and reports whether it had to be made.
func (s *Sessions) Open(target msg.UUID, name string) (*Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.list {
		if e.Target == target {
			// A name learned since is worth keeping.
			if name != "" && e.Name != name {
				e.Name = name
			}
			return e, false
		}
	}
	e := &Session{Target: target, Name: name}
	s.list = append(s.list, e)
	return e, true
}

// Switch makes a session current.
func (s *Sessions) Switch(e *Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, x := range s.list {
		if x == e {
			s.cur = i
			return
		}
	}
}

// Close drops a session and returns what is current afterwards.  Open
// chat is never dropped: it is the one place there is always to talk.
func (s *Sessions) Close(e *Session) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.Local() {
		return s.list[s.cur]
	}
	for i, x := range s.list {
		if x != e {
			continue
		}
		s.list = append(s.list[:i], s.list[i+1:]...)
		if s.cur >= len(s.list) {
			s.cur = len(s.list) - 1
		} else if s.cur > i {
			s.cur--
		}
		break
	}
	return s.list[s.cur]
}

// All returns the sessions in order, with the current one marked.
func (s *Sessions) All() ([]*Session, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Session, len(s.list))
	copy(out, s.list)
	return out, s.cur
}

// Roster is who is known by name.
//
// Names are learned three ways -- chat carries the speaker's, an
// instant message carries the sender's, and anything else has to be
// asked for with UUIDNameRequest -- so this is the one place that knows
// whether a name is worth asking for at all.
type Roster struct {
	mu      sync.Mutex
	names   map[msg.UUID]string
	pending map[msg.UUID]bool
}

func NewRoster() *Roster {
	return &Roster{names: map[msg.UUID]string{}, pending: map[msg.UUID]bool{}}
}

// Learn records a name that arrived with something else.
func (r *Roster) Learn(id msg.UUID, name string) {
	if id.IsZero() || name == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.names[id] = name
	delete(r.pending, id)
}

// Name returns what somebody is called, or the empty string.
func (r *Roster) Name(id msg.UUID) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.names[id]
}

// NameOr returns the name, or the id in a form short enough to read
// when there is none.
func (r *Roster) NameOr(id msg.UUID) string {
	if n := r.Name(id); n != "" {
		return n
	}
	s := id.String()
	if len(s) > 8 {
		s = s[:8]
	}
	return "(" + s + ")"
}

// Unknown filters a list down to the ids nobody has a name for and
// nobody has asked about, marking them as asked.
func (r *Roster) Unknown(ids []msg.UUID) []msg.UUID {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []msg.UUID
	for _, id := range ids {
		if id.IsZero() || r.names[id] != "" || r.pending[id] {
			continue
		}
		r.pending[id] = true
		out = append(out, id)
	}
	return out
}

// Find matches a name the way a person would type it: the whole thing,
// or the start of it, or the start of either half, ignoring case.  It
// returns every match, so an ambiguous one can be refused rather than
// guessed at.
func (r *Roster) Find(want string) []msg.UUID {
	want = strings.TrimSpace(strings.ToLower(want))
	if want == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	var exact, prefix []msg.UUID
	for id, name := range r.names {
		low := strings.ToLower(name)
		switch {
		case low == want:
			exact = append(exact, id)
		case strings.HasPrefix(low, want):
			prefix = append(prefix, id)
		default:
			// The last name on its own, or the first name of
			// somebody whose full name was typed with a
			// different last name.
			if first, last, ok := strings.Cut(low, " "); ok {
				if strings.HasPrefix(first, want) || strings.HasPrefix(last, want) {
					prefix = append(prefix, id)
				}
			}
		}
	}
	if len(exact) > 0 {
		return exact
	}
	sort.Slice(prefix, func(i, j int) bool {
		return r.names[prefix[i]] < r.names[prefix[j]]
	})
	return prefix
}
