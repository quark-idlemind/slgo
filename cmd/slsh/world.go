package main

// What is outside inventory: where the avatar is, who else is there,
// and what the simulator will tell you about itself.

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

var worldCommands = map[string]*command{
	"where": {
		usage: "where",
		brief: "the region and position this avatar is at",
		run:   cmdWhere,
	},
	"who": {
		usage: "who",
		brief: "who else is in the region, nearest first",
		run:   cmdWho,
	},
	"look": {
		usage: "look",
		brief: "what the simulator said about the region",
		run:   cmdLook,
	},
	"caps": {
		usage: "caps [TEXT]",
		brief: "the capabilities this session was granted",
		run:   cmdCaps,
	},
	"features": {
		usage: "features [TEXT]",
		brief: "what the simulator says it supports",
		run:   cmdFeatures,
	},
	"lsl": {
		usage: "lsl [TEXT]",
		brief: "the LSL this simulator implements: functions, constants, events",
		run:   cmdLSL,
	},
	"objects": {
		usage: "objects [TEXT]",
		brief: "the objects the region has described, by name",
		run:   cmdObjects,
	},
}

func cmdWhere(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	p, err := sh.s.Where(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%s at %.0f, %.0f, %.0f\n", p.Region, p.Position.X, p.Position.Y, p.Position.Z)
	if !p.ActiveGroup.IsZero() {
		fmt.Fprintf(out, "acting as group %s\n", p.ActiveGroup)
	}
	return nil
}

func cmdWho(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	ps, err := sh.s.Nearby(ctx)
	if err != nil {
		return err
	}
	if len(ps) == 0 {
		fmt.Fprintln(out, "nobody else is in range")
		return nil
	}
	listed := make([]person, 0, len(ps))
	for i, p := range ps {
		fmt.Fprintf(out, "%2d  %-32s %6.1fm  %s\n", i+1, p.Name, p.Distance, p.ID)
		listed = append(listed, person{ID: p.ID, Name: p.Name})
	}
	sh.setListed(listed)
	return nil
}

func cmdLook(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	r, err := sh.s.Region(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%s\n", r.Name)
	fmt.Fprintf(out, "  id       %s\n", r.ID)
	fmt.Fprintf(out, "  owner    %s\n", r.Owner)
	fmt.Fprintf(out, "  access   %d\n", r.Access)
	fmt.Fprintf(out, "  water    %.1fm\n", r.WaterHeight)
	fmt.Fprintf(out, "  product  %s\n", r.ProductName)
	if n, err := sh.s.Known(ctx); err == nil {
		fmt.Fprintf(out, "  objects  %d described so far\n", n)
	}
	return nil
}

func cmdCaps(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	want := ""
	if len(args) > 0 {
		want = strings.ToLower(args[0])
	}
	caps := append([]string(nil), sh.s.Info().Caps...)
	sort.Strings(caps)
	for _, c := range caps {
		if want == "" || strings.Contains(strings.ToLower(c), want) {
			fmt.Fprintln(out, c)
		}
	}
	return nil
}

func cmdFeatures(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	f, err := sh.s.Features(ctx)
	if err != nil {
		return err
	}
	want := ""
	if len(args) > 0 {
		want = strings.ToLower(args[0])
	}
	for _, n := range f.Names() {
		if want != "" && !strings.Contains(strings.ToLower(n), want) {
			continue
		}
		switch v := f.Raw[n].(type) {
		case map[string]any:
			keys := make([]string, 0, len(v))
			for k := range v {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			fmt.Fprintf(out, "%-30s %v\n", n, keys)
		default:
			fmt.Fprintf(out, "%-30s %v\n", n, v)
		}
	}
	return nil
}

func cmdLSL(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	s, err := sh.s.LSLSyntax(ctx)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		fmt.Fprintf(out, "version %d: %d functions, %d constants, %d events, %d types\n",
			s.Version, len(s.Functions), len(s.Constants), len(s.Events), len(s.Types))
		return nil
	}

	want := strings.ToLower(args[0])
	for _, n := range s.FunctionNames() {
		if !strings.Contains(strings.ToLower(n), want) {
			continue
		}
		f := s.Functions[n]
		line := f.Signature()
		if f.Energy != 0 || f.Sleep != 0 {
			line += fmt.Sprintf("   energy %g, sleep %g", f.Energy, f.Sleep)
		}
		if f.Deprecated {
			line += "   [deprecated]"
		}
		fmt.Fprintln(out, line)
	}
	for _, n := range s.ConstantNames() {
		if !strings.Contains(strings.ToLower(n), want) {
			continue
		}
		c := s.Constants[n]
		fmt.Fprintf(out, "%s %s = %s\n", c.Type, c.Name, c.Value)
	}
	for _, n := range s.EventNames() {
		if !strings.Contains(strings.ToLower(n), want) {
			continue
		}
		e := s.Events[n]
		var as []string
		for _, a := range e.Arguments {
			as = append(as, a.Type+" "+a.Name)
		}
		fmt.Fprintf(out, "%s(%s)\n", e.Name, strings.Join(as, ", "))
	}
	return nil
}

func cmdObjects(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	all, err := sh.s.AllObjects(ctx, 30*time.Second)
	if err != nil {
		return err
	}
	want := ""
	if len(args) > 0 {
		want = strings.ToLower(args[0])
	}
	n := 0
	for _, o := range all {
		if o.IsAvatar() {
			continue
		}
		if want != "" && !strings.Contains(strings.ToLower(o.Name), want) {
			continue
		}
		fmt.Fprintf(out, "%-36s %-28s %.0f, %.0f, %.0f\n",
			o.ID, o.Name, o.Position.X, o.Position.Y, o.Position.Z)
		n++
	}
	if n == 0 {
		fmt.Fprintln(out, "nothing matched")
	}
	return nil
}
