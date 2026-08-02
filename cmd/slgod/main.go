// Command slgod holds grid connections and serves clients.
//
//	slgod -listen 127.0.0.1:7778 example builder
//
// Each argument names a profile under ~/.config/slgo.  The connections
// stay up until the process is signalled; clients attach and detach
// freely without the grid noticing.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"slgo/agent"
	"slgo/msg"
	"slgo/server"
)

func main() {
	var (
		listen  = flag.String("listen", "127.0.0.1:7778", "address to serve clients on")
		verbose = flag.Bool("v", false, "log every message the grid sends")
		start   = flag.String("start", "", "override the profile's start location")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: slgod [-listen addr] [-v] profile [profile...]\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}

	log.SetFlags(log.Ltime)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := server.New()

	for _, name := range flag.Args() {
		login, err := agent.LoadProfile(name)
		if err != nil {
			log.Fatalf("%s: %v", name, err)
		}
		if *start != "" {
			login.Start = *start
		}
		if login.Channel == "" {
			login.Channel = "slgo"
		}

		opts := agent.Options{
			// A message nobody has registered for is how a
			// protocol change announces itself.
			OnUnhandled: func(p *msg.Packet) {
				if *verbose {
					log.Printf("%s: no handler for %s", name, p.ID)
				}
			},
			OnError: func(p *msg.Packet) {
				log.Printf("%s: undecodable packet: %v", name, p.Err)
			},
		}

		log.Printf("%s: logging in...", name)
		h, err := srv.Host(ctx, name, login, opts)
		if err != nil {
			log.Fatalf("%s: %v", name, err)
		}
		a := h.Agent
		log.Printf("%s: %s in %s, %d capabilities",
			name, a.Account.Name(), orUnknown(a.RegionName()), len(a.Caps))

		go func(name string, h *server.Hosted) {
			<-h.Agent.Done()
			if err := h.Agent.Err(); err != nil {
				log.Printf("%s: connection ended: %v", name, err)
			} else {
				log.Printf("%s: connection ended", name)
			}
		}(name, h)
	}

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("serving gRPC on %s for %s", ln.Addr(), strings.Join(srv.Names(), ", "))

	go func() {
		if err := srv.Serve(ctx, ln); err != nil {
			log.Printf("serve: %v", err)
		}
	}()

	<-ctx.Done()
	stop()
	log.Print("logging out...")

	shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	srv.Close(shutdown)
	log.Print("done")
}

func orUnknown(s string) string {
	if s == "" {
		return "an unnamed region"
	}
	return s
}
