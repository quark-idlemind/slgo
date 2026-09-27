package sl

// What a profile says about somebody.
//
// It is asked for over the circuit, with the AvatarPropertiesRequest the
// viewer still sends for APT_PROPERTIES_LEGACY.  The viewer's own road is
// an AgentProfile capability, which is not among those this session asks
// the seed for (agent.DefaultCaps); the legacy answer is truncated -- the
// about text is capped at 512 bytes -- and needs nothing new.
//
// One request is answered with three messages, the groups first, twenty
// milliseconds ahead of the properties, so this listens before it asks.
// An avatar with no groups to show sends one row of zeros, which is
// dropped.  A key the grid has never heard of gets that empty groups row
// and no properties ever: that silence is how the grid says so, and
// Profile.Known is where this says it.  The avatar need not be anywhere
// near; it is a question to the grid and not to the region.
//
// The groups are the ones the avatar chose to list in the profile, which
// may be fewer than the ones it is in: the filtering is done at the far
// end.  ImageID, FLImageID and FLAboutText are not read.
// Why: doc/profiles.md

import (
	"context"
	"fmt"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// The flags AvatarPropertiesReply carries
// (llavatarpropertiesprocessor.h:37-41).  Measured values on Agni were
// 0x11 and 0x1d, so this is a small bitfield and not a count of
// anything.
const (
	ProfileAllowPublish  = 1 << 0 // the profile may be shown outside the grid
	ProfileMaturePublish = 1 << 1 // and is marked mature
	ProfileIdentified    = 1 << 2 // payment information is on file
	ProfileTransacted    = 1 << 3 // and has been used
	ProfileOnline        = 1 << 4 // online, "if known" -- see Profile.Flags
)

// captionLinden is the caption index the viewer treats as an employee,
// whose payment information is not revealed
// (llavatarpropertiesprocessor.cpp:269-270).
const captionLinden = 3

// Payment is what a profile says about payment information.
//
// Four states rather than a bool, because the two flags make three
// answers between them and the absence of an answer is the fourth: an
// account whose profile carries a caption -- a Linden, or one of the
// special accounts -- has its payment information withheld, and the
// flags for it say nothing at all rather than no
// (llavatarpropertiesprocessor.cpp:262-292).  Reading that as "none on
// file" would be this package inventing a fact about somebody's money.
type Payment int

const (
	PaymentNotRevealed Payment = iota
	PaymentNone
	PaymentOnFile
	PaymentUsed
)

func (p Payment) String() string {
	switch p {
	case PaymentNone:
		return "none on file"
	case PaymentOnFile:
		return "on file"
	case PaymentUsed:
		return "on file, and used"
	}
	return "not revealed"
}

// ProfileGroup is one group an avatar lists in their profile.
type ProfileGroup struct {
	ID    msg.UUID
	Name  string
	Title string // the title worn in that group, which may be empty

	// Powers is the role's power mask.  Kept as the number: naming the
	// bits is the group manager's business and nothing here has one.
	Powers uint64

	// Notices says the avatar takes this group's notices.  It is the
	// one thing in the row that is about them rather than about the
	// group.
	Notices bool
}

// Profile is what a profile floater shows about an avatar.
type Profile struct {
	ID msg.UUID

	// Known says the properties reply arrived.  A profile that is not
	// Known is not an empty profile: it is the grid declining to say
	// anything about that key, which is how it says it has never heard
	// of one.  See the head of this file.
	Known bool

	// BornOn is the date as the simulator writes it, "5/21/2010", and
	// is passed on unparsed: it is somebody's month and day in an order
	// nothing states, and a date this package guessed wrong at would
	// look exactly as convincing as one it got right.
	BornOn string

	// Partner is who they are partnered to, or zero for nobody.  It is
	// a key and not a name; the caller resolves it, since this package
	// has a session that can ask and the profile itself does not carry
	// one.
	Partner msg.UUID

	About string
	URL   string

	// Caption is the words shown in place of "Resident", and
	// CaptionIndex an index into a list of them the viewer keeps.  The
	// field is one byte for the index or a string for the words, which
	// is what the template means by "special - usually U8"
	// (message_template.msg:3928) and what the viewer sorts out by its
	// length (llavatarpropertiesprocessor.cpp:451-459).
	Caption      string
	CaptionIndex uint8

	// Flags is the bitfield above, kept whole.  Only payment is read out
	// of it here: the online bit is not passed on, because the viewer
	// itself only shows online status for a friend who has granted the
	// right to see it (llpanelprofile.cpp:1146-1153), and OnlineFriends
	// already answers "who is online" from the friends list, which is
	// the answer that is actually true.
	Flags uint32

	// Groups are the groups listed in the profile, which is not every
	// group they belong to.  See the head of this file.
	Groups []ProfileGroup

	// WantTo, Skills and Languages are the interests, which arrive in a
	// message of their own and are usually empty.  The masks beside them
	// are not kept: they are bitfields whose labels live in the viewer's
	// XUI rather than in the protocol, so nothing here could turn one
	// into words.
	WantTo    string
	Skills    string
	Languages string
}

// Payment says what the flags say about payment information.
func (p *Profile) Payment() Payment {
	// The caption first, because a captioned account's flags are not
	// filled in rather than being filled in with no.
	if p.Caption != "" || p.CaptionIndex == captionLinden {
		return PaymentNotRevealed
	}
	switch {
	case p.Flags&ProfileTransacted != 0:
		return PaymentUsed
	case p.Flags&ProfileIdentified != 0:
		return PaymentOnFile
	}
	return PaymentNone
}

// profileGrace bounds the wait for the rest after the properties have
// arrived.
//
// The three replies came 22 milliseconds apart on Agni and the
// properties were the second of them, so by the time they are in hand
// the groups are already in and the interests are a millisecond behind.
// This is the cap for a reply that is not coming rather than the pause a
// profile normally costs, and reaching it costs nothing but the parts
// that never arrived.
const profileGrace = 2 * time.Second

// Profile asks the grid what a profile says about somebody, and waits
// for all three of the answers.
//
// One call rather than three, because one request provokes all three
// replies and there is no way to ask for them separately: a caller that
// wanted only the groups would still have to send this and still have to
// be listening before it did.
//
// The timeout is how long to wait.  Reaching it with the groups reply in
// hand and no properties is not a failure and is not reported as one --
// it is the grid saying it has never heard of that key, which is the
// only way it ever says so.  Reaching it having heard nothing at all is
// a real timeout: the question went unanswered.
func (w *Session) Profile(ctx context.Context, who msg.UUID, timeout time.Duration) (*Profile, error) {
	if who.IsZero() {
		return nil, fmt.Errorf("sl: Profile needs somebody to ask about")
	}
	if timeout == 0 {
		timeout = 15 * time.Second
	}

	p := &Profile{ID: who}
	var gotProps, gotGroups, gotInterests bool
	done := make(chan struct{}, 1)
	wake := func() {
		select {
		case done <- struct{}{}:
		default:
		}
	}

	// Listening before asking, because the groups arrive first and can
	// arrive before Send has returned.  See the head of this file.
	stop := w.onAvatarReply(func(r *avatarReply) {
		if r.Avatar != who {
			return
		}
		switch {
		case r.Props != nil:
			readProperties(p, &r.Props.PropertiesData)
			gotProps = true
		case r.Groups != nil:
			readGroups(p, r.Groups.GroupData)
			gotGroups = true
		case r.Interests != nil:
			readInterests(p, &r.Interests.PropertiesData)
			gotInterests = true
		}
		wake()
	})
	defer stop()

	m := &msg.AvatarPropertiesRequest{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.AgentData.AvatarID = who
	if err := w.Send(ctx, m); err != nil {
		return nil, err
	}

	// The wait is the whole timeout until the properties arrive and the
	// grace afterwards, since whatever is still out then is a
	// millisecond behind them or is not coming at all.
	deadline := time.Now().Add(timeout)
	for {
		w.mu.Lock()
		props, groups, interests := gotProps, gotGroups, gotInterests
		w.mu.Unlock()
		if props && groups && interests {
			p.Known = true
			return p, nil
		}

		wait := time.Until(deadline)
		if props {
			wait = profileGrace
		}
		t := time.NewTimer(wait)
		select {
		case <-done:
			t.Stop()
		case <-t.C:
			switch {
			case props:
				// Part of it arrived and the rest did not, which is a
				// profile with a hole in it rather than no profile.
				p.Known = true
				return p, nil
			case groups:
				// The grid answered, and what it answered was that it
				// has nothing to say about this key.
				return p, nil
			}
			return nil, fmt.Errorf("%w: what %s's profile says", ErrTimeout, who)
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		}
	}
}

// readProperties fills in the part of a profile that
// AvatarPropertiesReply carries.
func readProperties(p *Profile, d *msg.AvatarPropertiesReply_PropertiesData) {
	p.BornOn = trimNul(d.BornOn)
	p.Partner = d.PartnerID
	p.About = trimNul(d.AboutText)
	p.URL = trimNul(d.ProfileURL)
	p.Flags = d.Flags

	// One byte is an index into the viewer's list of captions and
	// anything longer is the caption itself, which is the viewer's own
	// reading of the field (llavatarpropertiesprocessor.cpp:451-459).
	// The ordinary avatar sends one byte holding zero, which is an index
	// into that list meaning nothing in particular; no bytes at all is
	// neither of the two and is left as nothing.
	switch c := d.CharterMember; {
	case len(c) == 1:
		p.CaptionIndex = c[0]
	case len(c) > 1:
		p.Caption = trimNul(c)
	}
}

// readGroups fills in the groups, dropping the empty row an avatar with
// none to show sends.  See the head of this file.
//
// The list is replaced rather than added to.  A second reply about the
// same avatar is the same question asked again -- by another client on
// this avatar's circuit, whose traffic slgod relays here as well -- and
// the newer answer is the answer, not more of the old one.
func readGroups(p *Profile, rows []msg.AvatarGroupsReply_GroupData) {
	p.Groups = nil
	for i := range rows {
		g := &rows[i]
		if g.GroupID.IsZero() {
			continue
		}
		p.Groups = append(p.Groups, ProfileGroup{
			ID:      g.GroupID,
			Name:    trimNul(g.GroupName),
			Title:   trimNul(g.GroupTitle),
			Powers:  g.GroupPowers,
			Notices: g.AcceptNotices,
		})
	}
}

// readInterests fills in the interests, of which only the free text is
// kept.  See Profile.
func readInterests(p *Profile, d *msg.AvatarInterestsReply_PropertiesData) {
	p.WantTo = trimNul(d.WantToText)
	p.Skills = trimNul(d.SkillsText)
	p.Languages = trimNul(d.LanguagesText)
}

// avatarReply is one of the three messages an AvatarPropertiesRequest is
// answered with: exactly one of the three pointers is set.
//
// One subscription for the three rather than three, because they are one
// answer in three envelopes -- nothing asks for one of them alone -- and
// because a caller has to sift them by the avatar they are about, which
// is a field all three share and none of them shares with the request.
type avatarReply struct {
	Avatar    msg.UUID
	Props     *msg.AvatarPropertiesReply
	Interests *msg.AvatarInterestsReply
	Groups    *msg.AvatarGroupsReply
}

// onAvatarReply is the internal subscription Profile waits on.
//
// Delivered rather than stored, for the reason scriptRunning gives: a
// profile is what somebody had written about themselves at the moment
// they were asked, and a remembered one would be a claim about the past
// dressed as one about now.
func (w *Session) onAvatarReply(fn func(*avatarReply)) (stop func()) {
	w.mu.Lock()
	w.profileFns = append(w.profileFns, fn)
	i := len(w.profileFns) - 1
	w.mu.Unlock()
	return func() {
		w.mu.Lock()
		if i < len(w.profileFns) {
			w.profileFns[i] = nil
		}
		w.mu.Unlock()
	}
}

// avatarReplyTo hands one of the three replies to whoever asked for it.
//
// The subscriptions are called with the lock HELD, which is not what
// scriptRunning does and is deliberate: a profile is assembled from
// three messages that arrive on the reader goroutine while the caller
// waits on another, so the lock is what makes the assembling safe to
// read.  Nothing registered here does anything but fill in a struct.
func (w *Session) avatarReplyTo(r *avatarReply) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, fn := range w.profileFns {
		if fn != nil {
			fn(r)
		}
	}
}
