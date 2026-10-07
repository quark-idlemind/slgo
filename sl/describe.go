package sl

import (
	"context"
	"errors"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"

	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// Description is one way the region, or this session, described an
// object: the last few are kept per object. See agent.Description.
// Why: doc/objects.md#how-an-object-was-described
type Description = agent.Description

// DescKind is the kind of a Description: full, compressed, terse,
// cached, requested or killed.
type DescKind = agent.DescKind

const (
	DescFull       = agent.DescFull
	DescCompressed = agent.DescCompressed
	DescTerse      = agent.DescTerse
	DescCached     = agent.DescCached
	DescRequested  = agent.DescRequested
	DescKilled     = agent.DescKilled
)

// Describer is what a Backend can also be, when it can say how each
// object was described. Hosted and Direct are.
type Describer interface {
	// Descriptions is, for every object the store holds, the last few
	// ways it was described, oldest first.
	Descriptions(ctx context.Context) (map[msg.UUID][]Description, error)
}

// ErrNoDescriptions is what Descriptions returns for a backend that does
// not keep how objects were described.
var ErrNoDescriptions = errors.New("sl: this session does not keep how objects were described")

// Descriptions is how each object in the region was described, by id:
// the last few ways, oldest first, with the one that put each in its
// parent's list. A daemon older than the record says none for every
// object.
// Why: doc/objects.md#how-an-object-was-described
func (w *Session) Descriptions(ctx context.Context) (map[msg.UUID][]Description, error) {
	d, ok := w.b.(Describer)
	if !ok {
		return nil, ErrNoDescriptions
	}
	return d.Descriptions(ctx)
}

// Descriptions reads what this process's own agent recorded.
func (d *Direct) Descriptions(ctx context.Context) (map[msg.UUID][]Description, error) {
	out := map[msg.UUID][]Description{}
	for _, o := range d.a.Objects().All() {
		out[o.ID] = o.Descriptions()
	}
	return out, nil
}

// Descriptions asks the daemon, which holds the record.
func (h *Hosted) Descriptions(ctx context.Context) (map[msg.UUID][]Description, error) {
	r, err := h.conn.ObjectsHow(ctx, "", "")
	if err != nil {
		return nil, err
	}
	out := make(map[msg.UUID][]Description, len(r.Objects))
	for _, o := range r.Objects {
		id, err := msg.ParseUUID(o.Id)
		if err != nil {
			continue
		}
		out[id] = descriptionsFromPB(o.How)
	}
	return out, nil
}

func descriptionsFromPB(in []*pb.ObjectDescription) []Description {
	out := make([]Description, 0, len(in))
	for _, d := range in {
		out = append(out, Description{
			Kind: DescKind(d.Kind), Parent: d.Parent, Seq: d.Seq, Message: d.Message,
			Block: int(d.Block), Blocks: int(d.Blocks), Count: int(d.Count),
			Refill: d.Refill, Listed: d.Listed, Circuit: d.Circuit, At: time.Unix(0, d.AtUnixNano),
		})
	}
	return out
}
