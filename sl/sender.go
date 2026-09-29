package sl

// Who something came from, as a line should print it.
//
// The name on a message is whatever sent it, and an object can be given
// any name at all, a person's included.  So a program printing who
// something came from puts an object, a group or the grid's own name
// behind a label saying so, and prints only a person's name bare: the
// kind is Sender, and Label is the one way to print a name with it.
// Why: doc/im-senders.md#labelling-a-sender

import "github.com/quark-idlemind/slgo/msg"

// Sender is the kind of thing a name on a message names.
type Sender uint8

const (
	SenderPerson Sender = iota
	SenderObject
	SenderGroup
	SenderGrid
	SenderConference
)

// SystemName is the name the grid signs its own messages with
// (SYSTEM_FROM, llinstantmessage.cpp:51).
const SystemName = "Second Life"

// Label is name as a line should print it: a person's as it is, and
// anything else with its kind in front, as in "[Object] a lamp".  A
// conference is named by whoever started it, so it is labelled as a
// group is.
func (k Sender) Label(name string) string {
	switch k {
	case SenderObject:
		return "[Object] " + name
	case SenderGroup:
		return "[Group] " + name
	case SenderGrid:
		return "[Grid] " + name
	case SenderConference:
		return "[Conference] " + name
	}
	return name
}

// Sender says what FromName names, as the viewer decides it.
//
// An object's message, alert or item (19, 31, 9) carries the object's
// name whoever owns it; a web page to open (28) is the grid's.  So is
// anything with no sender id or signed SystemName
// (llimprocessing.cpp:900, 2289) -- but an object may be called that
// too, and one is the grid's only when it came from no region and no
// position (llimprocessing.cpp:1739-1742).  Anything else sent as a
// group is the group's, but for an invitation or a notice, which carry
// the group's id with the name of whoever invited or posted
// (llimprocessing.cpp:1341-1356, 1477-1478).
func (m *IM) Sender() Sender {
	switch m.Dialog {
	case DialogFromTask:
		if m.FromName == SystemName && m.Region.IsZero() && m.Position == (msg.Vector3{}) {
			return SenderGrid
		}
		return SenderObject
	case DialogTaskInventoryOffered, DialogFromTaskAsAlert:
		return SenderObject
	case DialogGotoURL:
		return SenderGrid
	}
	switch {
	case m.From.IsZero(), m.FromName == SystemName:
		return SenderGrid
	case m.Group && m.Dialog != DialogGroupInvitation && m.Dialog != DialogGroupNotice:
		return SenderGroup
	}
	return SenderPerson
}

// Sender says what From names, from the source type: an avatar is a
// person, an object an object, and anything else the simulator's own.
func (l Line) Sender() Sender {
	switch l.SourceType {
	case SourceAgent:
		return SenderPerson
	case SourceObject:
		return SenderObject
	}
	return SenderGrid
}
