// Command slgo-roundtrip saves a script and reads the asset back, to
// find out whether what comes out is what went in.
//
// The script it uses has a tab indenting a line and a tab inside a
// string literal.  The first only has to survive storage; the second
// has to survive it exactly, because it is data.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/client"
	"github.com/quark-idlemind/slgo/internal/slhost"
	"github.com/quark-idlemind/slgo/llsd"
	"github.com/quark-idlemind/slgo/msg"
)

var (
	addr    = flag.String("server", "", "slgod address (default: sl-host, port 7807)")
	profile = flag.String("agent", "", "hosted agent ($SLGO_AGENT, or the daemon's default)")
)

const (
	assetScript = 10
	invScript   = 10
	target      = "mono"
)

// source is the script under test, with real tabs.
const source = "default { state_entry() {\n\tllOwnerSay(\"hello\tworld\");\n} }\n"

type run struct {
	c    *client.Conn
	me   msg.UUID
	sess msg.UUID

	mu      sync.Mutex
	created map[uint32]*msg.UpdateCreateInventoryItem_InventoryData
}

func main() {
	flag.Parse()
	ctx := context.Background()

	c, err := client.Dial(ctx, slhost.MustAddr(*addr))
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	info, err := c.Attach(ctx, *profile,
		"UpdateCreateInventoryItem", "TransferInfo", "TransferPacket")
	if err != nil {
		log.Fatal(err)
	}
	r := &run{
		c: c, me: msg.MustParseUUID(info.AgentId),
		sess:    msg.MustParseUUID(info.SessionId),
		created: map[uint32]*msg.UpdateCreateInventoryItem_InventoryData{},
	}
	transfers := client.NewTransfers(c)
	fmt.Printf("%s in %s\n\n", info.AvatarName, info.Region)

	go func() {
		for m := range c.Messages() {
			if transfers.Handle(m) {
				continue
			}
			v, err := m.Decode()
			if err != nil || v == nil {
				continue
			}
			if t, ok := v.(*msg.UpdateCreateInventoryItem); ok {
				r.mu.Lock()
				for i := range t.InventoryData {
					d := t.InventoryData[i]
					r.created[d.CallbackID] = &d
				}
				r.mu.Unlock()
			}
		}
	}()

	fmt.Println("--- what went in ---")
	fmt.Print(dump([]byte(source)))

	item := r.createScript(ctx, "slgo roundtrip")
	fmt.Printf("\nitem  %s\n", item.ItemID)

	body, err := r.upload(ctx, item.ItemID, []byte(source))
	if err != nil {
		log.Fatalf("saving: %v", err)
	}
	m := llsd.Map(mustLLSD(body))
	asset, err := msg.ParseUUID(llsd.String(m, "new_asset"))
	if err != nil {
		log.Fatalf("no new_asset in %s", snippet(body, 300))
	}
	fmt.Printf("asset %s\n", asset)
	fmt.Printf("compiled %v", llsd.Bool(m, "compiled"))
	if errs, ok := m["errors"].([]any); ok {
		for _, e := range errs {
			fmt.Printf("\n  %v", e)
		}
	}
	fmt.Println()

	// The asset is only readable once the simulator has finished
	// storing it.
	time.Sleep(3 * time.Second)

	got, err := transfers.Fetch(ctx, r.me, r.sess, client.AssetRef{
		Owner: r.me,
		Item:  item.ItemID,
		Asset: asset,
		Type:  client.AssetLSLText,
	}, 30*time.Second)
	if err != nil {
		log.Fatalf("reading it back: %v", err)
	}

	fmt.Printf("\n--- what came back (%d bytes) ---\n", len(got))
	fmt.Print(dump(got))

	fmt.Println("\n--- comparison ---")
	compare([]byte(source), got)
}

// compare reports whether the two are the same, and where they first
// differ if they are not.
func compare(in, out []byte) {
	if bytes.Equal(in, out) {
		fmt.Printf("IDENTICAL, %d bytes, both tabs survived\n", len(in))
		return
	}
	fmt.Printf("DIFFERENT: %d bytes in, %d bytes out\n", len(in), len(out))

	n := min(len(in), len(out))
	at := n
	for i := range n {
		if in[i] != out[i] {
			at = i
			break
		}
	}
	fmt.Printf("first difference at offset %d\n", at)
	if at < n {
		fmt.Printf("  in  %#02x %s\n", in[at], describe(in[at]))
		fmt.Printf("  out %#02x %s\n", out[at], describe(out[at]))
	} else {
		fmt.Println("  one is a prefix of the other")
	}

	// Tabs are the point of the exercise, so count them either way.
	fmt.Printf("tabs: %d in, %d out\n",
		bytes.Count(in, []byte{'\t'}), bytes.Count(out, []byte{'\t'}))
}

func describe(b byte) string {
	switch b {
	case '\t':
		return "tab"
	case '\n':
		return "newline"
	case '\r':
		return "carriage return"
	case ' ':
		return "space"
	}
	if b >= 0x20 && b < 0x7f {
		return fmt.Sprintf("%q", rune(b))
	}
	return "control"
}

// dump prints bytes with tabs and newlines made visible, so a tab
// turned into spaces cannot hide.
func dump(b []byte) string {
	var s strings.Builder
	for _, line := range strings.SplitAfter(string(b), "\n") {
		if line == "" {
			continue
		}
		shown := strings.ReplaceAll(strings.TrimSuffix(line, "\n"), "\t", "<TAB>")
		fmt.Fprintf(&s, "  %s<NL>\n", shown)
	}
	return s.String()
}

func (r *run) createScript(ctx context.Context, name string) *msg.UpdateCreateInventoryItem_InventoryData {
	cb := uint32(time.Now().UnixNano() & 0x7fffffff)
	m := &msg.CreateInventoryItem{}
	m.AgentData.AgentID, m.AgentData.SessionID = r.me, r.sess
	b := &m.InventoryBlock
	b.CallbackID = cb
	b.NextOwnerMask = 0x0008e000
	b.Type, b.InvType = assetScript, invScript
	b.Name = append([]byte(name), 0)
	b.Description = append([]byte("slgo tab round trip"), 0)
	if err := r.c.Send(ctx, m, true); err != nil {
		log.Fatal(err)
	}

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		d := r.created[cb]
		r.mu.Unlock()
		if d != nil {
			return d
		}
		time.Sleep(100 * time.Millisecond)
	}
	log.Fatalf("the simulator never confirmed creating %q", name)
	return nil
}

// upload saves the source over UpdateScriptAgent and returns the reply.
func (r *run) upload(ctx context.Context, item msg.UUID, body []byte) ([]byte, error) {
	req, err := llsd.Encode(map[string]any{
		"item_id": item.String(),
		"target":  target,
	})
	if err != nil {
		return nil, err
	}
	resp, err := r.c.DoCap(ctx, agent.CapRequest{
		Cap: "UpdateScriptAgent", Method: "POST",
		Type: "application/llsd+xml", Body: req,
	})
	if err != nil {
		return nil, err
	}
	if !resp.OK() {
		return nil, fmt.Errorf("status %d: %s", resp.Status, snippet(resp.Body, 300))
	}
	uploader := llsd.String(llsd.Map(mustLLSD(resp.Body)), "uploader")
	if uploader == "" {
		return nil, fmt.Errorf("no uploader in %s", snippet(resp.Body, 300))
	}

	resp, err = r.c.DoCap(ctx, agent.CapRequest{
		URL: uploader, Method: "POST",
		Type: "application/octet-stream", Body: body,
	})
	if err != nil {
		return nil, err
	}
	if !resp.OK() {
		return nil, fmt.Errorf("upload status %d: %s", resp.Status, snippet(resp.Body, 300))
	}
	return resp.Body, nil
}

func mustLLSD(b []byte) any {
	v, err := llsd.Decode(bytes.NewReader(b))
	if err != nil {
		log.Fatalf("reply is not LLSD: %v\n%s", err, snippet(b, 300))
	}
	return v
}

func snippet(b []byte, n int) string {
	if len(b) > n {
		b = b[:n]
	}
	return strings.TrimSpace(string(b))
}
