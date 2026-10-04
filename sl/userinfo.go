package sl

// An avatar's own account details: its email address and its
// directory visibility.
//
// They are private.  Nothing here asks unless a caller does, nothing
// logs them, and no error carries them or the body they came in.
// Why: doc/account.md

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/llsd"
	"github.com/quark-idlemind/slgo/msg"
)

// UserInfoCap is the capability the viewer asks for first.
const UserInfoCap = "UserInfo"

// DefaultUserInfoTimeout bounds the wait when the caller's context does
// not.  The one reply measured took 190 ms.
//
// The value is not promised and may change in any release: refer to it by name.
const DefaultUserInfoTimeout = 5 * time.Second

// UserInfo is what the grid keeps about the avatar's own account.
type UserInfo struct {
	Email               string
	DirectoryVisibility string

	// IMViaEmail is the retired flag for forwarding IMs to email.
	// Second Life retired it and the viewer ignores it there; it is
	// read for other grids.
	IMViaEmail bool
}

// UserInfo asks the grid for this avatar's own account details, the
// way the viewer does: the UserInfo capability when the region offers
// it, and the UDP UserInfoRequest when it does not.
// Why: doc/account.md#how-the-viewer-asks
//
// The wait ends with ctx or after DefaultUserInfoTimeout, and a timeout
// wraps ErrTimeout.
func (w *Session) UserInfo(ctx context.Context) (*UserInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, DefaultUserInfoTimeout)
	defer cancel()
	if w.b.HasCap(UserInfoCap) {
		return w.userInfoByCap(ctx)
	}
	return w.userInfoByCircuit(ctx)
}

// userInfoByCap reads the capability's LLSD as the viewer does: a map
// with success, and on success email and directory_visibility, and
// im_via_email only where it is not Second Life.  Errors say what
// failed and never what the body held, bar the server's own message.
func (w *Session) userInfoByCap(ctx context.Context) (*UserInfo, error) {
	resp, err := w.b.DoCap(ctx, agent.CapRequest{Cap: UserInfoCap, Method: "GET"})
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("sl: UserInfo: no answer from the capability: %w", ErrTimeout)
		}
		return nil, fmt.Errorf("sl: UserInfo: %w", err)
	}
	if resp.Status != 200 {
		return nil, fmt.Errorf("sl: UserInfo: the capability answered %d", resp.Status)
	}
	v, err := llsd.Decode(bytes.NewReader(resp.Body))
	if err != nil {
		return nil, fmt.Errorf("sl: UserInfo: the capability's answer is not LLSD")
	}
	m := llsd.Map(v)
	if m == nil {
		return nil, fmt.Errorf("sl: UserInfo: the capability's answer is not a map")
	}
	if !llsd.Bool(m, "success") {
		return nil, fmt.Errorf("sl: UserInfo: the grid refused: %s", llsd.String(m, "message"))
	}
	return &UserInfo{
		Email:               llsd.String(m, "email"),
		DirectoryVisibility: llsd.String(m, "directory_visibility"),
		IMViaEmail:          llsd.Bool(m, "im_via_email"),
	}, nil
}

// userInfoByCircuit sends UserInfoRequest and waits for the
// UserInfoReply carrying this agent's id.  Each call listens before
// asking, as ParcelInfo does.
func (w *Session) userInfoByCircuit(ctx context.Context) (*UserInfo, error) {
	give, err := w.borrowUserInfo()
	if err != nil {
		return nil, err
	}
	defer give()

	got := make(chan *UserInfo, 1)
	stop := w.onUserInfo(func(who msg.UUID, u *UserInfo) {
		if who != w.me {
			return
		}
		select {
		case got <- u:
		default:
		}
	})
	defer stop()

	m := &msg.UserInfoRequest{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	if err := w.Send(ctx, m); err != nil {
		return nil, err
	}

	select {
	case u := <-got:
		return u, nil
	case <-ctx.Done():
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("sl: UserInfo: no UserInfoReply: %w", ErrTimeout)
		}
		return nil, ctx.Err()
	case <-w.b.Done():
		return nil, fmt.Errorf("sl: UserInfo: the session ended")
	}
}

func userInfoFrom(m *msg.UserInfoReply) *UserInfo {
	return &UserInfo{
		Email:               trimNul(m.UserData.EMail),
		DirectoryVisibility: trimNul(m.UserData.DirectoryVisibility),
		IMViaEmail:          m.UserData.IMViaEMail,
	}
}

// onUserInfo is onParcelInfo's shape: a slot blanked on stop, a blank
// one reused before the slice grows.
func (w *Session) onUserInfo(fn func(msg.UUID, *UserInfo)) (stop func()) {
	w.mu.Lock()
	i := -1
	for j, f := range w.userInfoFns {
		if f == nil {
			i = j
			break
		}
	}
	if i < 0 {
		w.userInfoFns = append(w.userInfoFns, fn)
		i = len(w.userInfoFns) - 1
	} else {
		w.userInfoFns[i] = fn
	}
	w.mu.Unlock()
	return func() {
		w.mu.Lock()
		if i < len(w.userInfoFns) {
			w.userInfoFns[i] = nil
		}
		w.mu.Unlock()
	}
}

// userInfoReply hands one reply to whoever asked.
func (w *Session) userInfoReply(who msg.UUID, u *UserInfo) {
	w.mu.Lock()
	fns := make([]func(msg.UUID, *UserInfo), len(w.userInfoFns))
	copy(fns, w.userInfoFns)
	w.mu.Unlock()
	for _, fn := range fns {
		if fn != nil {
			fn(who, u)
		}
	}
}

// borrowUserInfo has the daemon relay UserInfoReply for as long as the
// returned func is not called, as a sit borrows AvatarAnimation: it is
// not among Subscriptions, so no session that did not ask is sent the
// account's email.  A backend that is not a Watcher already sees
// everything.
// Why: doc/account.md#nothing-logs-it
func (w *Session) borrowUserInfo() (give func(), err error) {
	b, ok := w.b.(Watcher)
	if !ok {
		return func() {}, nil
	}
	w.mu.Lock()
	w.userInfoWatch++
	first := w.userInfoWatch == 1
	w.mu.Unlock()
	release := func() {
		w.mu.Lock()
		w.userInfoWatch--
		last := w.userInfoWatch == 0
		w.mu.Unlock()
		if last {
			_ = b.Unwatch("UserInfoReply")
		}
	}
	if first {
		if err := b.Watch("UserInfoReply"); err != nil {
			release()
			return func() {}, fmt.Errorf("sl: UserInfo: cannot listen for the reply: %w", err)
		}
	}
	return release, nil
}
