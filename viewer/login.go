package viewer

import (
	"crypto/subtle"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/internal/xmlrpc"
)

// Handover is one running session, described well enough to give away.
//
// Raw is the whole response the real login server gave when this session
// started, and it is the response the viewer gets: replayed, not
// composed.  Everything the login server said comes through -- the
// inventory skeleton, the gestures, udp_blacklist, region_size_x,
// agent_access, whatever Linden Lab adds next -- because nothing here
// has to know what any of it means.
//
// Three fields are replaced, and they are the whole of the trick.  The
// viewer is told the session's real agent id, session id and circuit
// code, so as far as the simulator is concerned nothing new has logged
// in; but where the response said which simulator to talk to, it now
// says slgod.
type Handover struct {
	First string
	Last  string

	// Digest is the account password in the "$1$" + md5 form, which
	// is what a viewer sends and what a profile stores, so
	// authenticating is a comparison and never a decryption.
	Digest string

	// OneTime is a digest the daemon minted for a single login, in the
	// same form, and empty when there is none outstanding.
	//
	// A second accepted password rather than a replacement, because
	// the two answer different questions.  The profile's is a standing
	// arrangement: whoever can read the profile may attach whenever
	// they like.  This one is an invitation issued just now, to hand
	// the session to a viewer being started this second by somebody
	// who cannot read the profile and could not hash a password into a
	// match if they could -- what a profile stores is already a
	// digest.  Dropping the first for the second would break every
	// handover that works today; the login handler therefore accepts
	// either.
	OneTime string

	// UseOnce is called when OneTime was the digest that matched, and
	// is what makes "one time" true rather than a claim.  Nil is
	// allowed and means nothing is consumed.
	//
	// It fires on a match rather than on a completed handover: by then
	// the password has been shown to whoever asked, so letting it work
	// twice is exactly what it must not do, whatever happens next.
	UseOnce func()

	Raw map[string]any

	// Admit is called once the password has matched and before the
	// response is composed, and is what opens the circuit and settles
	// SimIP, SimPort and Seed.
	//
	// It is separate from finding the session because it must not
	// happen for a login that is about to be refused.  The circuit
	// used to be opened while the handover was being looked up, which
	// is before the password has been compared -- so naming an avatar
	// this daemon holds was enough to open its UDP socket, and the
	// socket outlives the attempt.  A wrong password now costs the
	// asker a refusal and nothing else.
	//
	// Nil means the three fields are already filled in, which is what
	// a test that composes a handover by hand wants.
	Admit func() error

	// SimIP and SimPort are where the viewer should send its UDP:
	// slgod, standing in for the simulator.  Set by Admit when there
	// is one.
	SimIP   string
	SimPort int

	// Seed is slgod's own seed capability.  Every other capability
	// the viewer eventually asks for is handed back as the
	// simulator's own URL; this one has to come here so that the
	// event queue has a single reader.
	Seed string
}

// Lookup finds the session a viewer is asking for, by the name it typed.
// A nil Handover means there is no such session, which is answered the
// same way as a wrong password, in the same words and after the same
// work.  Lookup itself should do nothing that costs anything: it runs
// before the password is known to be right.
type Lookup func(first, last string) *Handover

// LoginHandler answers a viewer's login_to_simulator by handing over a
// session that is already running.
//
// It is deliberately an http.Handler rather than a server: where it is
// bound is the caller's decision, and the sensible answers are loopback
// or the address of a private tunnel (doc/guide.md, "Handing a session
// to a viewer").  This hands a live grid session to whoever asks with
// the right name and password, and the password is one an attacker who
// can already read the profile has anyway -- so the protection that
// matters is that nobody else can reach the endpoint.
func LoginHandler(find Lookup, logf func(string, ...any)) http.Handler {
	if logf == nil {
		logf = log.Printf
	}
	return &loginHandler{find: find, logf: logf}
}

type loginHandler struct {
	find Lookup
	logf func(string, ...any)
}

func (h *loginHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "login is a POST", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()

	// Bounded before a byte is decoded.  This is the one handler a
	// stranger reaches with nothing, and the decoder would otherwise
	// read for as long as the sender kept sending.
	method, params, err := xmlrpc.DecodeCall(http.MaxBytesReader(w, r.Body, MaxRequestBody))
	if err != nil {
		// Malformed is a fault: nothing was asked, so there is no
		// login to refuse.
		h.logf("viewer login: unreadable request: %v", err)
		h.fault(w, 1, "could not read the request: "+err.Error())
		return
	}
	if method != "login_to_simulator" {
		h.logf("viewer login: asked for %q", method)
		h.fault(w, 1, "this is not a login server, it hands over a session")
		return
	}

	first := xmlrpc.String(params, "first")
	last := xmlrpc.String(params, "last")
	passwd := xmlrpc.String(params, "passwd")

	// Hashed before anything is looked up, so that the cost of the
	// hash is paid by every attempt and not only by one that names an
	// avatar this daemon holds.
	got := []byte(agent.HashPassword(passwd))

	hand := h.find(first, last)

	// Constant time, because this is a credential comparison even
	// though both sides are digests.
	//
	// Both candidates are compared, always, and the answers combined
	// afterwards: stopping at the first match would make the time
	// taken say which of the two was presented, and skipping the
	// second when the first matched is the same leak wearing a hat.
	//
	// And both are compared when there is no session at all.  The
	// refusal below is one sentence whoever was asked for, so the
	// only thing left that could tell an avatar this daemon holds from
	// one it does not is how long the answer took -- and an attempt
	// that skipped the comparison for an unknown name would be the
	// quicker of the two.  What stands in for a missing digest is one
	// of the same length (see noDigest), because ConstantTimeCompare
	// is constant only across inputs of equal length and returns at
	// once for any other.
	var profileDigest, oneTimeDigest string
	if hand != nil {
		profileDigest, oneTimeDigest = hand.Digest, hand.OneTime
	}
	byProfile := subtle.ConstantTimeCompare(got, digestOrNone(profileDigest))
	byOneTime := subtle.ConstantTimeCompare(got, digestOrNone(oneTimeDigest))
	if hand == nil || byProfile|byOneTime != 1 {
		// One answer for all of it: no session by that name, a
		// session with no viewer_password, and a wrong password for
		// one that could be handed over.  They used to be two
		// sentences, and the difference between them said, to anybody
		// who could reach this port and cared to type a wrong
		// password, that the avatar named was logged in through this
		// daemon right now and could be taken.  The name is not a
		// secret on the grid; that is.
		//
		// The log keeps the distinction, because the person reading
		// it is the one running the daemon, and a mistyped password
		// and a profile that was never set up want different fixes.
		if hand == nil {
			h.logf("viewer login: no session for %s %s", first, last)
		} else {
			h.logf("viewer login: wrong password for %s %s", first, last)
		}
		h.refuse(w, "key", refusedLogin)
		return
	}
	if byOneTime == 1 && hand.UseOnce != nil {
		// Spent here, before anything else can go wrong, so that a
		// handover which fails further down does not leave a live
		// credential behind that somebody watched cross a command
		// line.  The password itself is never logged, here or
		// anywhere: it is on a viewer's argv already, which is quite
		// enough exposure for one secret.
		hand.UseOnce()
	}
	if hand.Raw == nil {
		// A session whose login response was not kept cannot be
		// handed over, and saying so plainly beats handing across
		// something half composed.
		h.logf("viewer login: %s %s has no stored login response", first, last)
		h.refuse(w, "presence", "That session cannot be handed over.")
		return
	}

	// Only now is anything opened.  See Handover.Admit.
	if hand.Admit != nil {
		if err := hand.Admit(); err != nil {
			h.logf("viewer login: cannot admit %s %s: %v", first, last, err)
			h.refuse(w, "presence", "That session cannot be handed over.")
			return
		}
	}

	resp := handoverResponse(hand)
	w.Header().Set("Content-Type", "text/xml")
	if err := xmlrpc.EncodeResponse(w, resp); err != nil {
		h.logf("viewer login: writing the response: %v", err)
		return
	}
	h.logf("viewer login: handed %s %s to a viewer at %s:%d",
		first, last, hand.SimIP, hand.SimPort)
}

// refusedLogin is the whole of what a refused login is told, whatever
// the reason it was refused before the password matched.
//
// Worded for the person at the viewer, who has usually just mistyped
// something, and worded so that it is true in every case it covers:
// it does not say whether the name or the password was the part that
// failed, because saying so is the disclosure.
const refusedLogin = "That name and password do not match a session here."

// noDigest stands in for a digest that is not there, so that comparing
// against it costs what comparing against a real one does.
//
// The same length as every digest HashPassword returns, and made of a
// character that is not a hex digit, so no password can hash to it --
// HashPassword gives back either "$1$" and 32 hex digits it computed
// or a "$1$" and 32 hex digits it was handed, and this is neither.
const noDigest = "$1$--------------------------------"

// digestOrNone is d, or noDigest if d is empty.
//
// An empty digest can never match -- ConstantTimeCompare answers 0 for
// lengths that differ -- but it answers 0 at once, where a real digest
// is compared byte by byte.  The comparison has to take as long when
// there is nothing to compare with as when there is.
func digestOrNone(d string) []byte {
	if d == "" {
		return []byte(noDigest)
	}
	return []byte(d)
}

// handoverResponse is the stored login response with the three fields
// that say where the simulator is pointed at slgod instead.
//
// A copy, because the stored response belongs to the session and will be
// handed over again if the viewer is restarted.
func handoverResponse(h *Handover) map[string]any {
	out := make(map[string]any, len(h.Raw))
	for k, v := range h.Raw {
		out[k] = v
	}
	out["sim_ip"] = h.SimIP
	out["sim_port"] = int64(h.SimPort)
	out["seed_capability"] = h.Seed
	return out
}

// refuse answers the way a login server refuses, which is not a fault.
//
// A viewer reads login:false with a reason and a message and shows the
// message to the person; an XML-RPC fault gets it a generic failure with
// nothing to act on.  The difference is the whole of what someone sees
// when this goes wrong, so it is worth getting right even though both
// are "an error".
func (h *loginHandler) refuse(w http.ResponseWriter, reason, message string) {
	w.Header().Set("Content-Type", "text/xml")
	_ = xmlrpc.EncodeResponse(w, map[string]any{
		"login":   "false",
		"reason":  reason,
		"message": message,
	})
}

func (h *loginHandler) fault(w http.ResponseWriter, code int64, message string) {
	w.Header().Set("Content-Type", "text/xml")
	_ = xmlrpc.EncodeFault(w, code, message)
}

// LoginURI is the address to give a viewer, in the form its grid list
// wants.
//
// https, because that is the only thing this endpoint speaks.  A bare
// address used to become http, which was right while there was a plain
// channel and is now a URL nothing answers on: Go's TLS server replies
// to a plaintext request with 400 and a viewer reports it as an
// unreachable grid.  An address that already carries a scheme is left
// as it is, so a caller that has one keeps it.
func LoginURI(addr string) string {
	if !strings.Contains(addr, "//") {
		addr = "https://" + addr
	}
	if !strings.HasSuffix(addr, "/") {
		addr += "/"
	}
	return addr
}

// HostPort splits a listener address into the parts a login response
// names separately.
func HostPort(addr string) (string, int, error) {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return "", 0, fmt.Errorf("viewer: %q has no port", addr)
	}
	host := strings.Trim(addr[:i], "[]")
	port, err := strconv.Atoi(addr[i+1:])
	if err != nil {
		return "", 0, fmt.Errorf("viewer: %q has no port: %w", addr, err)
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return host, port, nil
}
