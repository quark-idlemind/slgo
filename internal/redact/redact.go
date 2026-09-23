// Package redact decides how much of a credential a log line may carry.
//
// A daemon here holds live Second Life sessions, and a session is a
// handful of values that are each as good as the password while it
// lasts: the session id and circuit code, which with the agent id are
// all UseCircuitCode needs to open a circuit as the avatar, and every
// capability URL, which is a bearer credential -- whoever has one can
// use it, and the seed is the key to all the others.  A log is read by
// more people and kept for longer than any of those should be, so by
// default none of them is written to one whole.
//
// What is written instead is enough to tell things apart and to see
// what happened: an id cut to its first eight characters, a capability
// URL cut to its host, and a code not at all.  Agent ids and avatar
// names are not credentials and are left alone; they are what makes a
// log readable.
//
// Everything that decides this is here, so that there is one switch and
// one place to read what it covers.  The switch is SetFull, for the
// debugging session that needs the real values and whose operator has
// said so on the command line; a log written with it on is a copy of
// the credentials and has to be treated as one.
package redact

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync/atomic"
)

// full is whether redaction is off.  Atomic because it is set once at
// startup by main and read from every goroutine a session has, and a
// test turns it over while nothing else is running.
var full atomic.Bool

// SetFull turns redaction off (true) or back on (false), for the whole
// process.  It is meant to be called once, from main, before anything
// is logged: a value already turned into a log line or an error is not
// revisited.
func SetFull(on bool) { full.Store(on) }

// Full reports whether redaction is off, for the rare line that has to
// be worded differently rather than just carry a shorter value.
func Full() bool { return full.Load() }

// Keep is how many leading characters of an id are kept.
//
// Eight hex digits are thirty-two bits of a hundred and twenty-eight:
// plenty to tell apart the handful of sessions one daemon holds, or a
// claim from the value it should have matched, and nowhere near enough
// to be used in place of the whole.
const Keep = 8

// Elided marks where a value was cut, so that a truncated id is never
// mistaken for a whole one by somebody pasting it somewhere.
const Elided = "..."

// Hidden is what a value too short to truncate usefully -- a circuit
// code is a 32-bit number -- is written as.
const Hidden = "(hidden)"

// ID is an identifier cut to its first Keep characters.  v is anything
// with a string form: a msg.UUID, or a string.
func ID(v any) string {
	s := fmt.Sprint(v)
	if full.Load() || len(s) <= Keep {
		return s
	}
	return s[:Keep] + Elided
}

// Secret is a value written as Hidden, or whole when redaction is off.
func Secret(v any) string {
	if full.Load() {
		return fmt.Sprint(v)
	}
	return Hidden
}

// URL is a capability URL cut to its scheme and host.
//
// The host is kept because it says which simulator was being talked
// to, which is most of what a failure to reach one needs; the path is
// the credential.  A string that does not parse as a URL with a host
// is not recognisably anything, so it is hidden whole rather than
// guessed at.
func URL(s string) string {
	if full.Load() {
		return s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return Hidden
	}
	rest := ""
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
		rest = "/" + Elided
	}
	return u.Scheme + "://" + u.Host + rest
}

// Error cuts the URL out of any *url.Error in err's chain, and returns
// err.
//
// It exists because Go's HTTP client puts the whole URL it was asked for
// into every error it returns -- `Post "https://host/cap/<the
// credential>": EOF` -- and those errors travel: into a session's
// reason for ending, into a retry line in the log, back to a client.
// Cutting it where the request was made is the one place that covers
// every road the error then takes.  The *url.Error is altered in place;
// it was made by the request that has just failed, and nothing else
// holds it.
//
// Best called on the error just as the client returned it.  An error
// wrapped around it with fmt.Errorf has already written the URL into
// its own text, which altering the *url.Error underneath cannot reach;
// that text is rewritten too, but only by finding the URL in it.
func Error(err error) error {
	if err == nil || full.Load() {
		return err
	}
	var ue *url.Error
	if !errors.As(err, &ue) {
		return err
	}
	was := ue.URL
	ue.URL = URL(was)
	if error(ue) == err || was == ue.URL {
		return err
	}
	return &rewritten{err: err, text: strings.ReplaceAll(err.Error(), was, ue.URL)}
}

// rewritten is an error whose text had a URL taken out of it after it
// was made.  It unwraps to the original, so errors.Is and errors.As see
// through it exactly as they did.
type rewritten struct {
	err  error
	text string
}

func (r *rewritten) Error() string { return r.text }
func (r *rewritten) Unwrap() error { return r.err }

// Text cuts every capability URL out of s, for text that came from
// elsewhere and may have one anywhere in it.  A URL is recognised by its
// scheme, and runs to the next space or quote.
func Text(s string) string {
	if full.Load() {
		return s
	}
	var b strings.Builder
	for {
		i := strings.Index(s, "http")
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		rest := s[i:]
		if !strings.HasPrefix(rest, "http://") && !strings.HasPrefix(rest, "https://") {
			b.WriteString(s[:i+4])
			s = s[i+4:]
			continue
		}
		end := strings.IndexAny(rest, " \t\n\"'<>")
		if end < 0 {
			end = len(rest)
		}
		b.WriteString(s[:i])
		b.WriteString(URL(rest[:end]))
		s = rest[end:]
	}
}
