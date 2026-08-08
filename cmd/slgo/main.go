// Command slgo attaches to a running slgod and reports what it finds.
//
//	slgo -server 127.0.0.1:7778 -agent example status
//	slgo -agent example watch ChatFromSimulator ImprovedInstantMessage
//	slgo -agent example inventory
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/client"
	"github.com/quark-idlemind/slgo/internal/session"
	"github.com/quark-idlemind/slgo/msg"
	slgov1 "github.com/quark-idlemind/slgo/proto/slgov1"
)

func main() {
	var (
		addr    = flag.String("server", "127.0.0.1:7778", "slgod address")
		name    = flag.String("agent", "", "which hosted agent ($SLGO_AGENT, or the daemon's default)")
		force   = flag.Bool("force", false, "for host and logout: overrule a deliberate stop, or clients in use")
		limit   = flag.Duration("for", 30*time.Second, "how long to watch")
		logfile = flag.String("log", "", "append chat to this file instead of stdout")
	)
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: slgo [-server addr] [-agent name] {agents|status|host NAME|logout NAME|chat|watch [message...]|inventory}")
		os.Exit(2)
	}

	ctx := context.Background()
	c, err := client.Dial(ctx, *addr)
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	switch cmd := flag.Arg(0); cmd {
	case "agents":
		agents, err := c.ListAgents(ctx)
		if err != nil {
			log.Fatal(err)
		}
		for _, a := range agents {
			// Say when one is down.  A session that ended still
			// answers questions out of what it last heard, so a
			// listing that showed only the name and region would
			// show a dead session as a healthy one.
			where := a.Region
			if !a.Connected {
				where = "NOT CONNECTED"
			}
			fmt.Printf("%-12s %-24s %-20s %s\n", a.Name, a.AvatarName, where, a.AgentId)
		}

	case "host":
		if flag.NArg() < 2 {
			log.Fatal("host NAME -- which agent to start; there is no default for starting one")
		}
		who := flag.Arg(1)
		r, err := c.Host(ctx, who, *force)
		if err != nil {
			log.Fatal(err)
		}
		if r.GetAlready() {
			fmt.Printf("%s was already up: %s in %s\n",
				who, r.Agent.GetAvatarName(), r.Agent.GetRegion())
			return
		}
		fmt.Printf("%s: %s in %s\n", who, r.Agent.GetAvatarName(), r.Agent.GetRegion())

	case "logout":
		if flag.NArg() < 2 {
			log.Fatal("logout NAME -- which agent to log out")
		}
		who := flag.Arg(1)
		r, err := c.Logout(ctx, who, *force)
		if err != nil {
			if cs := r.GetClients(); len(cs) > 0 {
				log.Fatalf("%v\n        attached: %s", err, strings.Join(cs, ", "))
			}
			log.Fatal(err)
		}
		fmt.Printf("%s logged out; it will not come back until asked for by name\n", who)

	case "status":
		mustAttach(ctx, c, *name)
		st, err := c.Status(ctx)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%s (%s) in %s\n", st.Agent.AvatarName, st.Agent.Name, st.Agent.Region)
		fmt.Printf("  simulator   %s\n", st.Agent.ChannelVersion)
		fmt.Printf("  packets     %d in, %d out (%d resent, %d abandoned)\n",
			st.PacketsIn, st.PacketsOut, st.Resent, st.Abandoned)
		fmt.Printf("  duplicates  %d\n", st.Duplicates)
		fmt.Printf("  clients     %d\n", st.Clients)
		fmt.Printf("  caps        %d\n", len(st.Agent.Caps))
		if len(st.Unhandled) > 0 {
			keys := make([]string, 0, len(st.Unhandled))
			for k := range st.Unhandled {
				keys = append(keys, k)
			}
			sort.Slice(keys, func(i, j int) bool { return st.Unhandled[keys[i]] > st.Unhandled[keys[j]] })
			fmt.Println("  no handler for:")
			for _, k := range keys {
				fmt.Printf("    %-32s %d\n", k, st.Unhandled[k])
			}
		}

	case "chat":
		// Public chat is channel 0 by definition: the simulator
		// only sends ChatFromSimulator for what an avatar could
		// hear, and anything on another channel never reaches a
		// viewer at all.
		mustAttach(ctx, c, *name, "ChatFromSimulator")
		w := os.Stdout
		if *logfile != "" {
			f, err := os.OpenFile(*logfile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if err != nil {
				log.Fatal(err)
			}
			defer f.Close()
			w = f
			fmt.Printf("logging chat to %s\n", *logfile)
		}
		fmt.Fprintf(w, "# chat log opened %s\n", time.Now().Format(time.RFC3339))
		deadline := time.After(*limit)
		for {
			select {
			case m, ok := <-c.Messages():
				if !ok {
					log.Fatalf("stream ended: %v", c.Err())
				}
				v, err := m.Decode()
				if err != nil || v == nil {
					continue
				}
				logChat(w, v.(*msg.ChatFromSimulator))
			case <-deadline:
				return
			}
		}

	case "watch":
		names := flag.Args()[1:]
		if len(names) == 0 {
			names = []string{"*"}
		}
		mustAttach(ctx, c, *name, names...)
		fmt.Printf("watching %v for %s\n", names, *limit)
		deadline := time.After(*limit)
		for {
			select {
			case m, ok := <-c.Messages():
				if !ok {
					log.Fatalf("stream ended: %v", c.Err())
				}
				show(m)
			case <-deadline:
				return
			}
		}

	case "inventory":
		info := mustAttach(ctx, c, *name)
		root, err := msg.ParseUUID(info.InventoryRoot)
		if err != nil {
			log.Fatalf("no inventory root: %v", err)
		}
		inv := agent.NewInventory(root)
		start := time.Now()
		if err := agent.FetchInventory(ctx, c, inv, agent.FetchOptions{Concurrency: 8}); err != nil {
			log.Fatal(err)
		}
		folders, items := inv.Counts()
		fmt.Printf("%d folders, %d items in %s\n", folders, items, time.Since(start).Round(time.Millisecond))
		for _, f := range inv.Children(inv.Root()) {
			fmt.Printf("  %-46s %2d folders %4d items\n",
				f.Name, len(inv.Children(f.ID)), len(inv.Contents(f.ID)))
		}

	default:
		log.Fatalf("unknown command %q", cmd)
	}
}

func mustAttach(ctx context.Context, c *client.Conn, name string, subscribe ...string) *slgov1.AgentInfo {
	name = session.AgentName(name)
	// An empty name goes through: the daemon picks the session it has
	// held longest.  Resolving it here would make this program's idea of
	// the default differ from every other client's.
	info, err := c.Attach(ctx, name, subscribe...)
	if err != nil {
		log.Fatalf("attach: %v", err)
	}
	return info
}

// chatTypes and sourceTypes name the codes ChatFromSimulator carries.
var chatTypes = map[uint8]string{
	0: "whisper", 1: "say", 2: "shout", 3: "typing-start",
	4: "typing-stop", 5: "debug", 8: "owner-say", 9: "region-say",
}

var sourceTypes = map[uint8]string{0: "system", 1: "agent", 2: "object"}

func logChat(w io.Writer, m *msg.ChatFromSimulator) {
	d := &m.ChatData
	// Typing notifications carry no text and are noise in a log.
	if d.ChatType == 3 || d.ChatType == 4 {
		return
	}
	kind := chatTypes[d.ChatType]
	if kind == "" {
		kind = fmt.Sprintf("type%d", d.ChatType)
	}
	src := sourceTypes[d.SourceType]
	if src == "" {
		src = fmt.Sprintf("src%d", d.SourceType)
	}
	fmt.Fprintf(w, "%s [%s/%s] %s: %s\n",
		time.Now().Format("15:04:05"), src, kind,
		nul(d.FromName), nul(d.Message))
}

// nul drops the terminator the protocol puts on its strings.
func nul(b []byte) string {
	if n := len(b); n > 0 && b[n-1] == 0 {
		b = b[:n-1]
	}
	return string(b)
}

func show(m *client.Message) {
	name := m.Name
	if name == "" {
		name = fmt.Sprintf("unknown %d", m.ID)
	}
	v, err := m.Decode()
	switch {
	case err != nil:
		fmt.Printf("%s seq=%d: undecodable: %v\n", name, m.Sequence, err)
	case v == nil:
		fmt.Printf("%s seq=%d: %d bytes, not in this template\n", name, m.Sequence, len(m.Body))
	default:
		fmt.Print(msg.DumpMessage(v))
	}
}
