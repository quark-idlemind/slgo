package server

// The active group, and why the server has to keep it.
//
// A parcel usually grants "create objects" to a group, and a login
// starts with none active, so an avatar without its group cannot rez
// and the refusal blames the land.  A reconnect is a fresh login, so
// settling the group once is not enough: the resolved uuid belongs to
// the Hosted, which survives reconnection, and the supervisor reapplies
// it every time it puts a new session in place.
//
// The uuid is re-sent rather than re-derived.  Working out WHICH group
// means waiting for the list, which is not in the login response and
// arrives later on the event queue, so re-deriving would stall every
// reconnect for as long as that takes to reach an answer that cannot
// have changed.
// Why: doc/daemon.md#the-active-group

import (
	"context"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
)

// SetGroup makes a group the avatar's active one and remembers it, so
// that it is put back after every reconnect.
//
// A zero uuid means no group, which is what a login starts with; it is
// recorded and nothing is sent.
func (h *Hosted) SetGroup(ctx context.Context, group msg.UUID) error {
	h.mu.Lock()
	h.group = group
	h.mu.Unlock()

	if group.IsZero() {
		return nil
	}
	return activateGroup(ctx, h.Agent(), group)
}

// Group is the group being acted as, or the zero uuid for none.
func (h *Hosted) Group() msg.UUID {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.group
}

// restoreGroup puts the remembered group back on a session that has
// just been established.
//
// Failing is not fatal to the reconnect: the session is up and usable
// for everything that does not need land rights, and taking it down
// again would trade a building problem for no session at all.  It is
// said out loud instead, because the whole reason this exists is that
// losing the group is otherwise silent.
func (h *Hosted) restoreGroup(ctx context.Context, a *agent.Agent) {
	group := h.Group()
	if group.IsZero() {
		return
	}
	if err := activateGroup(ctx, a, group); err != nil {
		h.logf("could not put the active group back after reconnecting: %v", err)
		return
	}
	h.logf("acting as group %s again", group)
}

// activateGroup sends the request.
//
// Fire and forget, like most of this protocol: the simulator answers
// with an AgentDataUpdate, which the agent records, and there is
// nothing here to wait for.
func activateGroup(ctx context.Context, a *agent.Agent, group msg.UUID) error {
	if a == nil {
		return errNoSession
	}
	m := &msg.ActivateGroup{}
	m.AgentData.AgentID = a.Account.AgentID
	m.AgentData.SessionID = a.Account.SessionID
	m.AgentData.GroupID = group
	return a.Send.Send(ctx, m)
}
