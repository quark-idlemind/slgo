package agent

// How a set's children are ordered, and whether the order can be believed,
// from what each circuit was sent.  The object store is shared by every
// agent in a region and each agent's circuit numbers its packets by
// itself, so the sequence numbers of two circuits mean nothing against
// each other: each circuit's order of a set is kept apart, and a set is
// known only when a circuit that has described all of it says the order,
// and every other such circuit says the same.
// Why: doc/objects.md#ordering-by-the-packet

import (
	"fmt"
	"slices"
	"sort"
)

// circKey is where one circuit listed a child: the sequence number of the
// packet and the block's place in it.
type circKey struct {
	circ uint32
	seq  uint32
	idx  uint16
}

// keyOf is the key circuit c gave this object.
func (v *Object) keyOf(c uint32) (circKey, bool) {
	for _, k := range v.keys {
		if k.circ == c {
			return k, true
		}
	}
	return circKey{}, false
}

// keyBefore says whether a was sent before b, by one circuit.
func keyBefore(a, b circKey) bool {
	return listedBefore(a.seq, int(a.idx), b.seq, int(b.idx))
}

// orderVerdict is what the keys of a parent's children say.
type orderVerdict struct {
	// ok: some circuit has described every child under the parent, and
	// every circuit that has agrees on the order.
	ok bool
	// note says why not, for slsh objects --how.
	note string
}

// keyLocked records the key a circuit gave an object that is already
// listed under its parent, when this is the first the circuit says of it
// as a fresh description: a second agent hearing what the first listed.
func (o *Objects) keyLocked(v *Object, ev evidence) {
	if v.exempt || ev.refill || ev.msg == 0 || v.Parent == 0 || v.PCode == pcodeAvatar {
		return
	}
	if _, ok := v.keyOf(ev.circ); ok {
		return
	}
	v.keys = append(slices.Clip(v.keys), circKey{ev.circ, ev.seq, uint16(ev.index)})
	o.orderLocked(v.Parent)
}

// orderLocked puts a parent's children in the order of the circuit that
// has described the most of them, and works out whether that order can be
// believed.  Children with no key of their own (a live link, a listing
// that came by no packet) keep their places, and a child the chosen
// circuit has no key for goes after those it has, in the order it came.
//
// A circuit has described the set completely when it has a key for every
// child the store holds under the parent that is not exempt.  The root
// says nothing of how many children there are, so this is complete as to
// the children seen, and a child the region never sent leaves every
// circuit short alike.  Keys of different circuits are never compared.
// Why: doc/objects.md#ordering-by-the-packet
func (o *Objects) orderLocked(p uint32) {
	list := o.kids[p]
	if len(list) == 0 || o.explicit[p] {
		delete(o.verdicts, p)
		return
	}
	var slots []int // places in list of children that have a key
	counts := map[uint32]int{}
	need := 0 // children that are not exempt
	for i, id := range list {
		c := o.byID[id]
		if c == nil || c.exempt {
			continue
		}
		need++
		for _, k := range c.keys {
			counts[k.circ]++
		}
		if len(c.keys) > 0 {
			slots = append(slots, i)
		}
	}
	if need == 0 {
		o.verdicts[p] = orderVerdict{ok: true}
		return
	}
	if len(counts) == 0 {
		o.verdicts[p] = orderVerdict{note: "no circuit described these children afresh (answers to requests)"}
		return
	}
	// The circuit that has described the most, the lowest on a tie.
	var best uint32
	bestN := -1
	for c, n := range counts {
		if n > bestN || (n == bestN && c < best) {
			best, bestN = c, n
		}
	}
	// Reorder the keyed children among their own places.
	var with, without []*Object
	for _, i := range slots {
		c := o.byID[list[i]]
		if _, ok := c.keyOf(best); ok {
			with = append(with, c)
		} else {
			without = append(without, c)
		}
	}
	sorted := true
	for i := 1; i < len(with); i++ {
		a, _ := with[i-1].keyOf(best)
		b, _ := with[i].keyOf(best)
		if !keyBefore(a, b) {
			sorted = false
			break
		}
	}
	if !sorted {
		sort.SliceStable(with, func(i, j int) bool {
			a, _ := with[i].keyOf(best)
			b, _ := with[j].keyOf(best)
			return keyBefore(a, b)
		})
	}
	// With is before without in the slots whatever: a child only another
	// circuit keyed follows the ones this circuit keyed.
	ordered := append(with, without...)
	for n, i := range slots {
		list[i] = ordered[n].ID
	}

	// Which circuits are complete, and do they agree: each one's order of
	// the children, read off the list the chosen circuit put in order.
	var complete []uint32
	for c, n := range counts {
		if n == need {
			complete = append(complete, c)
		}
	}
	if len(complete) == 0 {
		o.verdicts[p] = orderVerdict{note: "no circuit has described every child of the set"}
		return
	}
	slices.Sort(complete)
	for _, c := range complete {
		var prev circKey
		first := true
		for _, id := range list {
			ch := o.byID[id]
			if ch == nil || ch.exempt {
				continue
			}
			k, _ := ch.keyOf(c)
			if !first && !keyBefore(prev, k) {
				o.verdicts[p] = orderVerdict{note: fmt.Sprintf(
					"circuits %d and %d each described every child and disagree on the order", complete[0], c)}
				return
			}
			prev, first = k, false
		}
	}
	o.verdicts[p] = orderVerdict{ok: true}
}

// noteLocked is why the set under this parent is not known by its keys, or
// empty.
func (o *Objects) noteLocked(set uint32) string {
	if v, ok := o.verdicts[set]; ok && !v.ok && !o.explicit[set] {
		return v.note
	}
	return ""
}

// dropCircuit takes a circuit's keys away, as when its agent leaves the
// region: a set that only that circuit described completely is then not
// known by any.
func (o *Objects) dropCircuit(circ uint32) {
	o.mu.Lock()
	defer o.mu.Unlock()
	parents := map[uint32]bool{}
	for _, v := range o.byID {
		i := slices.IndexFunc(v.keys, func(k circKey) bool { return k.circ == circ })
		if i < 0 {
			continue
		}
		v.keys = slices.Delete(slices.Clone(v.keys), i, i+1)
		if v.listed {
			parents[v.listedUnder] = true
		}
	}
	for p := range parents {
		o.orderLocked(p)
	}
}

// Leave is what an agent does when it leaves a region's store: it takes
// its viewpoint away (Unwatch) and the keys its circuit gave.
func (o *Objects) Leave(key string, circ uint32) {
	o.Unwatch(key)
	if circ != 0 {
		o.dropCircuit(circ)
	}
}
