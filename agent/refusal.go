package agent

import (
	"github.com/quark-idlemind/slgo/llsd"
	"github.com/quark-idlemind/slgo/msg"
)

// What the grid says when it will not teleport this avatar.
//
// A TeleportFailed has two voices, and they were measured on Agni to
// be different things:
//
//	{"AlertInfo":[{"ExtraParams":"","Message":"no_host"}],
//	 "Info":[{"AgentID":"...","Reason":"no_host"}]}
//	{"AlertInfo":[{"ExtraParams":"","Message":"MustHaveVIPStatus"}],
//	 "Info":[{"AgentID":"...","Reason":"You must be a premium or vip
//	  subscriber to enter this region."}]}
//
// AlertInfo.Message is a KEY -- the grid's name for the refusal, spelt
// for a program rather than for a person -- and Info.Reason is
// sometimes that same key and sometimes a sentence for a person.  So
// the key is the one a program can act on, and the sentence is the one
// a person can read, and neither is the other.
//
// It is read here, in the package below both the client library and
// the daemon, because both need it: sl to tell a caller what a refused
// teleport meant, and the daemon's homing loop to tell "you are home
// already" from "that region is down".  Each used to search the
// rendered words for the key, and each kept its own copy of the string
// to search for; a string the grid owns was written out twice, in two
// packages, with a comment in one pointing at the other.  Here it is
// written once, as a type, and read off the field that carries it.

// RefusalKey is the grid's name for why it refused a teleport: the
// Message of a TeleportFailed's AlertInfo block.
//
// It is a type of its own rather than a string so that comparing one
// against the keys below says what is being compared, and so that the
// sentence beside it -- which is a string, and which sometimes happens
// to contain the key's own spelling -- cannot be compared by mistake.
//
// The keys named here are the ones this tree acts on or has measured.
// Anything else the grid sends arrives as it came and compares equal to
// none of them, which is the right answer for a key nobody here has met.
type RefusalKey string

const (
	// KeyCouldntTPCloser is the grid refusing to shorten a teleport
	// that would arrive where it started, with the sentence "Could not
	// teleport closer to destination".  Measured on Agni going to a
	// landmark the avatar was standing on (2026-08-19) and going home
	// while standing on the home point (2026-09-01), which is why both
	// sl.GoHome and the daemon's homing loop read it as "already there".
	KeyCouldntTPCloser RefusalKey = "CouldntTPCloser"

	// KeyNoHost is a region handle that is no region at all.  Measured
	// on Agni, and it is the one refusal where both voices say the same
	// word: the key is "no_host" and so is the reason.
	KeyNoHost RefusalKey = "no_host"

	// KeyRegionTPAccessBlocked is a region whose maturity rating is
	// above what this avatar may be shown.  Measured on Agni on
	// 2026-09-02, and the sentence beside it names two causes -- the
	// preference and the account's ceiling -- without saying which;
	// see sl/maturity.go for how they are told apart.
	KeyRegionTPAccessBlocked RefusalKey = "RegionTPAccessBlocked"

	// KeyMustHaveVIPStatus is a region that admits only premium
	// accounts, measured on Agni with the sentence "You must be a
	// premium or vip subscriber to enter this region."
	KeyMustHaveVIPStatus RefusalKey = "MustHaveVIPStatus"
)

// Refusal is both voices of one TeleportFailed.
//
// Either may be empty.  A TeleportFailed with no AlertInfo block has no
// key, and the reason is then all there is; nothing here promotes the
// reason into the key when that happens, because the reason is
// sometimes a sentence and a sentence is not a key.
type Refusal struct {
	Key    RefusalKey
	Reason string
}

// ReadTeleportFailed reads a TeleportFailed that came on the circuit,
// which the template says is where it comes from and which a grid other
// than Second Life may still use.
func ReadTeleportFailed(m *msg.TeleportFailed) Refusal {
	r := Refusal{Reason: trimNul(m.Info.Reason)}
	if len(m.AlertInfo) > 0 {
		r.Key = RefusalKey(trimNul(m.AlertInfo[0].Message))
	}
	return r
}

// ReadTeleportFailedEvent reads a TeleportFailed that came on the event
// queue, which is where Second Life actually sends it -- measured, and
// not what the template says.  body is the event's decoded LLSD.
//
// The blocks are read the way eventBlock reads a TeleportFinish's, as
// an array or a bare map, for the reason given there.
func ReadTeleportFailedEvent(body any) Refusal {
	var r Refusal
	if alert := eventBlock(body, "AlertInfo"); alert != nil {
		r.Key = RefusalKey(llsd.String(alert, "Message"))
	}
	if info := eventBlock(body, "Info"); info != nil {
		r.Reason = llsd.String(info, "Reason")
	}
	return r
}
