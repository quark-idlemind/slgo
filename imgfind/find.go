// Package imgfind finds text and simple figures in an image.
//
// Find answers the questions a test asks of a screenshot: where
// is this label, where is this arrow, where is the outlined button.
// Each match carries the box (its top-left and its size), the center of
// that box, and either the text that was read or a short description of
// the figure. Coordinates are pixels with the origin at the top-left of
// the image, which is the same space a click uses.
//
// A face of an object is a picture too: sl.Session.FacePicture builds
// the one a face shows, and Find reads it. This package imports nothing
// from slgo.
//
// Text is read by the tesseract binary on PATH. Figures are read from
// the pixels, so a request that only asks for drawings or boxes does
// not need tesseract.
//
// # Experimental
//
// This package is experimental: not covered by slgo's compatibility
// promise, and it may change in any release.
package imgfind

import (
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"regexp"
	"sort"
	"strings"
)

// Kind says which thing a Request is looking for.
type Kind int

const (
	// KindText is an exact string, such as "Menu". It matches a
	// recognized word, or several words on one line joined by spaces
	// ("Example Menu"). The match is case-sensitive.
	KindText Kind = iota
	// KindDrawing is a simple figure named by Drawing: "circle",
	// "arrow", "left arrow", "right arrow", "up arrow", "down arrow".
	KindDrawing
	// KindBox is a region drawn as an outline around a lighter
	// interior, such as a button.
	KindBox
	// KindPattern is a regular expression applied to the same words
	// and lines KindText sees. The expression is not anchored.
	KindPattern
)

// Request is one thing to look for. A single request may match several
// times; each returned Item points back at the Request that produced it.
type Request struct {
	Kind Kind

	// Text is the exact text for KindText.
	Text string
	// Drawing names the figure for KindDrawing.
	Drawing string
	// Pattern is the regular expression for KindPattern.
	Pattern string
}

// Text returns a request for the exact string s.
func Text(s string) *Request { return &Request{Kind: KindText, Text: s} }

// Drawing returns a request for a simple figure.
//
// name is "circle", "arrow", or a direction ("left arrow", "right
// arrow", "up arrow", "down arrow"). "arrow" matches every direction.
// Matching ignores case and extra spaces.
func Drawing(name string) *Request { return &Request{Kind: KindDrawing, Drawing: name} }

// Box returns a request for every outlined box.
func Box() *Request { return &Request{Kind: KindBox} }

// Pattern returns a request for text matching expr.
func Pattern(expr string) *Request { return &Request{Kind: KindPattern, Pattern: expr} }

// Point is a pixel position. X increases to the right and Y increases
// downward.
type Point struct {
	X, Y int
}

// Item is one match. Contents is the text that was read, or a short
// description of a figure ("left arrow", "circle", "outlined box").
// Location is the top-left of the match, Width and Height are its size,
// and Center is the middle of that box.
type Item struct {
	Request  *Request
	Contents string
	Location Point
	Width    int
	Height   int
	Center   Point
}

// Find looks for each request in img.
//
// A request that matches nothing contributes no items and is not an
// error. The result is grouped by request, in the order the requests
// were given, and within a request from top to bottom and then left to
// right. The image is read once, however many requests there are.
func Find(img image.Image, reqs ...*Request) ([]*Item, error) {
	prepared, err := prepare(reqs)
	if err != nil {
		return nil, err
	}
	if img == nil || img.Bounds().Empty() {
		return nil, fmt.Errorf("imgfind: empty image")
	}

	var texts []span
	if prepared.needText {
		texts, err = readText(img)
		if err != nil {
			return nil, err
		}
	}
	var figures []figure
	if prepared.needFigure {
		figures = readFigures(img)
	}

	var out []*Item
	for i, req := range reqs {
		found := match(req, prepared.res[i], texts, figures)
		sortItems(found)
		out = append(out, found...)
	}
	return out, nil
}

// FindFile decodes the PNG, JPEG or GIF at path and calls Find on it.
// The requests are checked before the file is opened.
func FindFile(path string, reqs ...*Request) ([]*Item, error) {
	if _, err := prepare(reqs); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("imgfind: %w", err)
	}
	img, _, err := image.Decode(f)
	f.Close()
	if err != nil {
		return nil, fmt.Errorf("imgfind: %s: %w", path, err)
	}
	return Find(img, reqs...)
}

// prepared is the compiled form of the requests. res[i] belongs to reqs[i].
type prepared struct {
	needText   bool
	needFigure bool
	res        []*regexp.Regexp
}

func prepare(reqs []*Request) (prepared, error) {
	var p prepared
	p.res = make([]*regexp.Regexp, len(reqs))
	for i, req := range reqs {
		if req == nil {
			return prepared{}, fmt.Errorf("imgfind: nil request")
		}
		switch req.Kind {
		case KindText:
			if strings.TrimSpace(req.Text) == "" {
				return prepared{}, fmt.Errorf("imgfind: text request is empty")
			}
			p.needText = true
		case KindPattern:
			re, err := regexp.Compile(req.Pattern)
			if err != nil {
				return prepared{}, fmt.Errorf("imgfind: pattern %q: %w", req.Pattern, err)
			}
			p.res[i] = re
			p.needText = true
		case KindDrawing:
			if _, err := canonDrawing(req.Drawing); err != nil {
				return prepared{}, err
			}
			p.needFigure = true
		case KindBox:
			p.needFigure = true
		default:
			return prepared{}, fmt.Errorf("imgfind: unknown request kind %d", req.Kind)
		}
	}
	return p, nil
}

func match(req *Request, re *regexp.Regexp, texts []span, figures []figure) []*Item {
	switch req.Kind {
	case KindText:
		q := strings.TrimSpace(req.Text)
		var out []*Item
		for _, t := range texts {
			if t.text == q {
				out = append(out, item(req, t.text, t.x, t.y, t.w, t.h))
			}
		}
		return out
	case KindPattern:
		var out []*Item
		for _, t := range texts {
			if re.MatchString(t.text) {
				out = append(out, item(req, t.text, t.x, t.y, t.w, t.h))
			}
		}
		return out
	case KindDrawing:
		want, _ := canonDrawing(req.Drawing)
		var out []*Item
		for _, f := range figures {
			if f.kind != figCircle && f.kind != figArrow {
				continue
			}
			if !drawingMatches(want, f) {
				continue
			}
			out = append(out, item(req, f.contents, f.x, f.y, f.w, f.h))
		}
		return out
	case KindBox:
		var out []*Item
		for _, f := range figures {
			if f.kind == figBox {
				out = append(out, item(req, f.contents, f.x, f.y, f.w, f.h))
			}
		}
		return out
	default:
		return nil
	}
}

func drawingMatches(want string, f figure) bool {
	switch want {
	case "circle":
		return f.kind == figCircle
	case "arrow":
		return f.kind == figArrow
	default:
		return f.kind == figArrow && f.contents == want
	}
}

func canonDrawing(name string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(name))
	s = strings.ReplaceAll(s, "-", " ")
	s = strings.Join(strings.Fields(s), " ")
	switch s {
	case "circle", "circles", "ring":
		return "circle", nil
	case "arrow", "arrows":
		return "arrow", nil
	case "left arrow", "arrow left", "leftarrow":
		return "left arrow", nil
	case "right arrow", "arrow right", "rightarrow":
		return "right arrow", nil
	case "up arrow", "arrow up", "uparrow":
		return "up arrow", nil
	case "down arrow", "arrow down", "downarrow":
		return "down arrow", nil
	default:
		return "", fmt.Errorf("imgfind: unknown drawing %q (known: circle, arrow, left arrow, right arrow, up arrow, down arrow)", name)
	}
}

func item(req *Request, contents string, x, y, w, h int) *Item {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	return &Item{
		Request:  req,
		Contents: contents,
		Location: Point{X: x, Y: y},
		Width:    w,
		Height:   h,
		Center:   Point{X: x + w/2, Y: y + h/2},
	}
}

func sortItems(items []*Item) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if d := a.Location.Y - b.Location.Y; d > 8 || d < -8 {
			return a.Location.Y < b.Location.Y
		}
		if a.Location.X != b.Location.X {
			return a.Location.X < b.Location.X
		}
		return a.Contents < b.Contents
	})
}
