// Package slots manages slots of some type.
package slots

import "time"

// Timeout is the default timeout for a slot reservation.
const Timeout = time.Minute

// An ID is a unique ID only assigned by this package.
type ID struct {
	id uint64
}

// A Slot contains all information needed to identify a slot.
type Slot struct {
	id      ID        // ID of this slot, assigned by this package
	removed bool      // Set to true when a slot is no longer available
	expire  time.Time // When the current assignement expires
	Data    any       // Data associated with a slot
}

// A Request is a request for N slots.  The response is sent on Chan.  If
// Timeout is not positive it defaults to Timeout.  When a slots timeout expires
// it may be re-used by another request.  All slots returned expire at the same
// time.
type Request struct {
	N       int
	Timeout time.Duration
	Chan    chan Response
}

// A Response is the response to a Request.  If the request was filled then Wait
// is nil and Slots contains the list of slots to use.  If the request was not
// filled Wait is not nil and the caller can request again when when a receive
// on Wait returns.
type Response struct {
	ID    ID
	Slots []Slot
	Wait  chan struct{}
}

// A Return returns the slots given in a Response.
type Return struct {
	ID ID
}

// An Add adds more slots to the pool.
// Each Slot will have an ID assigned to it.
// If Done is not nil it will be closed when the Add is complete.
type Add struct {
	Slots []*Slot
	Done  chan struct{}
}

// A Remove recalls slots from the pool.
// If Done is not nil it will be closed when the Remove is complete.
type Remove struct {
	Slots []*Slot
	Done  chan struct{}
}

// A Terminate request causes a pool's Run to return.
type Terminate struct{}

// A Pool is a pool of slots.
// Pool reads requests sent to Chan.
type Pool struct {
	Chan     chan any
	wait     chan struct{}
	id       uint64
	assigned map[ID][]*Slot
	slots    map[ID]*Slot
}

// New returns a freshly initialized pool.
func New() *Pool {
	return &Pool{
		Chan:     make(chan any),
		assigned: map[ID][]*Slot{},
		slots:    map[ID]*Slot{},
	}
}

// nextID returns the next unique ID available.
func (p *Pool) nextID() ID { p.id++; return ID{id:p.id} }

// Run runs the pool.  It does not return until a Terminate request arrives.
func (p *Pool) Run() {
	for r := range p.Chan {
		now := time.Now()
		switch req := r.(type) {
		case Add:
			for _, s := range req.Slots {
				s.id = p.nextID()
				p.slots[s.id] = s
			}
		case Remove:
			for _, s := range req.Slots {
				s.removed = true
				delete(p.slots, s.id)
			}
		case Request:
			if req.N <= 0 {
				req.Chan <- Response{}
				break
			}
			if req.N > len(p.slots) {
				for id, slots := range p.assigned {
					if slots[0].expire.Before(now) {
						delete(p.assigned, id)
						for _, s := range slots {
							p.slots[s.id] = s
						}
					}
				}
				if req.N > len(p.slots) {
					if p.wait == nil {
						p.wait = make(chan struct{})
					}
					req.Chan <- Response{Wait: p.wait}
					break
				}
			}
			if req.Timeout <= 0 {
				req.Timeout = Timeout
			}
			expire := now.Add(req.Timeout)
			r := Response{
				ID:    p.nextID(),
				Slots: make([]Slot, 0, req.N),
			}
			slots := make([]*Slot, 0, req.N)
			for id, slot := range p.slots {
				slot.expire = expire
				slots = append(slots, slot)
				r.Slots = append(r.Slots, *slot)
				delete(p.slots, id)
				req.N--
				if req.N == 0 {
					break
				}
			}
			p.assigned[r.ID] = slots
			req.Chan <- r
		case Return:
			for _, s := range p.assigned[req.ID] {
				if s.removed {
					continue
				}
				p.slots[req.ID] = s
			}
			if p.wait != nil {
				close(p.wait)
				p.wait = nil
			}
		case Terminate:
			if p.wait != nil {
				close(p.wait)
				p.wait = nil
			}
			return
		}
	}
}
