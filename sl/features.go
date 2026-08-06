package sl

// What the simulator says it supports.
//
// SimulatorFeatures is one http GET that answers a set of questions
// which otherwise have to be discovered by trying something and seeing
// it refused: whether mesh may be rezzed here, how many attachments an
// avatar may wear, how many groups it may join, which voice server the
// region runs, and -- the one this package cares about most -- the id
// of the LSL syntax the region implements, which is how you tell
// whether the syntax you have cached is still the syntax being run.
//
// It is per region and it can differ between them: a beta grid region
// answers differently from a main grid one, which is the whole reason
// the capability exists.

import (
	"bytes"
	"context"
	"fmt"
	"sort"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/llsd"
	"github.com/quark-idlemind/slgo/msg"
)

// Features is the simulator's answer, kept whole.
//
// The set of keys is Linden Lab's to change and has grown several times,
// so the map is the truth and the methods are conveniences over it.  A
// key nobody here has heard of is still in Raw.
type Features struct {
	Raw map[string]any
}

// Features asks the simulator what it supports.
func (w *Session) Features(ctx context.Context) (*Features, error) {
	body, err := w.capDo(ctx, agent.CapRequest{Cap: "SimulatorFeatures", Method: "GET"})
	if err != nil {
		return nil, err
	}
	v, err := llsd.Decode(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("sl: SimulatorFeatures: %w", err)
	}
	m := llsd.Map(v)
	if m == nil {
		return nil, fmt.Errorf("sl: SimulatorFeatures answered with %T, wanted a map", v)
	}
	return &Features{Raw: m}, nil
}

// Names lists the features on offer, sorted.
func (f *Features) Names() []string {
	out := make([]string, 0, len(f.Raw))
	for k := range f.Raw {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Has reports whether the simulator mentioned this at all, which is not
// the same as it being on.
func (f *Features) Has(name string) bool {
	_, ok := f.Raw[name]
	return ok
}

// Bool, Int and String read one feature.  A key that is missing, or is
// not of that shape, reads as the zero value: the simulator not
// mentioning something and saying it is off amount to the same thing.
func (f *Features) Bool(name string) bool {
	b, _ := f.Raw[name].(bool)
	return b
}

func (f *Features) Int(name string) int64 {
	switch v := f.Raw[name].(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	}
	return 0
}

func (f *Features) String(name string) string {
	s, _ := f.Raw[name].(string)
	return s
}

// Map reads a feature that is itself a map, such as PhysicsShapeTypes
// or AnimatedObjects.
func (f *Features) Map(name string) map[string]any {
	m, _ := f.Raw[name].(map[string]any)
	return m
}

// LSLSyntaxID is the id of the LSL this region implements.  It changes
// when Linden Lab changes the language, and is what makes a cached
// syntax safe to reuse.
func (f *Features) LSLSyntaxID() msg.UUID {
	id, err := msg.ParseUUID(f.String("LSLSyntaxId"))
	if err != nil {
		return msg.UUID{}
	}
	return id
}

// The handful worth naming, because a program branches on them rather
// than printing them.
func (f *Features) MeshRezEnabled() bool     { return f.Bool("MeshRezEnabled") }
func (f *Features) MeshUploadEnabled() bool  { return f.Bool("MeshUploadEnabled") }
func (f *Features) LuaScriptsEnabled() bool  { return f.Bool("LuaScriptsEnabled") }
func (f *Features) MaxAgentAttachments() int { return int(f.Int("MaxAgentAttachments")) }
func (f *Features) MaxAgentGroups() int      { return int(f.Int("MaxAgentGroups")) }
func (f *Features) MaxTextureResolution() int {
	return int(f.Int("MaxTextureResolution"))
}
func (f *Features) HostName() string { return f.String("HostName") }
