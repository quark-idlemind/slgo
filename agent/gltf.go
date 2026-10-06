package agent

// A face's GLTF material overrides, kept with the object they are on.
//
// The region says them in a GenericStreamingMessage (method 0x4175, data
// LLSD notation) to a session that asked the seed for ModifyMaterialParams,
// and says every one in view at once when the avatar arrives: about a
// hundred messages in five seconds for the region it was measured in.  An
// override is by the object's local id and may come before the
// ObjectUpdate that describes that object, so one that finds no object
// waits here, in a set that is bounded, until it does.
//
// One message is the object's whole set of overrides and replaces the last
// (msg.ParseGLTFOverrideUpdate), so what an object holds never grows: at
// most one override for each of its faces.  Overrides are on the Object,
// so whatever removes the object removes them -- a kill, Trim, Flush --
// and nothing here can outlive it.  Only the pending set is held without
// an object, and what bounds it is below.
// Why: doc/gltf.md#what-is-kept

import (
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// maxPendingGLTF is how many local ids may have an override waiting for
// their object, and pendingGLTFAge how long one waits.
//
// The age is a minute, the grace an orphan is given (orphanGrace): the
// object's description came within the same burst when it was watched
// -- the login's hundred overrides were all in five seconds -- and an
// override whose object is not described in a minute is for something out
// of view, which will be described, and say its overrides again, if it
// comes into view.  The count is a bound for a region far busier than the
// one measured, ten times its burst; at it the oldest waiting goes first.
// A session that lives for days therefore holds at most this many, and
// each is at most 45 small faces.
const (
	maxPendingGLTF = 1024
	pendingGLTFAge = time.Minute
)

// pendingOverride is an object's overrides, waiting for the object.
type pendingOverride struct {
	faces map[int]*msg.GLTFOverride
	at    time.Time
}

// setGLTF applies a material override message: the object's overrides
// become what it says, which is none for an empty message.  An object the
// store does not hold yet has them kept in the pending set; an empty
// message for one clears what waits.
func (o *Objects) setGLTF(u *msg.GLTFOverrideUpdate) {
	o.mu.Lock()
	defer o.mu.Unlock()
	faces := u.Faces
	if len(faces) == 0 {
		faces = nil
	}
	if v := o.byLocalLocked(u.Local); v != nil {
		v.GLTF = faces
		delete(o.pendingGLTF, u.Local)
		return
	}
	if faces == nil {
		delete(o.pendingGLTF, u.Local)
		return
	}
	o.pendingGLTF[u.Local] = pendingOverride{faces: faces, at: o.now()}
	o.boundPendingLocked()
}

// boundPendingLocked drops the oldest waiting overrides until there are
// no more than maxPendingGLTF, and any that has waited past its age.
func (o *Objects) boundPendingLocked() {
	now := o.now()
	for local, p := range o.pendingGLTF {
		if now.Sub(p.at) > pendingGLTFAge {
			delete(o.pendingGLTF, local)
		}
	}
	for len(o.pendingGLTF) > maxPendingGLTF {
		var oldest uint32
		var at time.Time
		first := true
		for local, p := range o.pendingGLTF {
			if first || p.at.Before(at) {
				oldest, at, first = local, p.at, false
			}
		}
		delete(o.pendingGLTF, oldest)
	}
}

// expirePendingGLTF is what Trim does for the pending set.
func (o *Objects) expirePendingGLTF() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.boundPendingLocked()
}

// adoptGLTFLocked gives an object that has just been described, by its
// local id, the overrides that were waiting for it, if they have not
// waited past their age and it has none of its own.
func (o *Objects) adoptGLTFLocked(v *Object) {
	p, ok := o.pendingGLTF[v.Local]
	if !ok {
		return
	}
	delete(o.pendingGLTF, v.Local)
	if v.GLTF == nil && o.now().Sub(p.at) <= pendingGLTFAge {
		v.GLTF = p.faces
	}
}

// PendingGLTF is how many local ids have an override waiting for an
// object that has not been described.
func (o *Objects) PendingGLTF() int {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return len(o.pendingGLTF)
}

// pendingCopy is the pending set, for a store that takes this one's
// place (absorb).
func (o *Objects) pendingCopy() map[uint32]pendingOverride {
	o.mu.RLock()
	defer o.mu.RUnlock()
	out := make(map[uint32]pendingOverride, len(o.pendingGLTF))
	for k, v := range o.pendingGLTF {
		out[k] = v
	}
	return out
}

// absorbPendingLocked takes in another store's pending set, keeping the
// newer where both wait on one local id.
func (o *Objects) absorbPendingLocked(from map[uint32]pendingOverride) {
	for local, p := range from {
		if have, ok := o.pendingGLTF[local]; ok && !p.at.After(have.at) {
			continue
		}
		o.pendingGLTF[local] = p
	}
	o.boundPendingLocked()
}

// trackGLTF registers the handler that keeps the overrides current.  It is
// inline, with the object handlers, so that an override is applied in the
// order the region said it relative to the updates of its object.
func (a *Agent) trackGLTF() {
	a.Disp.MustHandle("GenericStreamingMessage", func(p *msg.Packet) {
		m := p.Message.(*msg.GenericStreamingMessage)
		if m.MethodData.Method != msg.GenericMethodGLTFMaterialOverride {
			return
		}
		u, err := msg.ParseGLTFOverrideUpdate(m.DataBlock.Data)
		if err != nil {
			a.logf("material override ignored: %v", err)
			return
		}
		a.Objects().setGLTF(u)
	}, msg.Inline())
}
