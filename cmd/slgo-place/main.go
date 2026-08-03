// Command slgo-place moves a rezzed object to a position.
//
// It exists because taking an object and rezzing it again does not put
// it back: a rez happens where you ask, and "where it was" is not
// something Second Life remembers for you. Anything that takes an object
// as part of a round trip has to note where it stood and put it back.
//
//	slgo-place -object Box1 -at 24.511482,76.04561,999.5
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/world"
)

func main() {
	server := flag.String("server", "127.0.0.1:7807", "slgod to attach to")
	agent := flag.String("agent", "example", "which hosted agent")
	object := flag.String("object", "", "object to move, by name")
	at := flag.String("at", "", "where to put it, x,y,z in region coordinates")
	desc := flag.String("desc", "", "also set the description; \"-\" clears it")
	flag.Parse()

	if *object == "" || *at == "" {
		die("usage: slgo-place -object NAME -at x,y,z")
	}
	pos, err := parseVec(*at)
	if err != nil {
		die("%v", err)
	}

	ctx := context.Background()
	w, err := world.Dial(ctx, *server, *agent)
	if err != nil {
		die("attach: %v", err)
	}
	defer w.Close()

	found, err := w.ObjectsNamed(ctx, *object, 60*time.Second)
	if err != nil || len(found) == 0 {
		die("no object named %q: %v", *object, err)
	}
	o := found[0]
	fmt.Printf("found %s at %v\n", o.Name, o.Position)

	// Its own rotation and scale, so that moving it changes only where
	// it is. Place sets all three, and inventing the other two would
	// quietly reshape whatever it was pointed at.
	if err := w.Place(ctx, &o.Object, pos, o.Rotation, o.Scale); err != nil {
		die("place: %v", err)
	}
	if *desc != "" {
		text := *desc
		if text == "-" {
			text = ""
		}
		if err := w.SetDescription(ctx, &o.Object, text); err != nil {
			die("description: %v", err)
		}
	}

	props, err := w.Properties(ctx, &o.Object, 20*time.Second)
	if err != nil {
		die("properties: %v", err)
	}
	again, err := w.ObjectByID(ctx, o.ID, 20*time.Second)
	if err != nil {
		die("re-reading it: %v", err)
	}
	fmt.Printf("now   %s at %v, description %q\n", again.Name, again.Position, props.Description)
}

func parseVec(s string) (msg.Vector3, error) {
	f := strings.Split(strings.Trim(strings.TrimSpace(s), "<>"), ",")
	if len(f) != 3 {
		return msg.Vector3{}, fmt.Errorf("cannot read %q as a position: want x,y,z", s)
	}
	var v [3]float64
	for i := range f {
		n, err := strconv.ParseFloat(strings.TrimSpace(f[i]), 32)
		if err != nil {
			return msg.Vector3{}, fmt.Errorf("cannot read %q as a position: %w", s, err)
		}
		v[i] = n
	}
	return msg.Vector3{X: float32(v[0]), Y: float32(v[1]), Z: float32(v[2])}, nil
}

func die(format string, v ...any) {
	fmt.Fprintf(os.Stderr, "slgo-place: "+format+"\n", v...)
	os.Exit(1)
}
