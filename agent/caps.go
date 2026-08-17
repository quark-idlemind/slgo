package agent

import (
	"bytes"
	"context"
	"fmt"
	"github.com/quark-idlemind/slgo/llsd"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// DefaultCaps is what a session asks the seed capability for.  Naming
// one that does not exist is harmless: the reply simply omits it.
var DefaultCaps = []string{
	"InventoryAPIv3",
	"LibraryAPIv3",
	"FetchInventory2",
	"FetchInventoryDescendents2",
	"EventQueueGet",
	"GetDisplayNames",
	"UpdateNotecardAgentInventory",
	"UpdateScriptAgent",
	// The Task variants edit the copy inside a prim rather than the
	// one in inventory.  Same two step upload, one more field.
	"UpdateNotecardTaskInventory",
	"UpdateScriptTask",
	"NewFileAgentInventory",
	// Searching for somebody by part of their name.  The UDP
	// AvatarPickerRequest is still answered but only ever matches a
	// whole name: "Quark Idlemind" finds them and "quark" finds
	// nothing, so anything that means to search has to come here.
	"AvatarPickerSearch",
	// What this simulator supports and which LSL it implements.  The
	// second is the authoritative list of functions, constants and
	// events, straight from the machine that will run them, and the
	// first carries the id that says when it last changed.
	"SimulatorFeatures",
	"LSLSyntax",
	"ViewerAsset",
	"GetTexture",
	"GetMesh2",
	"ObjectMedia",
	"ParcelPropertiesUpdate",
	"RemoteParcelRequest",
	"ViewerStats",
}

// Caps maps a capability name to the URL that serves it.
type Caps map[string]string

// Get returns a capability URL, and whether the simulator offered it.
func (c Caps) Get(name string) (string, bool) {
	u, ok := c[name]
	return u, ok && u != ""
}

// Names lists the capabilities on offer, sorted.
func (c Caps) Names() []string {
	out := make([]string, 0, len(c))
	for k := range c {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Caps are the capability URLs the region this session is in offered.
//
// It is a method rather than the field it used to be because the set
// belongs to the region and not to the session: a teleport replaces
// every URL in it at once, while requests are being made through it, and
// a plain field read as that happened was a data race.  The map is the
// live one and is not to be written to; a URL taken out of it is worth
// no more than the moment it was read, since the avatar may have left.
func (a *Agent) Caps() Caps {
	if c := a.caps.Load(); c != nil {
		return *c
	}
	return Caps{}
}

// Seed is the capability the set above was fetched from: the seed of
// the region the avatar is in NOW.
//
// It lives here rather than being worked out by the caller because
// there is nowhere else to work it out from.  Account.SeedCapability is
// the region this session LOGGED IN to and stays that for the rest of
// the session; the seed of every region after the first arrives inside
// a TeleportFinish that only this package reads, and moveTo was handed
// it, used it and dropped it.  Anything above wanting to hand a viewer
// or a client this region's capabilities could only guess, and the
// guess is right until the first teleport and wrong afterwards -- which
// is the worst shape a bug can have.
//
// Empty means there is no seed for the region we are in: a login
// response that carried none, or a move whose TeleportFinish named a
// seed that was not a URL.  SkipCaps does not empty it -- that says not
// to fetch the set, not that there is nowhere to fetch it from -- so a
// session that skipped them still says where they would have come from.
// What is never returned is the login seed after a move: a URL into a
// region the avatar has left is worse than none.
func (a *Agent) Seed() string {
	if s := a.seed.Load(); s != nil {
		return *s
	}
	return ""
}

func (a *Agent) setSeed(s string) { a.seed.Store(&s) }

// SetCaps replaces the set, for a caller that got them from somewhere
// this package did not: a session handed capabilities by whatever
// arranged the login, or a test.
func (a *Agent) SetCaps(c Caps) {
	if c == nil {
		c = Caps{}
	}
	a.caps.Store(&c)
}

// RequestCaps asks a seed capability for the URLs of the named
// capabilities.
//
// The reply also carries a Metadata map describing throttles and the
// like.  It is nested, so it is skipped here rather than flattened into
// the capability names -- which is what the C client does, and why its
// log fills with "Unknown capability: Metadata.account_level_benefits".
func RequestCaps(ctx context.Context, seed string, names []string, hc *http.Client) (Caps, error) {
	if seed == "" {
		return nil, fmt.Errorf("agent: no seed capability")
	}
	if len(names) == 0 {
		names = DefaultCaps
	}
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}

	body, err := llsd.Encode(names)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, seed, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/llsd+xml")
	req.Header.Set("Accept", "application/llsd+xml")

	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("agent: seed capability: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return nil, fmt.Errorf("agent: seed capability returned %s: %s",
			resp.Status, strings.TrimSpace(string(snippet)))
	}

	v, err := llsd.Decode(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("agent: seed capability: %w", err)
	}
	m := llsd.Map(v)
	if m == nil {
		return nil, fmt.Errorf("agent: seed capability returned %T, wanted a map", v)
	}

	caps := Caps{}
	for k, val := range m {
		// Metadata is a nested map of throttles and benefits,
		// not a capability.
		if s, ok := val.(string); ok && s != "" {
			caps[k] = s
		}
	}
	return caps, nil
}
