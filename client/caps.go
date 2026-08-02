package client

import (
	"bytes"
	"context"
	"fmt"
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

// RequestCaps asks a seed capability for the URLs of the named
// capabilities.
//
// The reply also carries a Metadata map describing throttles and the
// like.  It is nested, so it is skipped here rather than flattened into
// the capability names -- which is what the C client does, and why its
// log fills with "Unknown capability: Metadata.account_level_benefits".
func RequestCaps(ctx context.Context, seed string, names []string, hc *http.Client) (Caps, error) {
	if seed == "" {
		return nil, fmt.Errorf("client: no seed capability")
	}
	if len(names) == 0 {
		names = DefaultCaps
	}
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}

	body, err := EncodeLLSD(names)
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
		return nil, fmt.Errorf("client: seed capability: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return nil, fmt.Errorf("client: seed capability returned %s: %s",
			resp.Status, strings.TrimSpace(string(snippet)))
	}

	v, err := DecodeLLSD(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("client: seed capability: %w", err)
	}
	m := llsdMap(v)
	if m == nil {
		return nil, fmt.Errorf("client: seed capability returned %T, wanted a map", v)
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
