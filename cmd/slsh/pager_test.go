package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
)

func twentyLines() string {
	var b strings.Builder
	for i := 0; i < 20; i++ {
		if i == 12 {
			b.WriteString("the needle is here\n")
			continue
		}
		b.WriteString("line padding\n")
	}
	return b.String()
}

func TestPagerFitsOnAShortPage(t *testing.T) {
	p := newPager("one\ntwo\nthree", 24)
	if !p.fits() {
		t.Fatal("three lines should fit on 24 rows")
	}
}

func TestPagerSpaceMovesAScreenAndLeavesAtTheEnd(t *testing.T) {
	p := newPager(twentyLines(), 6) // view 5
	if p.view != 5 {
		t.Fatalf("view = %d, want 5", p.view)
	}
	if p.forward(p.view) {
		t.Fatal("first screen should not be the end")
	}
	if p.top != 5 {
		t.Errorf("top = %d, want 5", p.top)
	}
	p.forward(p.view)
	p.forward(p.view)
	if p.top != p.maxTop() {
		t.Errorf("top = %d, want max %d", p.top, p.maxTop())
	}
	if !p.forward(p.view) {
		t.Fatal("space on the last screen should leave")
	}
}

func TestPagerPGoesHomeAndBGoesBack(t *testing.T) {
	p := newPager(twentyLines(), 6)
	p.forward(p.view)
	p.forward(p.view)
	p.back(p.view)
	if p.top != 5 {
		t.Errorf("back a screen: top = %d, want 5", p.top)
	}
	p.home()
	if p.top != 0 {
		t.Errorf("p: top = %d, want 0", p.top)
	}
	p.back(p.view)
	if p.top != 0 {
		t.Errorf("back at the top moved: top = %d", p.top)
	}
}

func TestPagerHalfSteps(t *testing.T) {
	p := newPager(twentyLines(), 6) // half of 5 is 2
	p.forward(p.half())
	if p.top != 2 {
		t.Errorf("d: top = %d, want 2", p.top)
	}
	p.back(p.half())
	if p.top != 0 {
		t.Errorf("u: top = %d, want 0", p.top)
	}
}

func TestPagerSearchFindsTheNextAndPreviousLine(t *testing.T) {
	p := newPager(twentyLines(), 6)
	if !p.search("needle", 1) {
		t.Fatal("forward search missed the needle")
	}
	if got := p.lines[12]; !strings.Contains(got, "needle") {
		t.Fatalf("line 12 = %q", got)
	}
	if p.top != 12 && p.top != p.maxTop() {
		// 12 may be past maxTop (20-5=15? 20 lines, view 5, maxTop 15)
		// 12 < 15 so top should be 12
		t.Errorf("top = %d, want 12", p.top)
	}
	p.home()
	if p.search("needle", -1) {
		t.Fatal("backward from the top should find nothing")
	}
	p.top = 19
	if !p.search("needle", -1) {
		t.Fatal("backward search missed the needle")
	}
}

func TestPagerSearchRepeatsTheLastQuery(t *testing.T) {
	p := newPager("alpha\nbeta\nalpha again\ngamma\nalpha last\n", 4)
	if !p.search("alpha", 1) {
		t.Fatal("first alpha")
	}
	first := p.top
	if first != 2 {
		t.Errorf("first hit at %d, want 2 (the next alpha, not the one on screen)", first)
	}
	if !p.search("", 1) {
		t.Fatal("repeat should find the next alpha")
	}
	// The last alpha is on the last screen, which starts at 2.
	if p.top != 2 || p.hit != 4 {
		t.Errorf("repeat at top %d, hit %d, want the last screen (2) and 4", p.top, p.hit)
	}
}

// TestPagerSearchStopsAtTheLastScreen: a match near the end brings the
// last screenful, never less than one, and the next search goes on
// from the match rather than finding it again.
func TestPagerSearchStopsAtTheLastScreen(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 20; i++ {
		if i == 17 || i == 18 {
			b.WriteString("needle\n")
		} else {
			b.WriteString("hay\n")
		}
	}
	p := newPager(b.String(), 6) // view 5, so the last screen starts at 15
	if !p.search("needle", 1) {
		t.Fatal("the first needle was missed")
	}
	if p.top != p.maxTop() {
		t.Errorf("top = %d, want the last screen, %d", p.top, p.maxTop())
	}
	if p.status() != "(END)" {
		t.Errorf("status = %q, want (END)", p.status())
	}
	if !p.search("", 1) || p.hit != 18 || p.top != p.maxTop() {
		t.Errorf("the second needle: hit %d, top %d", p.hit, p.top)
	}
	if p.search("", 1) {
		t.Errorf("a third search found line %d; there are two needles", p.hit)
	}
	if !p.search("", -1) || p.hit != 17 {
		t.Errorf("searching back from the second found line %d, want 17", p.hit)
	}
}

func TestPagerSearchIgnoresANSI(t *testing.T) {
	p := newPager("plain\n\x1b[1mbold needle\x1b[0m\nplain\n", 4)
	if !p.search("needle", 1) {
		t.Fatal("search should see through SGR")
	}
}

func TestPagerNFollowsTheLastSearchDirection(t *testing.T) {
	p := newPager("alpha\nbeta\nalpha again\ngamma\nalpha last\n", 4)
	p.dir = 1
	p.query = "alpha"
	if !p.search("", p.dir) {
		t.Fatal("n forward")
	}
	if p.top != 2 {
		t.Errorf("n after / at %d, want 2", p.top)
	}
	p.dir = -1
	if !p.search("", p.dir) {
		t.Fatal("n backward")
	}
	if p.top != 0 {
		t.Errorf("n after ? at %d, want 0", p.top)
	}
}

func TestPagerPercentIsTheBottomLine(t *testing.T) {
	p := newPager(twentyLines(), 6) // 20 lines, view 5
	if got := p.percent(); got != 25 {
		t.Errorf("first screen percent = %d, want 25 (5 of 20)", got)
	}
	p.top = 5
	if got := p.percent(); got != 50 {
		t.Errorf("second screen percent = %d, want 50 (10 of 20)", got)
	}
	p.top = p.maxTop()
	if got := p.percent(); got != 100 {
		t.Errorf("last screen percent = %d, want 100", got)
	}
	if p.status() != "(END)" {
		t.Errorf("last screen status = %q, want (END)", p.status())
	}
}

func TestPagerSearchEmptyQueryWithNoPreviousFails(t *testing.T) {
	p := newPager(twentyLines(), 6)
	if p.search("", 1) {
		t.Fatal("empty search with no previous query should fail")
	}
}

// numberedLines is text whose every line says which line it is, so
// that a line printed twice can be told from a line printed once.
func numberedLines(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "line %02d of the page\n", i)
	}
	return b.String()
}

// pageWithKeys runs the pager over text, pressing keys, and answers
// with everything it wrote.
func pageWithKeys(t *testing.T, text, keyed string) string {
	t.Helper()
	var out strings.Builder
	keys := make(chan rune, 64)
	tm := &Term{
		out:    &out,
		keys:   keys,
		done:   make(chan struct{}),
		width:  40,
		height: 6, // view 5
		busy:   true,
	}
	for _, r := range keyed {
		keys <- r
	}
	close(keys)
	if err := page(context.Background(), tm, text); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

// A pager that clears the screen takes the scrollback with it: what was
// on the terminal before the page began is gone, and so is the page
// itself once it has been read.  This one appends, so it has no use for
// an erase or for a cursor moved to a row of its choosing, and the way
// to keep it that way is to say so here.
func TestPagerNeverErasesTheScreenOrMovesTheCursorUpIt(t *testing.T) {
	// Enough keys to move every way it moves: forward, half forward,
	// back, half back, home, a search, and out.
	out := pageWithKeys(t, numberedLines(40), " db u p/line 07\r q")
	for _, bad := range []struct {
		what string
		re   *regexp.Regexp
	}{
		{"an erase of the screen", regexp.MustCompile(`\x1b\[[0-9;]*J`)},
		{"a cursor moved to a row", regexp.MustCompile(`\x1b\[[0-9;]*[Hf]`)},
		{"a scroll region", regexp.MustCompile(`\x1b\[[0-9;]*r`)},
		{"the alternate screen", regexp.MustCompile(`\x1b\[\?104[59][hl]`)},
	} {
		if loc := bad.re.FindStringIndex(out); loc != nil {
			t.Errorf("the pager wrote %s: %q", bad.what,
				strings.ReplaceAll(out[loc[0]:loc[1]], "\x1b", "ESC"))
		}
	}
}

// Appending means a line printed twice stays printed twice, where a
// pager that repaints would have covered the first copy over.  Reading
// straight through must therefore print each line exactly once.
func TestPagingForwardPrintsEachLineOnce(t *testing.T) {
	const n = 20
	out := stripANSI(pageWithKeys(t, numberedLines(n), "    ")) // four screenfuls
	for i := 0; i < n; i++ {
		line := fmt.Sprintf("line %02d of the page", i)
		if got := strings.Count(out, line); got != 1 {
			t.Errorf("%q printed %d times, want 1:\n%s", line, got, out)
		}
	}
}

// A half-screen step prints half a screen.  more(1) does this, and the
// alternative here is worse than untidy: the half still on the screen
// would be printed under itself and stay there.
func TestAHalfStepPrintsOnlyTheNewHalf(t *testing.T) {
	out := stripANSI(pageWithKeys(t, numberedLines(20), "dq"))
	for _, line := range []string{"line 05", "line 06"} {
		if !strings.Contains(out, line) {
			t.Errorf("%q was never printed:\n%s", line, out)
		}
	}
	for i := 0; i < 5; i++ {
		line := fmt.Sprintf("line %02d of the page", i)
		if got := strings.Count(out, line); got != 1 {
			t.Errorf("the first screenful reprinted %q %d times, want 1:\n%s", line, got, out)
		}
	}
	if strings.Contains(out, "line 07") {
		t.Errorf("a half step of 2 printed five lines:\n%s", out)
	}
}

// Enter moves one line, which is the key for reading down through
// something slowly rather than a screenful at a bound.
func TestEnterMovesOneLine(t *testing.T) {
	p := newPager(numberedLines(20), 6) // view 5
	for _, r := range "\r\n\r" {
		if p.handle(context.Background(), nil, r) {
			t.Fatalf("Enter left the pager at top %d", p.top)
		}
	}
	if p.top != 3 {
		t.Errorf("three Enters: top = %d, want 3", p.top)
	}
	p.top = p.maxTop()
	if !p.handle(context.Background(), nil, '\r') {
		t.Error("Enter at the end of the page should leave, as space does")
	}
}

// One line moved is one line printed: the four still on the screen are
// not printed under themselves.
func TestEnterPrintsOneLine(t *testing.T) {
	out := stripANSI(pageWithKeys(t, numberedLines(20), "\r\rq"))
	for i := 0; i < 7; i++ {
		line := fmt.Sprintf("line %02d of the page", i)
		if got := strings.Count(out, line); got != 1 {
			t.Errorf("%q printed %d times, want 1:\n%s", line, got, out)
		}
	}
	if strings.Contains(out, "line 07") {
		t.Errorf("two Enters printed more than two lines:\n%s", out)
	}
}

// A key that moves nothing prints nothing.  Every keystroke of a search
// redraws the status line, and if that redraw were a repaint the page
// would be typed out again a screenful per character.
func TestAKeyThatMovesNothingPrintsNoLines(t *testing.T) {
	out := stripANSI(pageWithKeys(t, numberedLines(20), "/line 03\rq"))
	// "line 03" is on the first screenful, so the search moves nowhere.
	for i := 0; i < 5; i++ {
		line := fmt.Sprintf("line %02d of the page", i)
		if got := strings.Count(out, line); got != 1 {
			t.Errorf("%q printed %d times while a search was typed, want 1:\n%s", line, got, out)
		}
	}
}

func TestManPrintsToABufferWithoutPaging(t *testing.T) {
	// A test, a redirect, a pipe: the whole page, no waiting.
	var buf strings.Builder
	sh := &Shell{}
	if err := manPrint(context.Background(), sh, &buf, "hello\nworld\n"); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "hello\nworld\n" {
		t.Errorf("got %q", buf.String())
	}
}

func TestPageSearchFromKeys(t *testing.T) {
	var out strings.Builder
	keys := make(chan rune, 16)
	tm := &Term{
		out:    &out,
		keys:   keys,
		done:   make(chan struct{}),
		width:  40,
		height: 6,
		busy:   true,
	}
	for _, r := range "/needle\rq" {
		keys <- r
	}
	close(keys)
	if err := page(context.Background(), tm, twentyLines()); err != nil {
		t.Fatal(err)
	}
	got := stripANSI(out.String())
	if !strings.Contains(got, "the needle is here") {
		t.Errorf("search did not land on the needle:\n%s", got)
	}
}

func TestPageReadsKeysUntilTheEnd(t *testing.T) {
	var out strings.Builder
	keys := make(chan rune, 8)
	tm := &Term{
		out:    &out,
		keys:   keys,
		done:   make(chan struct{}),
		width:  40,
		height: 6,
		busy:   true,
	}
	keys <- ' '
	keys <- ' '
	keys <- ' '
	keys <- ' ' // last screens then leave
	close(keys)
	if err := page(context.Background(), tm, twentyLines()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "needle") {
		t.Errorf("pager never showed the needle:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "--More--") && !strings.Contains(out.String(), "(END)") {
		t.Errorf("pager never showed a status line:\n%s", stripANSI(out.String()))
	}
}
