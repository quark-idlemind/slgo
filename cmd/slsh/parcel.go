package main

// The land under the avatar.
//
// A region is one place to the protocol and several to everybody
// standing in it: what decides whether an avatar may build, fly, run a
// script or stay at all is the parcel, and "where" can only say the
// region.  This is the other half of where you are.
//
// Three sources, and the command says which one each answer came from,
// because they answer differently often enough that a person needs to
// know:
//
//   - Asking.  The simulator answers a question about any parcel in the
//     region in about a tenth of a second, and that is what "parcel"
//     and "parcel X,Y" do.
//   - The push.  Whatever held the session when the avatar arrived was
//     told what it landed on.  That is what the command falls back to
//     when asking gets no answer.
//   - The overlay.  Four packets on arrival say who owns every 4 metre
//     square of the region, with no names at all -- and cannot be asked
//     for again, so the session that heard them is the only source.
//     --region and --map read it.
//
// See doc/history/parcel.md for the measurements, including the
// afternoon spent believing the grid answered none of this.

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
)

type parcelOptions struct {
	Region bool `getopt:"--region -r  every parcel the overlay can see, counted"`
	Map    bool `getopt:"--map -m     draw the region's parcels, as \"map\" draws"`
	Rows   int  `getopt:"--rows=N     how many rows to draw, with --map [map_rows]"`
	Help   bool `getopt:"--help -h    show what this command takes"`
}

// parcelClasses are the overlay's ownership bits in the words a person
// would use, and the character --map draws each as.
var parcelClasses = []struct {
	bits byte
	mark byte
	name string
}{
	{agent.OverlayPublic, '.', "public"},
	{agent.OverlayOwned, 'o', "owned"},
	{agent.OverlayGroup, 'g', "group"},
	{agent.OverlaySelf, '@', "yours"},
	{agent.OverlayForSale, '$', "for sale"},
	{agent.OverlayAuction, 'a', "at auction"},
}

func parcelClass(b byte) (mark byte, name string) {
	for _, c := range parcelClasses {
		if b&agent.OverlayOwnerMask == c.bits {
			return c.mark, c.name
		}
	}
	return '?', fmt.Sprintf("owner bits %d", b&agent.OverlayOwnerMask)
}

// cmdParcel describes a parcel, or draws the region's parcels.
func cmdParcel(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o parcelOptions
	args, done, err := subOptions("parcel", &o, out, args)
	if err != nil || done {
		return err
	}
	if o.Region && o.Map {
		return usageError("parcel", "--region counts the parcels and --map draws them; ask for one")
	}
	if (o.Region || o.Map) && len(args) > 0 {
		return usageError("parcel",
			"--region and --map are about the whole region, so they take no point")
	}
	if o.Rows != 0 && !o.Map {
		return usageError("parcel", "--rows is for the picture --map draws")
	}
	if len(args) > 1 {
		return usageError("parcel", "one point, as X,Y")
	}

	switch {
	case o.Region:
		return parcelRegion(ctx, sh, out)
	case o.Map:
		return parcelMap(ctx, sh, out, o.Rows, sh.colour(out))
	case len(args) == 1:
		x, y, err := parcelPoint(args[0])
		if err != nil {
			return err
		}
		p, err := sh.s.ParcelAt(ctx, x, y, 0)
		if err != nil {
			return err
		}
		printParcel(ctx, sh, out, p, fmt.Sprintf("the parcel at %g, %g", x, y))
		return nil
	}

	p, err := sh.s.Parcel(ctx, 0)
	if err != nil {
		// The ask is the current answer and the push is the remembered
		// one, so a failure here is worth something rather than
		// nothing: say what the session was told and say that is what
		// it is.
		l, lerr := sh.s.Land(ctx)
		if lerr != nil || l.Told == nil {
			return err
		}
		fmt.Fprintf(out, "%s\n", orUnknown(l.Told.Name, "a parcel with no name"))
		fmt.Fprintf(out, "  local    %d\n", l.Told.LocalID)
		fmt.Fprintf(out, "\nthis is what the session was told when it arrived; asking now got no answer (%v)\n", err)
		return nil
	}
	printParcel(ctx, sh, out, p, "")
	return nil
}

// parcelPoint reads an X,Y written as the shell writes positions.
func parcelPoint(s string) (x, y float32, err error) {
	f := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' })
	if len(f) != 2 {
		return 0, 0, usageError("parcel", "a point is X,Y in metres from the region's south west corner")
	}
	if _, err := fmt.Sscanf(f[0]+" "+f[1], "%g %g", &x, &y); err != nil {
		return 0, 0, usageError("parcel", "a point is X,Y in metres from the region's south west corner")
	}
	if x < 0 || y < 0 || x >= 256 || y >= 256 {
		return 0, 0, usageError("parcel", "a point is inside the region: 0 to 256 each way")
	}
	return x, y, nil
}

// printParcel is the description, in the shape "look" prints a region.
func printParcel(ctx context.Context, sh *Shell, out io.Writer, p *agent.Parcel, what string) {
	fmt.Fprintf(out, "%s\n", orUnknown(p.Name, "a parcel with no name"))
	if what != "" {
		fmt.Fprintf(out, "  asked    %s\n", what)
	}
	fmt.Fprintf(out, "  local    %d\n", p.LocalID)

	owner := p.Owner.String()
	if p.IsGroupOwned {
		owner += "  (owned by the group)"
	} else if names := sh.s.Names(ctx, []msg.UUID{p.Owner}, 3*time.Second); names[p.Owner] != "" {
		owner += "  " + names[p.Owner]
	}
	fmt.Fprintf(out, "  owner    %s\n", owner)

	if !p.Group.IsZero() {
		fmt.Fprintf(out, "  group    %s%s\n", p.Group, parcelGroupName(ctx, sh, p.Group))
	}

	area := fmt.Sprintf("  area     %d m²", p.Area)
	if p.AABBMax != (msg.Vector3{}) {
		area += fmt.Sprintf(", within %g,%g to %g,%g",
			p.AABBMin.X, p.AABBMin.Y, p.AABBMax.X, p.AABBMax.Y)
	}
	fmt.Fprintln(out, area)

	// Prims are why building stops working, so the counts are spelled
	// out rather than summed: "486 of 937" is the answer to "may I
	// build", and the breakdown is the answer to "why not".
	fmt.Fprintf(out, "  prims    %d of %d  (owner %d, group %d, other %d, selected %d)\n",
		p.TotalPrims, p.MaxPrims, p.OwnerPrims, p.GroupPrims, p.OtherPrims, p.SelectedPrims)
	if p.SimWideMaxPrims > 0 && p.SimWideMaxPrims != p.MaxPrims {
		fmt.Fprintf(out, "  region   %d of %d prims across the whole region\n",
			p.SimWideTotalPrims, p.SimWideMaxPrims)
	}

	if allowed := parcelAllows(p.Flags); allowed != "" {
		fmt.Fprintf(out, "  allows   %s\n", allowed)
	}
	if refused := parcelRefuses(p.Flags); refused != "" {
		fmt.Fprintf(out, "  refuses  %s\n", refused)
	}

	if p.Flags&agent.ParcelForSale != 0 {
		fmt.Fprintf(out, "  sale     L$%d\n", p.SalePrice)
	}
	if p.PassPrice > 0 {
		hours := "hours"
		if p.PassHours == 1 {
			hours = "hour"
		}
		fmt.Fprintf(out, "  pass     L$%d for %g %s\n", p.PassPrice, p.PassHours, hours)
	}
	if !p.Claimed.IsZero() {
		fmt.Fprintf(out, "  claimed  %s\n", p.Claimed.Format("2006-01-02"))
	}
	if p.MusicURL != "" {
		fmt.Fprintf(out, "  music    %s\n", p.MusicURL)
	}
	if p.MediaURL != "" {
		fmt.Fprintf(out, "  media    %s\n", p.MediaURL)
	}
	if p.Desc != "" {
		fmt.Fprintf(out, "  about    %s\n", indented(p.Desc))
	}
}

// parcelGroupName names the group where this avatar is in it.
//
// Only then: a group's name comes with the session's own memberships
// and there is no cheap question for anybody else's, so the alternative
// to the id alone is a question that usually goes unanswered.
func parcelGroupName(ctx context.Context, sh *Shell, id msg.UUID) string {
	p, err := sh.s.Where(ctx)
	if err != nil || p == nil {
		return ""
	}
	for _, g := range p.Groups {
		if g.ID == id {
			return "  " + g.Name
		}
	}
	return ""
}

// parcelAllows and parcelRefuses put the flags word into words.
//
// Two lines rather than one with "no" in it, because the question a
// person has is one or the other: "may I build here" is answered by
// reading one list, and a list of twenty entries half of them negated
// is answered by reading all of it.
func parcelAllows(flags uint32) string {
	return parcelFlagList(flags, true)
}

func parcelRefuses(flags uint32) string {
	return parcelFlagList(flags, false)
}

func parcelFlagList(flags uint32, want bool) string {
	var out []string
	for _, f := range []struct {
		bit  uint32
		name string
	}{
		{agent.ParcelCreateObjects, "build"},
		{agent.ParcelCreateGroupObjects, "group build"},
		{agent.ParcelAllowFly, "fly"},
		{agent.ParcelAllowAllObjectEntry, "object entry"},
		{agent.ParcelAllowGroupObjectEntry, "group object entry"},
		{agent.ParcelAllowOtherScripts, "scripts"},
		{agent.ParcelAllowGroupScripts, "group scripts"},
		{agent.ParcelAllowLandmark, "landmarks"},
		{agent.ParcelAllowTerraform, "terraform"},
		{agent.ParcelAllowDamage, "damage"},
		{agent.ParcelAllowVoiceChat, "voice"},
		{agent.ParcelShowDirectory, "search listing"},
	} {
		if (flags&f.bit != 0) == want {
			out = append(out, f.name)
		}
	}
	return strings.Join(out, ", ")
}

// parcelRegion lists the region's parcels: the overlay for the shapes,
// and one question apiece for the names.
//
// The shapes come from the boundaries -- a square belongs to the parcel
// on its west unless a property line runs between them, and the same
// northward -- so the pieces are what the lines cut the region into.
// The overlay carries no names at all, so each piece is then asked
// about by a point inside it, which is a tenth of a second each and the
// only way there is.
//
// Asking also corrects the count.  One parcel can be two pieces of
// ground -- land bought either side of a road is still one parcel --
// and nothing in the overlay says so, where the local id the answers
// come back with says it plainly.
func parcelRegion(ctx context.Context, sh *Shell, out io.Writer) error {
	o, err := parcelOverlay(ctx, sh, out)
	if err != nil || o == nil {
		return err
	}

	pieces, _ := overlayPieces(o)
	if len(pieces) == 0 {
		fmt.Fprintln(out, "the overlay says nothing about this region")
		return nil
	}

	named, _, unanswered := nameThePieces(ctx, sh, pieces)
	sort.Slice(named, func(i, j int) bool { return named[i].squares > named[j].squares })

	what := fmt.Sprintf("%d parcels", len(named))
	if len(named) != len(pieces) {
		what += fmt.Sprintf(", in %d pieces the boundaries cut apart", len(pieces))
	}
	fmt.Fprintf(out, "%s\n", what)

	// The name column is as wide as the names, so that the columns
	// after it line up: a fixed width picked here would be too narrow
	// for the region that has a parcel called "Protected Land - Rez
	// zone" in it, and that region is Pelmar Reach.
	width := 0
	for _, n := range named {
		if len(n.name) > width {
			width = len(n.name)
		}
	}
	if width < 12 {
		width = 12
	}

	for i, n := range named {
		if i >= 30 {
			fmt.Fprintf(out, "  ... and %d more\n", len(named)-i)
			break
		}
		_, class := parcelClass(n.bits)
		name := n.name
		if name == "" {
			name = "(no answer)"
		}
		fmt.Fprintf(out, "  %6d m²  %-*s  %-11s at %d,%d\n",
			n.squares*agent.OverlayStep*agent.OverlayStep, width, name, class,
			n.x*agent.OverlayStep, n.y*agent.OverlayStep)
	}

	if unanswered > 0 {
		fmt.Fprintf(out, "\n%d of them went unanswered, and are listed by their shape alone\n", unanswered)
	}
	if !o.Complete() {
		fmt.Fprintf(out, "\n%d of the overlay's 4 packets arrived, so part of the region is missing\n",
			o.Packets())
	}
	return nil
}

// namedPiece is a piece of ground with whatever the grid called it.
type namedPiece struct {
	piece
	name  string
	local int32
}

// nameThePieces asks about a point inside each piece and merges the
// pieces that turn out to be one parcel.
//
// Sequential on purpose.  Fourteen questions at a tenth of a second is
// a second and a half, which is a command doing something rather than a
// command hanging; a burst of fourteen at once is a client that looks
// like something worth throttling.
//
// It gives up after three unanswered in a row.  A region that is not
// answering will not start, and thirty timeouts at two seconds each is
// a minute of a person waiting to be told nothing.
func nameThePieces(ctx context.Context, sh *Shell, pieces []piece) (
	named []namedPiece, of []int, unanswered int) {

	const giveUpAfter = 3
	byLocal := map[int32]int{} // local id to its place in named
	misses := 0
	of = make([]int, len(pieces))

	for i, p := range pieces {
		n := namedPiece{piece: p}

		if misses < giveUpAfter {
			// The middle of a square that is certainly inside the
			// piece: the scan found it there, where a corner of the
			// bounding box may be on somebody else's land.
			x := float32(p.x*agent.OverlayStep) + agent.OverlayStep/2
			y := float32(p.y*agent.OverlayStep) + agent.OverlayStep/2
			if got, err := sh.s.ParcelAt(ctx, x, y, 2*time.Second); err == nil {
				n.name, n.local = got.Name, got.LocalID
				misses = 0
			} else {
				misses++
				unanswered++
			}
		} else {
			unanswered++
		}

		// Two pieces with one local id are one parcel with a road
		// through it.  The squares add up; the position kept is the
		// first, which is as good as either.
		if n.local != 0 {
			if at, ok := byLocal[n.local]; ok {
				named[at].squares += n.squares
				of[i] = at
				continue
			}
			byLocal[n.local] = len(named)
		}
		of[i] = len(named)
		named = append(named, n)
	}
	return named, of, unanswered
}

// piece is one connected run of squares: a parcel as far as the overlay
// can tell, before anything has been asked.
type piece struct {
	squares int
	bits    byte

	// x and y are a square that is certainly inside the piece, in
	// overlay squares, and are what a question about it is asked at.
	// Deliberately not the corner of a bounding box: the two smallest
	// coordinates of an L shaped parcel meet on somebody else's land,
	// and a question asked there is answered about them.
	x, y int
}

// overlayPieces cuts the region up along the property lines.
//
// A square's west bit means a line runs along ITS west edge, so it is
// the wall between it and the square to the west; the south bit is the
// same southward.  Nothing is a wall on the far side, which is why the
// two directions are asked of different squares.
func overlayPieces(o *agent.Overlay) ([]piece, []int) {
	const edge = agent.OverlayEdge
	sq := o.Squares()
	seen := make([]bool, len(sq))
	at := func(x, y int) byte { return sq[y*edge+x] }

	// label is which piece each square ended up in, so that a picture
	// can colour the ground by parcel rather than by ownership.  A
	// square in a quarter that never arrived stays -1.
	label := make([]int, len(sq))
	for i := range label {
		label[i] = -1
	}

	var out []piece
	for y := 0; y < edge; y++ {
		for x := 0; x < edge; x++ {
			if seen[y*edge+x] {
				continue
			}
			// A square in a quarter that never arrived is not a
			// parcel, it is a hole, and counting holes as public land
			// would invent parcels nobody owns.
			if _, ok := o.At(float32(x*agent.OverlayStep), float32(y*agent.OverlayStep)); !ok {
				seen[y*edge+x] = true
				continue
			}

			p := piece{bits: at(x, y), x: x, y: y}
			stack := []int{y*edge + x}
			seen[y*edge+x] = true
			for len(stack) > 0 {
				i := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				p.squares++
				label[i] = len(out)
				cx, cy := i%edge, i/edge

				push := func(nx, ny int) {
					if nx < 0 || ny < 0 || nx >= edge || ny >= edge {
						return
					}
					j := ny*edge + nx
					if seen[j] {
						return
					}
					seen[j] = true
					stack = append(stack, j)
				}
				if at(cx, cy)&agent.OverlayWestLine == 0 {
					push(cx-1, cy)
				}
				if cx+1 < edge && at(cx+1, cy)&agent.OverlayWestLine == 0 {
					push(cx+1, cy)
				}
				if at(cx, cy)&agent.OverlaySouthLine == 0 {
					push(cx, cy-1)
				}
				if cy+1 < edge && at(cx, cy+1)&agent.OverlaySouthLine == 0 {
					push(cx, cy+1)
				}
			}
			out = append(out, p)
		}
	}
	return out, label
}

// parcelMarks are the characters a parcel can be drawn as, and
// parcelInks the colours they can be drawn in.
//
// Letters, because the mark has to survive the colour being gone: a
// picture down a pipe, in a test, or on a terminal somebody has set
// NO_COLOR on has nothing but the shape left, and a picture that was
// only ever telling parcels apart by colour would then be a wash of one
// character.  Colour is what makes them easy to tell apart at a glance;
// the letter is what makes them possible to tell apart at all.
//
// Colour is the foreground of the letter and never a block behind it,
// and the eight-and-bright names rather than a 256-colour index, as in
// mapColours.  Black and white are left out of the rotation: one of
// them is invisible on each kind of paper.
// Why: doc/slsh.md#the-colours-a-map-is-drawn-in
const parcelMarks = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

var parcelInks = []string{
	"green", "yellow", "blue", "magenta", "cyan", "red",
	"bright green", "bright yellow", "bright blue",
	"bright magenta", "bright cyan", "bright red",
}

// parcelDraw is how one parcel is drawn: a mark, and the colour it is
// painted in where there is colour.
type parcelDraw struct {
	mark byte
	ink  string
}

// parcelDraws decides a mark for each parcel.
//
// The letters and the colours turn over at different rates -- 52 and 12
// -- so two parcels are drawn alike only after 156 of them, where a
// region holds dozens.  Neighbouring parcels differ in both.
//
// Linden's protected land is the exception, and it is drawn as ground
// rather than as a parcel: blank for the roads and waterways a region
// is laid out around, and "." for the rez zones inside them, so that
// the loudest marks are left for the homes.  It is recognised by its
// name, not by its owner.
// Why: doc/slsh.md#protected-land-drawn-as-ground
func parcelDraws(named []namedPiece) []parcelDraw {
	out := make([]parcelDraw, len(named))
	n := 0
	for i, p := range named {
		switch protected, rez := protectedLand(p.name); {
		case protected && rez:
			out[i] = parcelDraw{mark: '.'}
		case protected:
			out[i] = parcelDraw{mark: ' '}
		default:
			out[i] = parcelDraw{
				mark: parcelMarks[n%len(parcelMarks)],
				ink:  parcelInks[n%len(parcelInks)],
			}
			n++
		}
	}
	return out
}

// protectedLand reads a parcel's name for Linden's own land, and
// whether it is one of the rez zones inside it.
func protectedLand(name string) (protected, rez bool) {
	lower := strings.ToLower(name)
	if !strings.HasPrefix(lower, "protected land") {
		return false, false
	}
	return true, strings.Contains(lower, "rez")
}

// parcelMap draws the region's parcels, in the shape "map" draws a
// region: one character to a cell, a different one for each parcel.
//
// What is drawn is the pieces the overlay's boundaries cut the region
// into, each with its own mark, and the key under it names them: which
// parcel is which is what a map of parcels is for, and not who owns it.
// Why: doc/slsh.md#drawing-parcels-rather-than-ownership
//
// Whether there is colour is decided by the caller and not discovered
// here, for the reason mapHighlight gives: a picture that reached for
// the terminal half way down would be a different picture depending on
// who ran it.
func parcelMap(ctx context.Context, sh *Shell, out io.Writer, rows int, colour bool) error {
	o, err := parcelOverlay(ctx, sh, out)
	if err != nil || o == nil {
		return err
	}

	pieces, label := overlayPieces(o)
	if len(pieces) == 0 {
		fmt.Fprintln(out, "the overlay says nothing about this region")
		return nil
	}
	named, of, unanswered := nameThePieces(ctx, sh, pieces)

	rows = firstSet(rows, sh.cfg.MapRows, mapDefaultRows)
	if rows < 2 || rows > mapMaxRows {
		return usageError("parcel", fmt.Sprintf("--rows wants 2 to %d", mapMaxRows))
	}
	g := regionGrid(sh.cfg, rows)
	draws := parcelDraws(named)

	drawn := map[int]bool{}
	for row := 0; row < g.rows; row++ {
		var line strings.Builder
		for col := 0; col < g.cols(); col++ {
			// The cell, in region metres.  North is up, so the top row
			// is the far edge.
			x0 := float32(col) * g.perCol()
			y1 := g.span - float32(row)*g.perRow()

			// Which parcel holds most of the cell.  A cell is several
			// squares across, and taking only the middle one would
			// lose a parcel narrower than a cell -- which is most of a
			// road.
			at := cellParcel(o, label, of, x0, x0+g.perCol(), y1-g.perRow(), y1)
			if at < 0 {
				line.WriteByte('?')
				continue
			}
			drawn[at] = true
			d := draws[at]
			if colour && d.ink != "" {
				line.WriteString(mapColours[d.ink] + string(d.mark) + mapColourOff)
			} else {
				line.WriteByte(d.mark)
			}
		}
		// Marks and their colours and nothing else, so the shell's own
		// writing.  See writeOwn.
		writeOwn(out, "  "+line.String()+"\n")
	}

	fmt.Fprintf(out, "\n%.0f metres across, %d parcels%s\n",
		g.width(), len(named), protectedNote(named))
	for i, n := range named {
		if !drawn[i] {
			// Too small to have won a cell.  Said rather than dropped:
			// a parcel missing from a picture and missing from its key
			// is a parcel a person concludes is not there.
			continue
		}
		d := draws[i]
		shown := string(d.mark)
		if colour && d.ink != "" {
			shown = mapColours[d.ink] + shown + mapColourOff
		}
		name := n.name
		if name == "" {
			name = "(no answer)"
		}
		if colour {
			// A parcel's name is whatever its owner typed, and here it
			// shares a line with the colour of its mark, so it is made
			// visible now and the line goes out by writeOwn rather than
			// through Print.
			name = visible(name)
		}
		_, class := parcelClass(n.bits)
		line := fmt.Sprintf("  %s  %-*s  %6d m²  %s\n", shown, parcelWidth(named), name,
			n.squares*agent.OverlayStep*agent.OverlayStep, class)
		if colour {
			writeOwn(out, line)
		} else {
			fmt.Fprint(out, line)
		}
	}
	if small := len(named) - len(drawn); small > 0 {
		fmt.Fprintf(out, "  and %d too small to draw at %d rows\n", small, g.rows)
	}
	if unanswered > 0 {
		fmt.Fprintf(out, "\n%d went unnamed: the overlay has their shape and nothing answered about them\n", unanswered)
	}
	if !o.Complete() {
		fmt.Fprintf(out, "\n%d of the overlay's 4 packets arrived, so ? is a quarter that never came\n",
			o.Packets())
	}
	return nil
}

// protectedNote explains the two quiet marks, and only where the
// picture has them in it: a legend for a mark nobody can see is a line
// of noise.
func protectedNote(named []namedPiece) string {
	var blank, rez bool
	for _, p := range named {
		if protected, isRez := protectedLand(p.name); protected {
			blank = blank || !isRez
			rez = rez || isRez
		}
	}
	switch {
	case blank && rez:
		return "; protected land is blank, its rez zones \".\""
	case blank:
		return "; protected land is blank"
	case rez:
		return "; a protected rez zone is \".\""
	}
	return ""
}

// parcelWidth is how wide the name column has to be.
func parcelWidth(named []namedPiece) int {
	width := 12
	for _, n := range named {
		if len(n.name) > width {
			width = len(n.name)
		}
	}
	return width
}

// cellParcel is the parcel holding most of one cell of the picture, or
// -1 for a cell nothing has described.
func cellParcel(o *agent.Overlay, label, of []int, west, east, south, north float32) int {
	count := map[int]int{}
	best, bestN := -1, 0
	for y := south; y < north; y += agent.OverlayStep {
		for x := west; x < east; x += agent.OverlayStep {
			if _, ok := o.At(x, y); !ok {
				continue
			}
			i := int(y)/agent.OverlayStep*agent.OverlayEdge + int(x)/agent.OverlayStep
			if i < 0 || i >= len(label) || label[i] < 0 {
				continue
			}
			at := of[label[i]]
			count[at]++
			if count[at] > bestN {
				best, bestN = at, count[at]
			}
		}
	}
	return best
}

// parcelOverlay fetches the overlay and says so when there is none.
//
// A missing overlay is not a failure and is worth explaining: it means
// this session was not there when the avatar arrived, and no request
// will bring it back -- only arriving somewhere will.
func parcelOverlay(ctx context.Context, sh *Shell, out io.Writer) (*agent.Overlay, error) {
	l, err := sh.s.Land(ctx)
	if err != nil {
		return nil, err
	}
	if l.Overlay == nil || l.Overlay.Packets() == 0 {
		fmt.Fprintln(out, "nothing has described this region's parcels")
		fmt.Fprintln(out, "the overlay arrives on arrival and cannot be asked for; teleporting here again would fetch it")
		return nil, nil
	}
	return l.Overlay, nil
}
