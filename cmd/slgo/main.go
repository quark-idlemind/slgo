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
	"log"
	"os"
	"sort"
	"time"

	"slgo/agent"
	"slgo/client"
	"slgo/msg"
	slgov1 "slgo/proto/slgov1"
)

func main() {
	var (
		addr  = flag.String("server", "127.0.0.1:7778", "slgod address")
		name  = flag.String("agent", "", "which hosted agent (default: the only one)")
		limit = flag.Duration("for", 30*time.Second, "how long to watch")
	)
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: slgo [-server addr] [-agent name] {agents|status|watch [message...]|inventory}")
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
			fmt.Printf("%-12s %-24s %-20s %s\n", a.Name, a.AvatarName, a.Region, a.AgentId)
		}

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
	if name == "" {
		agents, err := c.ListAgents(ctx)
		if err != nil {
			log.Fatal(err)
		}
		if len(agents) != 1 {
			log.Fatalf("the server hosts %d agents; name one with -agent", len(agents))
		}
		name = agents[0].Name
	}
	info, err := c.Attach(ctx, name, subscribe...)
	if err != nil {
		log.Fatalf("attach: %v", err)
	}
	return info
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
