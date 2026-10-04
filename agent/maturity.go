package agent

// The rating of land this avatar is shown, and the highest it may ask
// to be.
//
// Both arrive in the login response and in no message afterwards.
// agent_access_max is the CEILING: the highest rating the account is
// permitted, which comes from age verification and which nothing a
// client sends can move.  agent_region_access is the PREFERENCE: the
// highest it has asked to be shown, always at or below the ceiling.
// That is how a viewer reads them -- llstartup.cpp takes the first as
// "their actual ability to access content" and the second as "the value
// of their preference setting for that content".
//
// agent_access is neither, although its name makes it the obvious one
// to read.  It is the older field, from before there was an adult
// rating: a viewer of that time read it as mature if it began with M
// and general otherwise, and read nothing else, and today's viewer
// reads the two above and not it.  The login response kept in testdata
// shows why it cannot stand in for either: it carries M there, beside
// an A in both of the others.
//
// # What a session holds after login
//
// The preference can change while a session is up, and nothing tells a
// client that it has.  No message in the template that a simulator
// sends a client carries it -- ChildAgentUpdate has access levels in
// it, but that one passes between simulators -- and Linden Lab's viewer
// learns it from the login response and from the answer to its own
// request to change it, and from nowhere else.  That is read from the
// template and the viewer's source rather than watched.  So what this
// holds is the login's, until a request through DoCap is answered with
// a grant, and from then on the grant.
//
// A change made anywhere else is not seen: on the account's web page,
// in a viewer logged in elsewhere later, or by a viewer attached to
// this very session through slgod -- which is handed the simulator's
// own capabilities and talks to them directly, so its request never
// passes through here (see internal/viewer/caps.go).  What Maturity says is
// therefore the last thing this session was told, which is true of
// everything a simulator volunteers once, but here the gap has a name.

import (
	"bytes"

	"github.com/quark-idlemind/slgo/llsd"
)

// MaturityCap is the capability that sets the preference.  DoCap reads
// its answers on the way past, because the answer is the preference now
// in force and there is no other way to hear it.
const MaturityCap = "UpdateAgentInformation"

// Maturity is the rating this avatar is shown and the highest it may
// ask for, in the letters the grid uses: PG, M or A.
//
// Either is empty when nothing said: a login response without the
// field, which a grid other than Linden Lab's may send, or an agent that
// nothing logged in.  An empty answer is "not told" and never "general".
func (a *Agent) Maturity() (preference, ceiling string) {
	if a.Account != nil {
		preference = llsd.String(a.Account.Raw, "agent_region_access")
		ceiling = llsd.String(a.Account.Raw, "agent_access_max")
	}
	a.mu.RLock()
	if a.maturity != "" {
		preference = a.maturity
	}
	a.mu.RUnlock()
	return preference, ceiling
}

// noteMaturity keeps what an answer from MaturityCap granted.
//
// Only a grant it can read is kept.  A refusal, or an answer in a shape
// nobody has seen, says nothing about what the preference is now, and
// keeping a guess here would have Presence hand it out as fact.
// sl.SetMaturity reads the same answer for the caller that asked, and
// is the one that says what was wrong with it.
func (a *Agent) noteMaturity(status int, body []byte) {
	if status < 200 || status >= 300 {
		return
	}
	v, err := llsd.Decode(bytes.NewReader(body))
	if err != nil {
		return
	}
	got := llsd.String(llsd.Map(llsd.Map(v)["access_prefs"]), "max")
	if got == "" {
		return
	}
	a.mu.Lock()
	a.maturity = got
	a.mu.Unlock()
}
