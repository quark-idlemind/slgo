package sl

// What an avatar is willing to be shown, and why it is not what an
// avatar is allowed to see.
//
// Second Life rates land General, Moderate and Adult, and an avatar
// carries two different numbers about that.  They are easy to confuse
// and only one of them is a client's to change:
//
//   - The PREFERENCE: the highest rating this avatar has asked to be
//     shown.  It is an account setting the grid keeps, it is what this
//     file sets, and a viewer's maturity menu sets the same thing.
//   - The CEILING: the highest the account is PERMITTED to ask for,
//     which comes from age verification and is `agent_access_max` in
//     the login response.  Nothing a client sends moves it.  There is
//     no message for it in the template -- the only maturity field in
//     there, DataHomeLocationRequest's AgentEffectiveMaturity, is in a
//     trusted message a client cannot send -- and Linden Lab's own
//     viewer offers only what the account already allows and sends
//     people to the website for the rest.
//
// So SetMaturity asks, and the grid answers with what it granted, which
// may be lower.
//
// Both numbers also arrive in the login response, and a Presence
// carries them: MaturityPreference and MaturityCeiling, read out of
// that response where the session was built and kept current by the
// answers to SetMaturity.  That is how they are READ -- the capability
// has no way to be asked without being told, and the only rating always
// safe to tell it is General, which would lower a preference somebody
// had set higher.  See agent.Maturity for which fields they are, and
// for the one that looks right and is not.
//
// A teleport refused for maturity names both causes at once, the
// preference and the ceiling, and does not say which applies;
// SetMaturity is how to tell.  A grant lower than the request is what
// the capability is documented to do, and has not been seen here.
// Why: doc/maturity.md

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/llsd"
)

// MaturityCap is the capability that sets the preference.
//
// It is a POST of {"access_prefs": {"max": RATING}} and it answers with
// the same shape, carrying what was granted.  A session whose region
// did not offer it cannot set maturity at all; agent.DefaultCaps asks
// for it, so a session that lacks it is one whose daemon was built
// before this existed.
//
// It is the agent package's name for it and not a second spelling:
// the agent reads the same answer on its way past, which is how the
// preference a Presence reports stays the one last granted.
const MaturityCap = agent.MaturityCap

// The ratings, in the two-letter form the grid takes and answers in.
//
// They are Linden Lab's short strings and not words of ours: PG is what
// the protocol still calls General, which the interface stopped calling
// PG many years ago, and a translation layer here would mean the value
// on the wire and the value in an error message were different strings.
const (
	MaturityGeneral  = "PG"
	MaturityModerate = "M"
	MaturityAdult    = "A"
)

// ParseMaturity reads a rating from what somebody typed.
//
// Both vocabularies are taken: the words the interface uses and the
// letters the protocol does, since a person who has read a region's
// rating in "regions" has seen the words and a person reading this
// package has seen the letters.  "Mature" is taken as Moderate because
// that is what the rating used to be called and the old name is still
// what half the world says.
func ParseMaturity(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "pg", "g", "general":
		return MaturityGeneral, nil
	case "m", "mature", "moderate":
		return MaturityModerate, nil
	case "a", "adult":
		return MaturityAdult, nil
	}
	return "", fmt.Errorf("sl: %q is not a maturity rating: general, moderate or adult", s)
}

// MaturityName is a rating in the word the interface uses, for a line a
// person reads.  An unknown letter is returned as it came, since the
// set is Linden Lab's to add to.
func MaturityName(rating string) string {
	switch rating {
	case MaturityGeneral:
		return "general"
	case MaturityModerate:
		return "moderate"
	case MaturityAdult:
		return "adult"
	}
	return rating
}

// SetMaturity asks for the highest rating of land this avatar should be
// shown, and returns what the grid granted.
//
// The answer is the point of the call.  What comes back is the
// preference now in force, which is the lower of what was asked for and
// what the account is permitted -- so a caller that asked for Adult and
// was given Moderate has learnt the account's ceiling, and that ceiling
// is not something this or any other client can raise.  See the head of
// this file.
//
// Granted equal to what was asked for does not mean anything changed:
// setting a preference to what it already was is answered the same way.
func (w *Session) SetMaturity(ctx context.Context, rating string) (string, error) {
	want, err := ParseMaturity(rating)
	if err != nil {
		return "", err
	}

	body, err := llsd.Encode(map[string]any{
		"access_prefs": map[string]any{"max": want},
	})
	if err != nil {
		return "", err
	}
	answer, err := w.capDo(ctx, agent.CapRequest{
		Cap:    MaturityCap,
		Method: "POST",
		Body:   body,
		Type:   "application/llsd+xml",
	})
	if err != nil {
		return "", err
	}

	got, err := maturityGranted(answer)
	if err != nil {
		return "", err
	}
	return got, nil
}

// maturityGranted reads the rating out of what the capability answered.
//
// An answer this cannot read is an error and never the rating that was
// asked for.  Reporting the request back as though it were the reply is
// how a caller comes to believe an avatar may go somewhere it will be
// refused from, and the refusal arrives much later and somewhere else
// -- on a teleport, in the grid's own words, naming two possible causes
// and neither of them this.
func maturityGranted(answer []byte) (string, error) {
	v, err := llsd.Decode(bytes.NewReader(answer))
	if err != nil {
		return "", fmt.Errorf("sl: %s answered with something that is not LLSD: %w",
			MaturityCap, err)
	}
	m := llsd.Map(v)
	if m == nil {
		return "", fmt.Errorf("sl: %s answered with %T, wanted a map", MaturityCap, v)
	}
	prefs := llsd.Map(m["access_prefs"])
	if prefs == nil {
		return "", fmt.Errorf("sl: %s answered without an access_prefs in it: %v",
			MaturityCap, m)
	}
	got := llsd.String(prefs, "max")
	if got == "" {
		return "", fmt.Errorf("sl: %s named no rating in its answer: %v", MaturityCap, prefs)
	}
	return got, nil
}
