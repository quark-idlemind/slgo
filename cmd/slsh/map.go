package main

// Where everybody is, as a picture rather than as a column of metres.
//
// "who" answers how far away somebody is and says nothing at all about
// which way, which is the half of the question a person standing in a
// region actually has: three people at 20 metres are a crowd in one
// direction and three people scattered around the avatar in another,
// and the listing reads the same either way.  So this draws them.
//
// # Why a row is not a column
//
// A character cell in every terminal font is about twice as tall as it
// is wide.  A grid drawn with as many columns as rows is therefore
// twice as tall on the screen as it is wide, and a square region comes
// out looking like a doorway -- which matters, because the whole point
// of a picture is that the eye reads the shape without doing any
// arithmetic.  So a picture is drawn with twice as many columns as
// rows, and each column covers half the metres a row does.  The
// numbers are said under the grid all the same, because "about square"
// is not a scale.
//
// # Which positions these are
//
// The precise ones, out of the object updates the simulator sent for
// each avatar, by way of sl.Nearby.  The other source of avatar
// positions is CoarseLocationUpdate, which is what a viewer's mini-map
// is drawn from, and its height is a byte of four-metre steps that
// saturates: 255 means "higher than this can say" and not 1020.  See
// the handler in agent/agent.go for what that does to a session that
// believes it.  A picture that marks somebody as level with this avatar
// when they are in a skybox is worse than no picture, so the heights
// here are the ones that were described in full.
//
// The price is that only the avatars this session has been told about
// can be drawn, which is the draw distance and not the region.  Whoever
// reads a region picture has to know that, so man/map.txt says it and
// the count under the grid says how many are in the picture.

import (
	"context"
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// The shape and scale of a picture nobody has said anything about.
//
// mapRows is a size that fits any terminal: 16 rows is 34 columns of
// characters with the frame, so it sits inside an 80 column window
// beside everything else on the screen.
//
// mapSpan is a judgement rather than a measurement.  It is how many
// metres across the picture around the avatar covers, and 64 was
// chosen because it is comfortably more than the 20 metres ordinary
// chat carries and less than the draw distance usually is: the people
// in it are the people somebody is dealing with, and a picture that
// covered everything described would put them all in one cell in the
// middle.  With the default rows that is 4 metres to a row and 2 to a
// column.
//
// mapMaxRows is not a property of anything; it is a refusal to spend a
// terminal on a picture nobody could read, since 64 rows is already 130
// columns wide.
const (
	mapRows    = 16
	mapSpan    = 64
	mapMaxRows = 64
)

// mapRegionSize is how many metres a region is across, which is what
// --region covers and the one scale nobody gets to choose.
const mapRegionSize = 256

// mapLevel is how far above or below this avatar somebody can be and
// still be drawn as standing at the same height.
//
// About one storey.  An avatar is a couple of metres tall and a floor
// of a building is three or so, so within three metres is somebody who
// could be walked over to, and outside it is somebody on the roof or in
// the cellar.  Anything much larger and a skybox platform would read as
// level with the ground under it.
const mapLevel = 3

// mapFriendColour is what a friend is picked out of the picture in,
// and mapColourOff puts the terminal back to whatever it was doing.
//
// Green in the foreground rather than a block of it behind the mark.  A
// background commits to one terminal's idea of paper -- a green slab is
// the only thing the eye sees on a dark terminal, and dark text on it
// is hard to read on a light one -- while green ink over whatever paper
// is already there is legible on both, and leaves the mark itself
// readable as the mark it is: an "o", a "^" and a count all still say
// what they said.
//
// It is one named constant because the next thing anybody will want is
// to choose it, and that should be a line changed here rather than a
// hunt through the drawing.  When it does become a choice it belongs
// in Config, beside the prefix key: the escape is read out of the
// configuration file, cmdMap puts it in the mapHighlight where the
// bool is now, and paint uses that instead of this.  Nothing else in
// the picture has to move.
const (
	mapFriendColour = "\x1b[32m"
	mapColourOff    = "\x1b[0m"
)

// mapHighlight is who the picture picks out, and whether it is allowed
// to use colour to do it.
//
// Both are decided by the caller and neither is discovered here.  The
// drawing has to work the same whether it is going to a terminal, to a
// file or to a test's buffer, and a picture that reached for the
// environment half way down would be a different picture depending on
// who ran it.  See Shell.colour for what decides the second, and
// cmdMap for where the ids come from.
type mapHighlight struct {
	friends map[msg.UUID]bool
	colour  bool
}

// paint puts the friend colour round a piece of the picture, and hands
// back what it was given when the picture is not in colour.
func (hl mapHighlight) paint(s string) string {
	if !hl.colour {
		return s
	}
	return mapFriendColour + s + mapColourOff
}

// mapOptions is what map was asked for.
type mapOptions struct {
	Region bool `getopt:"--region -r     the whole region, rather than the ground around this avatar"`
	Rows   int  `getopt:"--rows=N       how many rows to draw; there are twice as many columns [16]"`
	Span   int  `getopt:"--span=METRES  how much ground the picture covers, without --region [64]"`
	Help   bool `getopt:"--help -h      show what this command takes"`
}

// cmdMap draws the avatars around this one.
//
// The two views are the same picture over a different square of the
// world: --region covers the region, which is 256 metres and settles
// the scale by itself, and without it the picture is a square of --span
// metres centred on this avatar.
//
// Nothing here is numbered, and the last listing is deliberately left
// alone: "im 2" still means the second line of whatever who or friends
// printed.  A picture cannot be numbered usefully -- two people in one
// cell are one character -- and quietly replacing the numbering with
// one that has no numbers in it would break the command somebody typed
// next.
func cmdMap(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o mapOptions
	args, done, err := subOptions("map", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) > 0 {
		return usageError("map", "it draws what is around this avatar and takes no argument")
	}

	rows := o.Rows
	if rows == 0 {
		rows = mapRows
	}
	if rows < 2 || rows > mapMaxRows {
		return usageError("map", fmt.Sprintf(
			"--rows wants 2 to %d; the picture is twice as many columns wide", mapMaxRows))
	}
	span := o.Span
	switch {
	case o.Region && span != 0:
		// Not ignored quietly.  A region is 256 metres and that is the
		// whole of the scale; somebody who typed both meant one of them
		// and should be told which one this would have thrown away.
		return usageError("map",
			"--region covers the region, which is 256 metres; --span is for the picture around the avatar")
	case span == 0:
		span = mapSpan
	case span < 2:
		return usageError("map",
			"--span is metres across, and less than 2 is narrower than the avatar in the middle of it")
	}

	where, err := sh.s.Where(ctx)
	if err != nil {
		return err
	}
	people, err := sh.s.Nearby(ctx)
	if err != nil {
		return err
	}

	// Who to pick out, and whether there is anywhere to pick them out
	// in.  The friend list is only asked for when there is: it is a
	// call to whoever holds the session, and a picture on its way to a
	// file has no use for the answer.
	hl := mapHighlight{colour: sh.colour(out)}
	if hl.colour {
		ids, err := sh.s.FriendIDs(ctx)
		switch {
		case err != nil:
			// Not fatal, because the picture is worth having without
			// the colour.  Said out loud all the same: a picture with
			// no green in it otherwise reads as a picture with no
			// friends in it, and that is a different fact.
			sh.errorf("map: no friend list, so nobody is picked out: %v", err)
		case len(ids) > 0:
			hl.friends = make(map[msg.UUID]bool, len(ids))
			for _, id := range ids {
				hl.friends[id] = true
			}
		}
	}

	// The heading says which of the two pictures this is and where the
	// avatar is standing, in the wording "where" and tp both use: a
	// grid of characters with no region named over it is a picture of
	// somewhere.  How big it is belongs under the grid rather than
	// here, and is not said twice.
	var g mapGrid
	var head string
	if o.Region {
		g = regionGrid(rows)
		head = fmt.Sprintf("all of %s", positionLine(where))
	} else {
		g = aroundGrid(rows, float32(span), where.Position)
		head = fmt.Sprintf("around you in %s", positionLine(where))
	}
	fmt.Fprintln(out, head)
	drawMap(out, g, where.Position, people, hl)
	return nil
}

// mapGrid is one picture: the square of the region it covers, and how
// many rows of characters it is drawn in.
//
// The columns are not a field because they are not a choice: there are
// twice as many of them as there are rows, which is what makes the
// picture look square on a screen.  See the head of this file.
type mapGrid struct {
	rows  int     // rows of characters
	span  float32 // metres the picture covers, the same each way
	west  float32 // the region metre at the left edge
	south float32 // the region metre at the bottom edge
}

// regionGrid is the picture of a whole region: the square is the region
// itself, so nothing but the number of rows is left to choose.
func regionGrid(rows int) mapGrid {
	return mapGrid{rows: rows, span: mapRegionSize}
}

// aroundGrid is the picture centred on this avatar, covering span
// metres each way.
func aroundGrid(rows int, span float32, me msg.Vector3) mapGrid {
	return mapGrid{
		rows:  rows,
		span:  span,
		west:  me.X - span/2,
		south: me.Y - span/2,
	}
}

func (g mapGrid) cols() int { return 2 * g.rows }

// perCol and perRow are how many metres one character covers.  A column
// is half a row because there are twice as many of them across the same
// square.
func (g mapGrid) perCol() float32 { return g.span / float32(g.cols()) }
func (g mapGrid) perRow() float32 { return g.span / float32(g.rows) }

// width and height are how much ground the whole picture covers, which
// is what the line under it says.
//
// The same number twice today, since a picture is a square of the world
// however many rows it is drawn in -- that is what having twice as many
// columns as rows is for.  They are worked out from the cells rather
// than printed as the span, so that the line stays true if the shape
// ever stops being square, and so that the two halves of the arithmetic
// are checked against each other every time it is drawn.
func (g mapGrid) width() float32  { return g.perCol() * float32(g.cols()) }
func (g mapGrid) height() float32 { return g.perRow() * float32(g.rows) }

// at is the cell a position falls in, and false for a position the
// picture does not cover.
//
// Row zero is the top, because north is up: y grows northward in a
// region and a row number grows downward on a screen, so the one is the
// other turned over.  Getting this backwards draws a picture that is
// wrong in the one way nobody can see by looking at it.
//
// Outside is a real answer and not an error.  Clamping to the edge
// would draw somebody at the edge of the picture, which is a claim
// about where they are, and it would be false.
func (g mapGrid) at(p msg.Vector3) (col, row int, in bool) {
	col = int(math.Floor(float64((p.X - g.west) / g.perCol())))
	fromSouth := int(math.Floor(float64((p.Y - g.south) / g.perRow())))
	row = g.rows - 1 - fromSouth
	if col < 0 || col >= g.cols() || row < 0 || row >= g.rows {
		return 0, 0, false
	}
	return col, row, true
}

// mapCell is what one character of the grid has in it: how many people,
// and the mark for the one there is when there is only one.
//
// friend is true when anybody standing in the cell is on the friend
// list, which is a fact about the cell rather than about whichever of
// them the mark is for.  A cell with two people in it is drawn as a
// count, and "there is a friend in that cell" is both true and the
// thing somebody looking for one wants said.
type mapCell struct {
	n      int
	mark   byte
	friend bool
}

// drawMap writes the picture and the lines that make it readable.
//
// It takes the grid, this avatar's position, everybody else's, and who
// is to be picked out; it writes to out and nothing else -- no session,
// no clock, no terminal, no environment -- because this is the part
// that is easy to get subtly wrong and impossible to check by eye in a
// live region, and a function of its arguments is a function a table of
// positions can be run through.
func drawMap(out io.Writer, g mapGrid, me msg.Vector3, people []sl.Person, hl mapHighlight) {
	cells := make([]mapCell, g.rows*g.cols())
	meCol, meRow, meIn := g.at(me)

	var outside []sl.Person
	drawn, under := 0, 0
	for _, p := range people {
		col, row, in := g.at(p.Position)
		if !in {
			outside = append(outside, p)
			continue
		}
		drawn++
		if meIn && col == meCol && row == meRow {
			// The star wins its own cell: a picture whose reader
			// cannot find themselves in it is not a picture of
			// anywhere.  Whoever is standing there is still counted,
			// and said aloud underneath, because a person hidden by a
			// mark is exactly the person somebody is looking for.
			under++
			continue
		}
		c := &cells[row*g.cols()+col]
		c.n++
		if c.n == 1 {
			c.mark = markFor(me, p.Position)
		}
		if hl.friends[p.ID] {
			c.friend = true
		}
	}

	// The line naming whoever the picture does not reach is built
	// before anything is written, because whether there is any green
	// in it is part of what decides whether the legend above it
	// explains what green means.
	outsideLine, outsideGreen := "", false
	if len(outside) > 0 {
		outsideLine, outsideGreen = namesOutside(outside, hl)
	}

	frame := "+" + strings.Repeat("-", g.cols()) + "+"
	fmt.Fprintln(out, frame)
	green := false
	var line strings.Builder
	for row := 0; row < g.rows; row++ {
		line.Reset()
		line.WriteByte('|')
		for col := 0; col < g.cols(); col++ {
			c := cells[row*g.cols()+col]
			switch {
			case meIn && row == meRow && col == meCol:
				// The star is never coloured, however much company it
				// has.  Two highlights in one picture are no highlight
				// at all, and this avatar is the one thing in the
				// picture nobody has to look for.
				line.WriteByte('*')
			case hl.colour && c.friend:
				line.WriteString(hl.paint(string(cellMark(c))))
				green = true
			default:
				line.WriteByte(cellMark(c))
			}
		}
		line.WriteByte('|')
		fmt.Fprintln(out, line.String())
	}
	fmt.Fprintln(out, frame)

	fmt.Fprintf(out, "%sm x %sm; north is up, east is right\n",
		tenth(g.width()), tenth(g.height()))
	fmt.Fprintf(out, "* you   o within %dm of your height   ^ higher   v lower\n", mapLevel)
	fmt.Fprintln(out, "a digit is that many in one cell, and + is more than nine")
	if green || outsideGreen {
		// Only when there is something green to explain.  A picture
		// with no colour in it -- a redirection, a pipe, an avatar
		// with no friend in sight -- must read exactly as it did
		// before there was any colour at all, and a line explaining a
		// colour to a file is nonsense.  The word is said in the
		// colour it is about, the way the lines above it show the
		// marks they are about rather than describing them.
		fmt.Fprintf(out, "%s is somebody on your friend list\n", hl.paint("green"))
	}

	if !meIn {
		// Only reachable in the region view, and only for an avatar the
		// session believes is outside the region it is in.  Saying so
		// is better than a picture with no star, which reads as a
		// drawing bug.
		fmt.Fprintln(out, "you are outside the picture, so there is no * in it")
	}
	if drawn == 0 && len(outside) == 0 {
		// The same words "who" uses for the same state, so that the
		// two commands do not describe one empty region two ways.
		fmt.Fprintln(out, "nobody else is in range")
	} else {
		fmt.Fprintf(out, "%d %s in the picture\n", drawn, plural(drawn, "avatar", "avatars"))
	}
	if under > 0 {
		fmt.Fprintf(out, "%d of them %s where you are, under the *\n",
			under, plural(under, "is", "are"))
	}
	if len(outside) > 0 {
		fmt.Fprintf(out, "%d outside it: %s\n", len(outside), outsideLine)
	}
}

// cellMark is the character one cell of the grid is drawn as.
//
// A count for more than one, because a picture that drew the first of
// them and dropped the rest would be hiding people in the very place
// somebody is looking; and a "+" past nine, because there is no digit
// for ten and a 9 there would be a lie.  Both are said in the legend
// under the grid.
func cellMark(c mapCell) byte {
	switch {
	case c.n == 0:
		return ' '
	case c.n == 1:
		return c.mark
	case c.n <= 9:
		return byte('0' + c.n)
	default:
		return '+'
	}
}

// markFor is what one other avatar is drawn as: level with this one,
// above it, or below it.  See mapLevel for how much "level" is.
func markFor(me, them msg.Vector3) byte {
	switch dz := them.Z - me.Z; {
	case dz > mapLevel:
		return '^'
	case dz < -mapLevel:
		return 'v'
	default:
		return 'o'
	}
}

// namesOutside is the line for the people the picture does not reach.
//
// With the distance, because "outside it" is only half an answer: the
// question anybody has about somebody who is not in the picture is
// whether they are just beyond the edge or on the other side of the
// region.  The list is in the order sl.Nearby hands them over, which is
// nearest first.
//
// It stops at eight names.  A region with forty avatars in it and a
// picture 64 metres across would otherwise print the whole of "who"
// under the grid, and "who" is the command for that.
//
// A friend is green here too, and the second answer says whether any
// of them was.  Being outside the picture does not stop somebody being
// a friend, and this line is exactly where a person looks when the
// friend they wanted is not among the marks; leaving it plain would
// mean the same fact was worth a colour three lines higher up and not
// here.  The name is what is coloured and not the distance, since the
// distance is not what makes them a friend.
func namesOutside(people []sl.Person, hl mapHighlight) (line string, green bool) {
	const most = 8
	parts := make([]string, 0, most+1)
	for i, p := range people {
		if i == most {
			parts = append(parts, fmt.Sprintf("and %d more", len(people)-most))
			break
		}
		name := p.Name
		if hl.colour && hl.friends[p.ID] {
			name = hl.paint(name)
			green = true
		}
		parts = append(parts, fmt.Sprintf("%s %.0fm", name, p.Distance))
	}
	return strings.Join(parts, ", "), green
}
