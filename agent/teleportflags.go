package agent

// Why a teleport was made, as TeleportStart says it.
//
// TeleportStart carries the flags of the teleport that is beginning:
// which way it was asked for, and a few things about how.  The viewer
// reads them for the progress bar's cancel button alone
// (process_teleport_start, llviewermessage.cpp:3455); here they are kept
// to say what moved the avatar when it arrives, which a teleport it did
// not ask for otherwise never says.  The values are the viewer's
// TELEPORT_FLAGS_*, indra/llmessage/llteleportflags.h:30-48.
// Why: doc/avatar-state.md#teleportstart

import (
	"sync"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

const (
	TeleportSetHomeToTarget uint32 = 1 << 0  // TELEPORT_FLAGS_SET_HOME_TO_TARGET
	TeleportSetLastToTarget uint32 = 1 << 1  // TELEPORT_FLAGS_SET_LAST_TO_TARGET
	TeleportViaLure         uint32 = 1 << 2  // TELEPORT_FLAGS_VIA_LURE
	TeleportViaLandmark     uint32 = 1 << 3  // TELEPORT_FLAGS_VIA_LANDMARK
	TeleportViaLocation     uint32 = 1 << 4  // TELEPORT_FLAGS_VIA_LOCATION
	TeleportViaHome         uint32 = 1 << 5  // TELEPORT_FLAGS_VIA_HOME
	TeleportViaTelehub      uint32 = 1 << 6  // TELEPORT_FLAGS_VIA_TELEHUB
	TeleportViaLogin        uint32 = 1 << 7  // TELEPORT_FLAGS_VIA_LOGIN
	TeleportViaGodlikeLure  uint32 = 1 << 8  // TELEPORT_FLAGS_VIA_GODLIKE_LURE
	TeleportGodlike         uint32 = 1 << 9  // TELEPORT_FLAGS_GODLIKE
	Teleport911             uint32 = 1 << 10 // TELEPORT_FLAGS_911
	TeleportDisableCancel   uint32 = 1 << 11 // TELEPORT_FLAGS_DISABLE_CANCEL
	TeleportViaRegionID     uint32 = 1 << 12 // TELEPORT_FLAGS_VIA_REGION_ID
	TeleportIsFlying        uint32 = 1 << 13 // TELEPORT_FLAGS_IS_FLYING
	TeleportShowResetHome   uint32 = 1 << 14 // TELEPORT_FLAGS_SHOW_RESET_HOME
	TeleportForceRedirect   uint32 = 1 << 15 // TELEPORT_FLAGS_FORCE_REDIRECT
	TeleportViaGlobalCoords uint32 = 1 << 16 // TELEPORT_FLAGS_VIA_GLOBAL_COORDS
	TeleportWithinRegion    uint32 = 1 << 17 // TELEPORT_FLAGS_WITHIN_REGION
)

// teleportCauseKept is how long a TeleportStart's flags wait for the
// arrival they belong to.  A teleport takes about a third of a second
// from start to arrival when it is measured (doc/history/teleport.md), so
// a minute is long enough for a slow one and short enough that flags
// from a teleport that never arrived are not given to something else.
const teleportCauseKept = time.Minute

// teleportStarts is the latest TeleportStart: its flags, and when.
type teleportStarts struct {
	mu    sync.Mutex
	flags uint32
	at    time.Time
	set   bool
}

// note keeps a TeleportStart.  It replaces the one before it, so the
// second of the two a landmark teleport sends is the same cause said
// again and not another.
func (s *teleportStarts) note(flags uint32, at time.Time) {
	s.mu.Lock()
	s.flags, s.at, s.set = flags, at, true
	s.mu.Unlock()
}

// take hands over what has been kept and forgets it: the flags of the
// latest start if there was one and it is no older than teleportCauseKept
// at now, otherwise zero.  It forgets either way.
func (s *teleportStarts) take(now time.Time) uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	flags, fresh := s.flags, s.set && now.Sub(s.at) <= teleportCauseKept
	s.flags, s.at, s.set = 0, time.Time{}, false
	if !fresh {
		return 0
	}
	return flags
}

// keepTeleportStart registers the handler that keeps the flags.
//
// Nothing here stops the message going where it went before: an
// attached viewer's circuit absorbs TeleportStart on its own account
// (viewer/circuit.go), and a handler here is only one more reader of
// what the session was sent.
func (a *Agent) keepTeleportStart() {
	a.Disp.MustHandle("TeleportStart", func(p *msg.Packet) {
		m := p.Message.(*msg.TeleportStart)
		at := p.At
		if at.IsZero() {
			at = time.Now()
		}
		a.teleports.note(m.Info.TeleportFlags, at)
	}, msg.Inline())
}
