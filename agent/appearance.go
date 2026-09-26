package agent

import (
	"sync"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// AppearanceLimit is how many avatars are remembered.
//
// A region holds a hundred or so at the very most, and this store keeps
// one entry each however long they stay -- so the limit is not about a
// crowd but about churn: a session left running somewhere busy sees
// avatars arrive and leave all day, and each one that leaves would
// otherwise be remembered for ever.  Nothing here is told when somebody
// goes.  KillObject names a local id and this is keyed by the avatar's
// own, so the two cannot be matched without keeping a third thing.
const AppearanceLimit = 256

// Appearances is what each avatar nearby looks like, kept as the
// simulator described it.
//
// It is kept for the same reason the land is: it is said once and cannot
// be asked for again.  An avatar's appearance arrives when it comes into
// view -- shape, skin, what it is wearing, which attachments hang off
// which point -- and the simulator has no message for "tell me again".
// A viewer handed a session that has been up for hours therefore knows
// every avatar's position and nothing about how any of them look, and
// draws each one as the default body: untextured, unshaped and wearing
// nothing.  Which is exactly how it was found.
//
// The whole message is kept rather than anything decoded from it.  The
// visual parameters alone are two hundred and fifty odd bytes of sliders
// that mean nothing to this package, and the texture entry is a packed
// per-face structure a viewer will unpack itself.  Storing the
// simulator's own bytes means a viewer is told precisely what the
// simulator said.
type Appearances struct {
	mu       sync.Mutex
	byAvatar map[msg.UUID]*msg.AvatarAppearance
	heard    map[msg.UUID]time.Time
	order    []msg.UUID
	dropped  int
}

// note keeps one avatar's appearance, replacing what was known.
//
// Replacing rather than accumulating because this arrives again each
// time somebody changes clothes, and it is a complete description every
// time.  The order of arrival is kept for eviction only, so an avatar
// that changes stays where it was rather than becoming the newest thing
// here.
func (s *Appearances) note(m *msg.AvatarAppearance) {
	s.noteAt(m, time.Now())
}

func (s *Appearances) noteAt(m *msg.AvatarAppearance, at time.Time) {
	id := m.Sender.ID
	if id.IsZero() {
		return
	}
	kept := copyAppearance(m)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byAvatar == nil {
		s.byAvatar = make(map[msg.UUID]*msg.AvatarAppearance)
		s.heard = make(map[msg.UUID]time.Time)
	}
	if _, seen := s.byAvatar[id]; !seen {
		s.order = append(s.order, id)
	}
	s.byAvatar[id] = kept
	s.heard[id] = at

	for len(s.order) > AppearanceLimit {
		oldest := s.order[0]
		s.order = s.order[1:]
		delete(s.byAvatar, oldest)
		delete(s.heard, oldest)
		s.dropped++
	}
}

// copyAppearance takes the message out of the packet it arrived in.
//
// Every block but Sender is either a slice or contains one, and those
// point into the receive buffer, which is reused.  Keeping the message
// without copying it means holding an appearance that quietly becomes
// some later packet's bytes.
func copyAppearance(m *msg.AvatarAppearance) *msg.AvatarAppearance {
	c := *m
	c.ObjectData.TextureEntry = append([]byte(nil), m.ObjectData.TextureEntry...)
	c.VisualParam = append([]msg.AvatarAppearance_VisualParam(nil), m.VisualParam...)
	c.AppearanceData = append([]msg.AvatarAppearance_AppearanceData(nil), m.AppearanceData...)
	c.AppearanceHover = append([]msg.AvatarAppearance_AppearanceHover(nil), m.AppearanceHover...)
	c.AttachmentBlock = append([]msg.AvatarAppearance_AttachmentBlock(nil), m.AttachmentBlock...)
	return &c
}

// All returns every appearance held, keyed by the avatar it describes.
func (s *Appearances) All() map[msg.UUID]*msg.AvatarAppearance {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[msg.UUID]*msg.AvatarAppearance, len(s.byAvatar))
	for id, m := range s.byAvatar {
		out[id] = m
	}
	return out
}

// Get is one avatar's appearance, or nil if none was seen.
func (s *Appearances) Get(id msg.UUID) *msg.AvatarAppearance {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byAvatar[id]
}

// Heard is one avatar's appearance and when it arrived, or nil.
//
// The time matters to a reader of the attachment list: it is the
// simulator's account as of then, and it is sent again only when the
// avatar is baked.
func (s *Appearances) Heard(id msg.UUID) (*msg.AvatarAppearance, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byAvatar[id], s.heard[id]
}

// Stats is how many avatars are held and how many were forgotten for the
// limit.
func (s *Appearances) Stats() (held, dropped int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.byAvatar), s.dropped
}

// forget drops everything, for a region crossing: the avatars of the
// region just left are not the ones here, and those that are will
// describe themselves again on arrival.
func (s *Appearances) forget() {
	s.mu.Lock()
	s.byAvatar, s.heard, s.order, s.dropped = nil, nil, nil, 0
	s.mu.Unlock()
}

// Appearances is how the avatars around this one look.
func (a *Agent) Appearances() *Appearances { return &a.appearance }
