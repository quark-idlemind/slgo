package server

// The active group, and why the server has to keep it.
//
// A parcel usually grants "create objects" to a GROUP rather than to
// individuals, and a login starts with NONE active.  A viewer hides
// this by storing the group in its settings and re-sending it every
// time, which makes it feel like a property of the account; headless it
// is not.  So an avatar that builds happily through a viewer cannot rez
// a thing here, and the refusal blames the land -- the wrong place to
// look.
//
// Settling it once, after logging in, is therefore not enough: a
// reconnect is a FRESH LOGIN, with a new session id and no active
// group, and an avatar that was building five minutes ago silently
// stops being able to.  That is a fault found in the field on
// 2026-08-07, on a session that had reconnected.
//
// So the resolved uuid belongs to the Hosted, which survives
// reconnection, rather than to the agent, which does not -- and the
// supervisor reapplies it every time it puts a new session in place.
//
// The uuid is re-sent rather than re-derived.  Working out WHICH group
// means waiting for the list, which is not in the login response and
// arrives later on the event queue, so re-deriving would stall every
// reconnect for as long as that takes to reach an answer that cannot
// have changed.

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
