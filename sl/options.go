package sl

import "time"

// Options are what a Session can be told after it is made.  A zero
// field is its default, so the zero Options is every default.
type Options struct {
	// MoveTimeout, PermissionsTimeout and DeleteTimeout bound the
	// reading back of a change that nothing answers, before it is
	// reported asked for and not confirmed, wrapping ErrTimeout:
	// MoveItem and MoveFolder list the destination until it holds what
	// was moved, SetObjectPermissions reads the object's masks until
	// they are what was sent, and Delete waits for the region to say
	// the object has gone.  A test that proves one of them runs out
	// sets it short rather than waiting it out.
	// Why: doc/readbacks.md
	MoveTimeout        time.Duration
	PermissionsTimeout time.Duration
	DeleteTimeout      time.Duration

	// MoneyTimeout bounds each wait for the grid to answer about L$:
	// Balance, a payment's answer, and the balance read when that
	// answer does not come.
	// Why: doc/money.md#how-long-to-wait
	MoneyTimeout time.Duration

	// GroupChatTimeout bounds the wait for the grid to say a group's
	// chat, or a conference, has started.
	// Why: doc/group-chat.md#joining-a-groups-chat-when-the-user-asks
	GroupChatTimeout time.Duration

	// RegionInfoTimeout bounds the wait for a region to answer
	// RequestRegionInfo.
	// Why: doc/simstats.md#what-a-region-says-about-itself-regioninfo
	RegionInfoTimeout time.Duration

	// ParcelInfoTimeout bounds the wait for a ParcelInfoReply.
	// Why: doc/history/parcel.md#a-parcel-anywhere-from-a-landmark
	ParcelInfoTimeout time.Duration

	// ObjectTimeout bounds the wait for the region to name an object
	// the session has not asked about before, in Faces, SetFace and
	// FacePicture, which read the object through ObjectByID.
	// Why: doc/objects.md#naming-an-object
	ObjectTimeout time.Duration

	// TakeOffTimeout bounds TakeOff's wait for the simulator to remove
	// the attachment, after which the item can be deleted at once.
	// Why: doc/slsh.md#deleting-straight-after-a-take-off
	TakeOffTimeout time.Duration

	// HUDChangeTimeout bounds DragOnScreen's wait, when it is asked to
	// settle, for the region to say the HUD prim pressed has changed.
	// Why: doc/hud-screen.md#a-drag
	HUDChangeTimeout time.Duration

	// NamesAskedAgainEvery is how often at most a lookup by name that
	// finds nothing (ObjectsNamed) asks again for the name of every
	// object in range, per session.  A full re-ask of about 1,000
	// objects measured about 300 messages and 8 s; 30 s keeps a lookup
	// that keeps missing to no more than about a quarter of its time
	// re-asking.
	// Why: doc/objects.md#a-name-a-script-changed
	NamesAskedAgainEvery time.Duration
}

// The defaults for Options.  None of them is a measurement; each is a
// margin over what was seen.
//
// No value below is promised: each may change in any release, so refer to
// it by name.
const (
	DefaultMoveTimeout        = 15 * time.Second
	DefaultPermissionsTimeout = 15 * time.Second
	DefaultDeleteTimeout      = 10 * time.Second
	DefaultMoneyTimeout       = 15 * time.Second

	// DefaultGroupChatTimeout is the viewer's own, SESSION_INITIALIZATION_TIMEOUT.
	DefaultGroupChatTimeout = 30 * time.Second

	// DefaultRegionInfoTimeout is a wide margin over the 115 ms one
	// reply took.
	DefaultRegionInfoTimeout = 15 * time.Second

	// DefaultObjectTimeout is a wide margin over the 4.5 s the slowest
	// first look at an object took.
	DefaultObjectTimeout = 30 * time.Second

	// DefaultTakeOffTimeout is a wide margin over the 78-226 ms a
	// detached attachment took to be removed.
	DefaultTakeOffTimeout = 10 * time.Second

	// DefaultHUDChangeTimeout is a wide margin over the 197 ms the
	// slowest of 20 presses took to be reported changed.
	DefaultHUDChangeTimeout = 5 * time.Second

	// DefaultNamesAskedAgainEvery is about four times the 8 s a full
	// re-ask of a busy region took.
	DefaultNamesAskedAgainEvery = 30 * time.Second
)

// SetOptions replaces the session's Options.  A call already waiting
// keeps the bound it began with.
func (w *Session) SetOptions(o Options) { w.opts.Store(&o) }

// Options is what SetOptions last set, as it was set: a field left zero
// is still zero here, and means the default.
func (w *Session) Options() Options {
	if o := w.opts.Load(); o != nil {
		return *o
	}
	return Options{}
}

// moveWait, permissionsWait, deleteWait, moneyWait, regionInfoWait,
// parcelInfoWait, objectWait, takeOffWait, hudChangeWait and
// namesAskedAgainEvery are the
// bounds in force.
func (w *Session) moveWait() time.Duration {
	return orDefault(w.Options().MoveTimeout, DefaultMoveTimeout)
}

func (w *Session) permissionsWait() time.Duration {
	return orDefault(w.Options().PermissionsTimeout, DefaultPermissionsTimeout)
}

func (w *Session) deleteWait() time.Duration {
	return orDefault(w.Options().DeleteTimeout, DefaultDeleteTimeout)
}

func (w *Session) moneyWait() time.Duration {
	return orDefault(w.Options().MoneyTimeout, DefaultMoneyTimeout)
}

func (w *Session) regionInfoWait() time.Duration {
	return orDefault(w.Options().RegionInfoTimeout, DefaultRegionInfoTimeout)
}

func (w *Session) parcelInfoWait() time.Duration {
	return orDefault(w.Options().ParcelInfoTimeout, DefaultParcelTimeout)
}

func (w *Session) objectWait() time.Duration {
	return orDefault(w.Options().ObjectTimeout, DefaultObjectTimeout)
}

func (w *Session) takeOffWait() time.Duration {
	return orDefault(w.Options().TakeOffTimeout, DefaultTakeOffTimeout)
}

func (w *Session) hudChangeWait() time.Duration {
	return orDefault(w.Options().HUDChangeTimeout, DefaultHUDChangeTimeout)
}

func (w *Session) namesAskedAgainEvery() time.Duration {
	return orDefault(w.Options().NamesAskedAgainEvery, DefaultNamesAskedAgainEvery)
}

func orDefault(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}
