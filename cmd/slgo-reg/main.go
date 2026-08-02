// Command slgo-reg talks to the register attachment directly, to find
// out whether it is listening at all.
package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"slgo/world"
)

func main() {
	server := flag.String("server", "127.0.0.1:7807", "slgod")
	agent := flag.String("agent", "example", "hosted agent")
	say := flag.String("say", "count", "what to send on the channel")
	channel := flag.Int("channel", -1701, "channel")
	flag.Parse()

	ctx := context.Background()
	w, err := world.Dial(ctx, *server, *agent)
	if err != nil {
		fmt.Println("attach:", err)
		return
	}
	defer w.Close()

	for _, a := range w.Attachments() {
		fmt.Printf("attached: %-28s point %d  %s\n", a.Object.Name, a.Point, a.Object.ID)
	}

	lines := w.Chat(world.ChatFilter{}, 64)
	defer w.StopChat(lines)

	if err := w.Say(ctx, *say, int32(*channel)); err != nil {
		fmt.Println("say:", err)
		return
	}
	fmt.Printf("said %q on %d; listening 15s for anything at all\n", *say, *channel)

	deadline := time.After(15 * time.Second)
	for {
		select {
		case l := <-lines:
			fmt.Printf("  heard [type %d from %s] %q\n", l.Type, l.Source, l.Text)
		case <-deadline:
			fmt.Println("(done)")
			return
		}
	}
}
