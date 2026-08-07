// Command slgo-build exercises object creation against a running
// slgod: rez two prims, link them, and read back what the simulator
// says about the result.
//
// Everything here is client side.  The server relays bytes and knows
// nothing about prims, which is the point.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/quark-idlemind/slgo/client"
	"github.com/quark-idlemind/slgo/internal/slhost"
	"github.com/quark-idlemind/slgo/msg"
)

var (
	addr  = flag.String("server", "127.0.0.1:7805", "slgod address")
	agent = flag.String("agent", "", "hosted agent ($SLGO_AGENT, or the daemon's default)")
	at    = flag.String("at", "192,205,27", "where to rez, region local")
	chat  = flag.String("chatlog", "", "append heard chat to this file")
)

// parentsMu guards the maps the reader goroutine fills.
var parentsMu sync.Mutex

type session struct {
	c    *client.Conn
	me   msg.UUID
	sess msg.UUID
}

func main() {
	flag.Parse()
	ctx := context.Background()

	c, err := client.Dial(ctx, slhost.MustAddr(*addr))
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	info, err := c.Attach(ctx, *agent,
		"ObjectUpdate", "ObjectPropertiesFamily", "ObjectProperties",
		"ChatFromSimulator", "AlertMessage")
	if err != nil {
		log.Fatal(err)
	}
	s := &session{
		c:    c,
		me:   msg.MustParseUUID(info.AgentId),
		sess: msg.MustParseUUID(info.SessionId),
	}
	fmt.Printf("%s in %s\n\n", info.AvatarName, info.Region)

	var chatOut *os.File
	if *chat != "" {
		chatOut, err = os.OpenFile(*chat, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			log.Fatal(err)
		}
		defer chatOut.Close()
	}

	// Everything the simulator says, sorted as it arrives.  The
	// object work below reads from these.
	props := make(chan *msg.ObjectPropertiesFamily, 256)
	full := make(chan *msg.ObjectProperties, 64)
	seen := map[msg.UUID]uint32{}
	parents := map[uint32]uint32{}
	go func() {
		for m := range c.Messages() {
			v, err := m.Decode()
			if err != nil || v == nil {
				continue
			}
			switch t := v.(type) {
			case *msg.ObjectUpdate:
				parentsMu.Lock()
				for i := range t.ObjectData {
					o := &t.ObjectData[i]
					seen[o.FullID] = o.ID
					parents[o.ID] = o.ParentID
				}
				parentsMu.Unlock()
			case *msg.ObjectPropertiesFamily:
				select {
				case props <- t:
				default:
				}
			case *msg.ObjectProperties:
				select {
				case full <- t:
				default:
				}
			case *msg.ChatFromSimulator:
				line := fmt.Sprintf("%s  %s: %s",
					time.Now().Format("15:04:05"),
					nul(t.ChatData.FromName), nul(t.ChatData.Message))
				fmt.Println("  [chat]", line)
				if chatOut != nil {
					fmt.Fprintln(chatOut, line)
				}
			case *msg.AlertMessage:
				fmt.Println("  [alert]", nul(t.AlertData.Message))
			}
		}
	}()

	// Say something, so the chat log has something in it that we
	// know the shape of.
	s.say(ctx, "slgo-build: starting")

	var x, y, z float32
	if _, err := fmt.Sscanf(*at, "%f,%f,%f", &x, &y, &z); err != nil {
		log.Fatalf("bad -at %q", *at)
	}

	fmt.Println("settling so the interest list fills")
	time.Sleep(10 * time.Second)

	first := s.rez(ctx, seen, props, msg.Vector3{X: x, Y: y, Z: z}, 0.5)
	if first == nil {
		log.Fatal("the first prim never appeared")
	}
	fmt.Printf("\nprim A  %s  local %d\n", first.id, first.local)

	second := s.rez(ctx, seen, props, msg.Vector3{X: x + 1, Y: y, Z: z}, 0.5)
	if second == nil {
		log.Fatal("the second prim never appeared")
	}
	fmt.Printf("prim B  %s  local %d\n", second.id, second.local)

	// Linking needs both selected.
	fmt.Println("\nselecting both and linking")
	s.send(ctx, sel(s.me, s.sess, first.local, second.local))
	time.Sleep(2 * time.Second)
	link := &msg.ObjectLink{}
	link.AgentData.AgentID, link.AgentData.SessionID = s.me, s.sess
	link.ObjectData = []msg.ObjectLink_ObjectData{
		{ObjectLocalID: first.local},
		{ObjectLocalID: second.local},
	}
	s.send(ctx, link)
	time.Sleep(4 * time.Second)

	// A link shows up as a ParentID on the child.
	fmt.Println("\nafter linking:")
	parentsMu.Lock()
	for _, o := range []*found{first, second} {
		role := "child of " + fmt.Sprint(parents[o.local])
		if parents[o.local] == 0 {
			role = "root"
		}
		fmt.Printf("  local %-12d %s\n", o.local, role)
	}
	parentsMu.Unlock()

	s.report(ctx, full, first, second)

	s.say(ctx, "slgo-build: done")
	time.Sleep(3 * time.Second)
}

type found struct {
	id    msg.UUID
	local uint32
}

func (s *session) send(ctx context.Context, m msg.Message) {
	if err := s.c.Send(ctx, m, true); err != nil {
		log.Fatalf("send %s: %v", m.MsgInfo().Name, err)
	}
}

func (s *session) say(ctx context.Context, what string) {
	m := &msg.ChatFromViewer{}
	m.AgentData.AgentID, m.AgentData.SessionID = s.me, s.sess
	m.ChatData.Message = append([]byte(what), 0)
	m.ChatData.Type = 1 // say
	m.ChatData.Channel = 0
	s.send(ctx, m)
}

func sel(me, sess msg.UUID, locals ...uint32) *msg.ObjectSelect {
	m := &msg.ObjectSelect{}
	m.AgentData.AgentID, m.AgentData.SessionID = me, sess
	for _, l := range locals {
		m.ObjectData = append(m.ObjectData, msg.ObjectSelect_ObjectData{ObjectLocalID: l})
	}
	return m
}

func cube(me, sess msg.UUID, at msg.Vector3, size float32) *msg.ObjectAdd {
	m := &msg.ObjectAdd{}
	m.AgentData.AgentID, m.AgentData.SessionID = me, sess
	d := &m.ObjectData
	d.PCode, d.Material, d.AddFlags = 9, 3, 2
	d.PathCurve, d.ProfileCurve = 16, 1
	d.PathScaleX, d.PathScaleY = 100, 100
	d.BypassRaycast = 1
	d.RayStart, d.RayEnd = at, at
	d.Scale = msg.Vector3{X: size, Y: size, Z: size}
	return m
}

// rez creates a prim and returns whichever object appeared.
//
// The new one is found by watching for a local id that was not there
// before.  Comparing sets of our objects instead would be wrong: the
// interest list fills gradually, so an attachment worn all along can
// look new simply because the simulator had not mentioned it yet.
func (s *session) rez(ctx context.Context, seen map[msg.UUID]uint32,
	props chan *msg.ObjectPropertiesFamily, at msg.Vector3, size float32) *found {

	before := map[uint32]bool{}
	parentsMu.Lock()
	for _, local := range seen {
		before[local] = true
	}
	parentsMu.Unlock()

	s.send(ctx, cube(s.me, s.sess, at, size))
	time.Sleep(6 * time.Second)

	var fresh []found
	parentsMu.Lock()
	for id, local := range seen {
		// An attachment has local id 0 and is never what we
		// just rezzed.
		if local != 0 && !before[local] {
			fresh = append(fresh, found{id: id, local: local})
		}
	}
	parentsMu.Unlock()
	if len(fresh) == 0 {
		return nil
	}

	// Of the new ones, keep the one we own.
	for _, f := range fresh {
		r := &msg.RequestObjectPropertiesFamily{}
		r.AgentData.AgentID, r.AgentData.SessionID = s.me, s.sess
		r.ObjectData.ObjectID = f.id
		s.send(ctx, r)
	}
	deadline := time.After(8 * time.Second)
	for {
		select {
		case p := <-props:
			if p.ObjectData.OwnerID != s.me {
				continue
			}
			for _, f := range fresh {
				if f.id == p.ObjectData.ObjectID {
					return &f
				}
			}
		case <-deadline:
			return nil
		}
	}
}

// report selects the prims and prints the full properties, which carry
// the permission masks and the inventory serial.
func (s *session) report(ctx context.Context, full chan *msg.ObjectProperties, of ...*found) {
	locals := make([]uint32, 0, len(of))
	for _, o := range of {
		locals = append(locals, o.local)
	}
	s.send(ctx, sel(s.me, s.sess, locals...))

	deadline := time.After(10 * time.Second)
	got := map[msg.UUID]bool{}
	for len(got) < len(of) {
		select {
		case p := <-full:
			for i := range p.ObjectData {
				o := &p.ObjectData[i]
				if got[o.ObjectID] {
					continue
				}
				got[o.ObjectID] = true
				fmt.Printf("\n%s  %q\n", o.ObjectID, nul(o.Name))
				fmt.Printf("  creator     %s\n", o.CreatorID)
				fmt.Printf("  owner       %s\n", o.OwnerID)
				// CreationDate is microseconds, not seconds:
				// reading it as seconds dates the prim to the
				// year 56 million.
				fmt.Printf("  created     %s\n",
					time.UnixMicro(int64(o.CreationDate)).Format(time.RFC3339))
				fmt.Printf("  base        %#08x\n", o.BaseMask)
				fmt.Printf("  owner mask  %#08x\n", o.OwnerMask)
				fmt.Printf("  group       %#08x\n", o.GroupMask)
				fmt.Printf("  everyone    %#08x\n", o.EveryoneMask)
				fmt.Printf("  next owner  %#08x\n", o.NextOwnerMask)
				fmt.Printf("  sale        type %d, price %d\n", o.SaleType, o.SalePrice)
				fmt.Printf("  inv serial  %d\n", o.InventorySerial)
				fmt.Printf("  category    %d\n", o.Category)
			}
		case <-deadline:
			fmt.Printf("\n(only %d of %d reported)\n", len(got), len(of))
			return
		}
	}
}

func nul(b []byte) string {
	if n := len(b); n > 0 && b[n-1] == 0 {
		b = b[:n-1]
	}
	return string(b)
}
