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
// A character cell is taller than it is wide.  A grid drawn with as
// many columns as rows is therefore taller on the screen than it is
// wide, and a square region comes out looking like a doorway -- which
// matters, because the whole point of a picture is that the eye reads
// the shape without doing any arithmetic.  So a picture is drawn with
// more columns than rows, and each column covers fewer metres than a
// row does.  The numbers are said under the grid all the same, because
// "about square" is not a scale.
//
// How much taller than wide is a fact about somebody's font rather than
// about anything here: two to one was the guess, and the font this was
// measured in is seven high to three wide.  So it is the map_ratio
// setting -- see CellRatio, which says which way round it is written,
// and mapGrid.cols, which is the only place it is used.
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
// reads a region picture has to know that, so man/map.md says it and
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
// Every one of these is a setting -- map_rows, map_span, map_ratio,
// map_level, map_friend_colour -- and these are what an empty
// configuration file leaves them at.  See config.go.
//
// mapDefaultRows is a size that fits any terminal: 16 rows at the
// default ratio is 37 columns, 39 with the frame, so it sits inside an
// 80 column window beside everything else on the screen.
//
// mapDefaultSpan is a judgement rather than a measurement.  It is how
// many metres across the picture around the avatar covers, and 64 was
// chosen because it is comfortably more than the 20 metres ordinary
// chat carries and less than the draw distance usually is: the people
// in it are the people somebody is dealing with, and a picture that
// covered everything described would put them all in one cell in the
// middle.  With the default rows that is 4 metres to a row and about
// 1.7 to a column.
//
// mapDefaultRatio is a measurement, of the font this was written in.
// See CellRatio: it is height first, seven high to three wide.
//
// mapMaxRows is not a property of anything and is not a setting; it is
// a refusal to spend a terminal on a picture nobody could read, since
// 64 rows is already 149 columns wide.
const (
	mapDefaultRows = 16
	mapDefaultSpan = 64
	mapMaxRows     = 64
)

var mapDefaultRatio = CellRatio{Tall: 7, Wide: 3}

// mapRegionSize is how many metres a region is across, which is what
// --region covers and the one scale nobody gets to choose.
const mapRegionSize = 256

// mapDefaultLevel is how far above or below this avatar somebody can be
// and still be drawn as standing at the same height.
//
// About one storey.  An avatar is a couple of metres tall and a floor
// of a building is three or so, so within three metres is somebody who
// could be walked over to, and outside it is somebody on the roof or in
// the cellar.  Anything much larger and a skybox platform would read as
// level with the ground under it -- which is why it is three and not
// thirty, and why the number is said in the legend under every picture
// whatever it has been set to.
const mapDefaultLevel = 3

// mapColours is what a friend can be picked out of the picture in, by
// name, and mapColourOff puts the terminal back to whatever it was
// doing.
//
// # Why a name and not an escape sequence
//
// Nobody should have to write "\x1b[32m" into a configuration file to
// choose a colour, and a name is the thing that can be checked: a
// misspelled name is refused where it was typed, where an escape
// sequence somebody got wrong would be written into the middle of the
// picture and arrive as rubbish among the marks.  It is also the only
// spelling that survives being read back: "set" prints what the file
// would take, and an escape printed to a terminal is invisible.
//
// # Which colours
//
// The eight a terminal has had since it was a terminal, and the bright
// half of each for the terminals that have them, written "bright
// green".  Nothing here is a 256-colour index or an RGB triple: those
// are not colours every terminal has, and one that has them draws the
// eight by their own scheme anyway, which is what makes green mean
// green on somebody's own screen rather than a particular green.
//
// The foreground and not a block of colour behind the mark.  A
// background commits to one terminal's idea of paper -- a green slab is
// the only thing the eye sees on a dark terminal, and dark text on it
// is hard to read on a light one -- while coloured ink over whatever
// paper is already there is legible on both, and leaves the mark itself
// readable as the mark it is: an "o", a "^" and a count all still say
// what they said.
var mapColours = map[string]string{
	"black":          "\x1b[30m",
	"red":            "\x1b[31m",
	"green":          "\x1b[32m",
	"yellow":         "\x1b[33m",
	"blue":           "\x1b[34m",
	"magenta":        "\x1b[35m",
	"cyan":           "\x1b[36m",
	"white":          "\x1b[37m",
	"bright black":   "\x1b[90m",
	"bright red":     "\x1b[91m",
	"bright green":   "\x1b[92m",
	"bright yellow":  "\x1b[93m",
	"bright blue":    "\x1b[94m",
	"bright magenta": "\x1b[95m",
	"bright cyan":    "\x1b[96m",
	"bright white":   "\x1b[97m",
}

// mapDefaultFriendColour is the one a friend is drawn in unless
// somebody says otherwise.
//
// Green because it is what a friend has been drawn in since there was
// any colour in a picture at all, and because it is the one of the
// eight that is legible over both kinds of paper -- blue disappears
// into a dark terminal and yellow into a light one.  Nothing depends
// on it being green, which is the point of it being a setting.
const mapDefaultFriendColour = "green"

const mapColourOff = "\x1b[0m"

// ParseColour reads a colour by name, in whatever case and spacing
// somebody typed it: "Bright  Green" is the same colour as "bright
// green", since one of those came off a command line where the words
// were separate arguments.
func ParseColour(s string) (string, error) {
	name := strings.ToLower(strings.Join(strings.Fields(s), " "))
	if _, ok := mapColours[name]; !ok {
		return "", fmt.Errorf("no colour called %q; there is %s", s, colourNames())
	}
	return name, nil
}

// colourNames is the colours there are, for a refusal that has to say
// what it would have taken.
//
// The plain eight in the order a terminal numbers them, which is the
// order everybody has seen them in, and then "bright" said once rather
// than eight more names: sixteen names in a line is a paragraph, and
// the eight with a note under them is a sentence.
func colourNames() string {
	return "black, red, green, yellow, blue, magenta, cyan, white, " +
		"and \"bright\" before any of them"
}

// mapHighlight is who the picture picks out, and what colour it is
// allowed to do it in.
//
// Both are decided by the caller and neither is discovered here.  The
// drawing has to work the same whether it is going to a terminal, to a
// file or to a test's buffer, and a picture that reached for the
// environment half way down would be a different picture depending on
// who ran it.  See Shell.colour for what decides whether there is a
// colour at all, and cmdMap for where the ids and the colour come from.
//
// colour is the NAME of the colour, not the escape, so that the legend
// under the grid can say which colour it is talking about in the same
// breath as showing it.  Empty is a picture with no colour in it at
// all, which is a redirection, a pipe, or NO_COLOR.
type mapHighlight struct {
	friends map[msg.UUID]bool
	colour  string
}

// paint puts the friend colour round a piece of the picture, and hands
// back what it was given when the picture is not in colour.
func (hl mapHighlight) paint(s string) string {
	esc := mapColours[hl.colour]
	if esc == "" {
		return s
	}
	return esc + s + mapColourOff
}

// friendColour is the name of the colour a friend is drawn in:
// whatever map_friend_colour says.
//
// A Config that never went through LoadConfig -- a test's, mostly --
// names no colour at all, and the default is a better answer than a
// picture that silently stopped picking anybody out.  A name that is in
// no table cannot arrive here otherwise: both the file and "set" refuse
// one where it is typed.
func friendColour(cfg Config) string {
	if _, ok := mapColours[cfg.MapFriendColour]; ok {
		return cfg.MapFriendColour
	}
	return mapDefaultFriendColour
}

// mapOptions is what map was asked for.
//
// The two that have a setting behind them say so instead of naming a
// number: the number in the brackets would be the default's default,
// and would be wrong for anybody who had set one.
type mapOptions struct {
	Region bool `getopt:"--region -r     the whole region, rather than the ground around this avatar"`
	Rows   int  `getopt:"--rows=N       how many rows to draw, for this picture only [map_rows]"`
	Span   int  `getopt:"--span=METRES  how much ground the picture covers, without --region [map_span]"`
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

	// The flag for this one picture, the setting for every picture, and
	// the default when neither has been chosen -- which is a Config
	// that never went through LoadConfig, as a test's is.
	rows := firstSet(o.Rows, sh.cfg.MapRows, mapDefaultRows)
	if rows < 2 || rows > mapMaxRows {
		widest := mapGrid{rows: mapMaxRows, ratio: sh.cfg.MapRatio}.cols()
		return usageError("map", fmt.Sprintf(
			"--rows wants 2 to %d; %d rows is already %d columns wide",
			mapMaxRows, mapMaxRows, widest))
	}
	var span int
	switch {
	case o.Region && o.Span != 0:
		// Not ignored quietly.  A region is 256 metres and that is the
		// whole of the scale; somebody who typed both meant one of them
		// and should be told which one this would have thrown away.
		return usageError("map",
			"--region covers the region, which is 256 metres; --span is for the picture around the avatar")
	case o.Span == 0:
		span = firstSet(sh.cfg.MapSpan, mapDefaultSpan)
	case o.Span < 2:
		return usageError("map",
			"--span is metres across, and less than 2 is narrower than the avatar in the middle of it")
	default:
		span = o.Span
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
	hl := mapHighlight{}
	if sh.colour(out) {
		hl.colour = friendColour(sh.cfg)
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
		g = regionGrid(sh.cfg, rows)
		head = fmt.Sprintf("all of %s", positionLine(where))
	} else {
		g = aroundGrid(sh.cfg, rows, float32(span), where.Position)
		head = fmt.Sprintf("around you in %s", positionLine(where))
	}
	fmt.Fprintln(out, head)
	drawMap(out, g, where.Position, people, hl)
	return nil
}

// firstSet is the first of these numbers anybody actually chose.
//
// Which is how a flag, a setting and a default are read in that order:
// nought is what "nobody said" looks like for every one of them, since
// none of these means anything at nought -- no picture has no rows and
// none covers no ground.
func firstSet(ns ...int) int {
	for _, n := range ns {
		if n != 0 {
			return n
		}
	}
	return 0
}

// mapGrid is one picture: the square of the world it covers, how many
// rows of characters it is drawn in, the shape of a cell it is drawn
// with, and how far off this avatar's height still counts as level.
//
// The last two are settings and are carried here rather than read where
// they are used, so that the drawing stays a function of its arguments:
// a picture is checked by putting positions in and reading the exact
// characters out, and one that reached for a Config half way down could
// not be.
//
// The columns are not a field because they are not a choice: how many
// there are follows from the rows and the ratio, which is what makes
// the picture look square on a screen.  See cols.
type mapGrid struct {
	rows  int       // rows of characters
	span  float32   // metres the picture covers, the same each way
	west  float32   // the region metre at the left edge
	south float32   // the region metre at the bottom edge
	ratio CellRatio // the shape of a character cell, height first
	level int       // metres above or below that are still level
}

// gridFrom is the part of every picture that comes out of the settings.
//
// Anything nobody has set is filled in from the defaults, which is a
// Config that never went through LoadConfig -- a test's, mostly.  A
// ratio of nought by nought is a division by zero rather than a small
// mistake, and a level of nought would draw everybody as above or below
// this avatar and nobody as level with it.
func gridFrom(cfg Config) mapGrid {
	g := mapGrid{ratio: cfg.MapRatio, level: cfg.MapLevel}
	if g.ratio.Tall < 1 || g.ratio.Wide < 1 {
		g.ratio = mapDefaultRatio
	}
	if g.level < 1 {
		g.level = mapDefaultLevel
	}
	return g
}

// regionGrid is the picture of a whole region: the square is the region
// itself, so nothing but the number of rows is left to choose.
func regionGrid(cfg Config, rows int) mapGrid {
	g := gridFrom(cfg)
	g.rows, g.span = rows, mapRegionSize
	return g
}

// aroundGrid is the picture centred on this avatar, covering span
// metres each way.
func aroundGrid(cfg Config, rows int, span float32, me msg.Vector3) mapGrid {
	g := gridFrom(cfg)
	g.rows, g.span = rows, span
	g.west, g.south = me.X-span/2, me.Y-span/2
	return g
}

// cols is how many characters wide the picture is.
//
// As many as it takes for a square of the world to look square on the
// screen: a cell is ratio.Tall high and ratio.Wide across, so as many
// rows of it stack into the same height as rows*Tall/Wide of them fit
// across.  At the measured 7:3, sixteen rows is 37 columns.
//
// Rounded rather than truncated, because half a column cannot be drawn
// and the nearer whole number is the nearer shape.  At 7:3 sixteen rows
// want 37.3 columns, where rounding and truncating agree on 37; twenty
// want 46.7, where truncating draws a picture half a column too narrow
// and rounding draws the one that was asked for.
//
// The ratio is height against width and getting it the other way round
// draws a picture nobody can see is wrong.  See CellRatio.
func (g mapGrid) cols() int {
	r := g.ratio
	if r.Tall < 1 || r.Wide < 1 {
		// A grid built without going through the constructors, which is
		// a mistake in this package rather than anything anybody typed.
		// The default shape is a better answer than a division by zero.
		r = mapDefaultRatio
	}
	n := int(math.Round(float64(g.rows) * float64(r.Tall) / float64(r.Wide)))
	if n < 1 {
		n = 1
	}
	return n
}

// perCol and perRow are how many metres one character covers.  A column
// is less than a row because there are more of them across the same
// square.
func (g mapGrid) perCol() float32 { return g.span / float32(g.cols()) }
func (g mapGrid) perRow() float32 { return g.span / float32(g.rows) }

// width and height are how much ground the whole picture covers, which
// is what the line under it says.
//
// The same number twice today, since a picture is a square of the world
// however many rows it is drawn in -- that is what having more columns
// than rows is for.  They are worked out from the cells rather
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
		if p.Distance < 0 {
			// Seated on something undescribed: nowhere to draw them,
			// so they are named underneath like anybody out of the
			// picture, with a "?" for how far.
			outside = append(outside, p)
			continue
		}
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
			c.mark = g.markFor(me, p.Position)
		}
		if hl.friends[p.ID] {
			c.friend = true
		}
	}

	// The line naming whoever the picture does not reach is built
	// before anything is written, because whether there is any colour
	// in it is part of what decides whether the legend above it
	// explains what the colour means.
	outsideLine, outsideColoured := "", false
	if len(outside) > 0 {
		outsideLine, outsideColoured = namesOutside(outside, hl)
	}

	frame := "+" + strings.Repeat("-", g.cols()) + "+"
	fmt.Fprintln(out, frame)
	coloured := false
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
			case hl.colour != "" && c.friend:
				line.WriteString(hl.paint(string(cellMark(c))))
				coloured = true
			default:
				line.WriteByte(cellMark(c))
			}
		}
		line.WriteByte('|')
		// The shell's own writing, colour and all: nothing in a row of
		// the picture is anybody's name.  See writeOwn.
		writeOwn(out, line.String()+"\n")
	}
	fmt.Fprintln(out, frame)

	fmt.Fprintf(out, "%sm x %sm; north is up, east is right\n",
		tenth(g.width()), tenth(g.height()))
	fmt.Fprintf(out, "* you   o within %dm of your height   ^ higher   v lower\n", g.level)
	fmt.Fprintln(out, "a digit is that many in one cell, and + is more than nine")
	if coloured || outsideColoured {
		// Only when there is something coloured to explain.  A picture
		// with no colour in it -- a redirection, a pipe, an avatar
		// with no friend in sight -- must read exactly as it did
		// before there was any colour at all, and a line explaining a
		// colour to a file is nonsense.  The colour's own name is the
		// word said, and said in that colour, the way the lines above
		// it show the marks they are about rather than describing
		// them: what a person has to recognise in the grid is the
		// colour, and what they have to type to change it is the name.
		writeOwn(out, fmt.Sprintf("%s is somebody on your friend list\n", hl.paint(hl.colour)))
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
		line := fmt.Sprintf("%d outside it: %s\n", len(outside), outsideLine)
		if hl.colour != "" {
			// A picture in colour is on its way to the terminal, and
			// the names in this line were made visible where it was
			// built, which is what lets the colour round them through.
			// See namesOutside.
			writeOwn(out, line)
		} else {
			fmt.Fprint(out, line)
		}
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
// above it, or below it.  How much "level" is comes from the picture,
// which got it from map_level; see mapDefaultLevel for how much it is
// unless somebody says otherwise.
func (g mapGrid) markFor(me, them msg.Vector3) byte {
	level := float32(g.level)
	switch dz := them.Z - me.Z; {
	case dz > level:
		return '^'
	case dz < -level:
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
// A friend is coloured here too, and the second answer says whether any
// of them was.  Being outside the picture does not stop somebody being
// a friend, and this line is exactly where a person looks when the
// friend they wanted is not among the marks; leaving it plain would
// mean the same fact was worth a colour three lines higher up and not
// here.  The name is what is coloured and not the distance, since the
// distance is not what makes them a friend.
//
// In a picture in colour every name is made visible here, a friend's
// or not, because that line goes to the terminal by writeOwn and Print
// never sees it: a name is what somebody chose to be called, and it is
// the only part of the line a stranger wrote.  Without colour the line
// goes the way any other output does, and is left as it arrived.
func namesOutside(people []sl.Person, hl mapHighlight) (line string, coloured bool) {
	const most = 8
	parts := make([]string, 0, most+1)
	for i, p := range people {
		if i == most {
			parts = append(parts, fmt.Sprintf("and %d more", len(people)-most))
			break
		}
		name := p.Name
		if hl.colour != "" {
			name = visible(name)
		}
		if hl.colour != "" && hl.friends[p.ID] {
			name = hl.paint(name)
			coloured = true
		}
		if p.Distance < 0 {
			parts = append(parts, name+" ?")
			continue
		}
		parts = append(parts, fmt.Sprintf("%s %.0fm", name, p.Distance))
	}
	return strings.Join(parts, ", "), coloured
}
