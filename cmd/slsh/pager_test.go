package main

import (
	"context"
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
	if p.top != 4 {
		t.Errorf("repeat at %d, want 4", p.top)
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
