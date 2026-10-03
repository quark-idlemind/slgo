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
}

// The defaults for Options.  None of them is a measurement; each is a
// margin over what was seen.
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

// moveWait, permissionsWait, deleteWait, moneyWait and regionInfoWait are the bounds in
// force.
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

func orDefault(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}
