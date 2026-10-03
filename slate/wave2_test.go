package slate

import (
	"strings"
	"testing"
)

// Wave 2, sections 1 to 4 of the plan: the grammar and the static checks.
// The runner runs none of it yet; the last test says so.

const w2Hdr = suiteHdr + "item hat is \"Example Hat\" in \"Objects\"\nitem vest is \"Example Vest\" in \"Clothing\"\n"

func TestButtonObservationsParse(t *testing.T) {
	s := mustCheck(t, w2Hdr+`expect button a text "Close" box becomes gone within 8s
then expect button a link 2 text "Open" face 1 is shown
then expect button a pattern "^P[0-9]" is count 8 as $n
then expect button a symbol "x" changes
then expect button a text "Close" is original
then expect button a text "Close" is gone
`)
	st := body(s)
	b := st[0].Expect[0].Button
	if b == nil || b.State.Kind != StateBecomes || b.Val != ButtonGone || len(b.Button.Parts) != 2 || b.Button.Parts[1].Kind != PartBox {
		t.Fatalf("%+v", b)
	}
	if st[0].Expect[0].Within == nil {
		t.Fatal("within lost")
	}
	b = st[1].Expect[0].Button
	if b.Link == nil || b.Link.Value != 2 || b.Button.Face == nil || b.Button.Face.Value != 1 || b.Val != ButtonShown || b.State.Kind != StateIs {
		t.Fatalf("%+v", b)
	}
	e := st[2].Expect[0]
	if e.Neg || e.Button.Val != ButtonCount || e.Button.Count.Value != 8 || e.As == nil || e.As.Name != "n" {
		t.Fatalf("%+v", e)
	}
	if b = st[3].Expect[0].Button; b.State.Kind != StateChanges {
		t.Fatalf("%+v", b)
	}
	if b = st[4].Expect[0].Button; !b.State.Original {
		t.Fatalf("%+v", b)
	}
	// A number the author writes bare is the count, and zero is legal.
	mustCheck(t, w2Hdr+"expect button a text \"x\" is count 0\n")
	// The capture binds a number, usable where a number is.
	mustCheck(t, w2Hdr+"expect button a text \"x\" is shown as $n\nthen expect glow a face 0 is $n\n")
	refuses(t, w2Hdr+"expect button a text \"x\" is shown as $n\nthen expect click a is $n\n", "$n\n", "capture type mismatch")
	parseErr(t, w2Hdr+"expect button a text \"x\" is lots\n", "expected shown, gone, count N, or original")
	parseErr(t, w2Hdr+"expect button a text \"x\" is count 1.5\n", "a count is a whole number")
	parseErr(t, w2Hdr+"expect button a text \"x\" is count\n", "expected an integer")
	parseErr(t, w2Hdr+"expect button a text \"x\" changes original\n", "changes takes no value")
	parseErr(t, w2Hdr+"expect button a text \"x\"\n", "expected is, becomes, or changes")
	parseErr(t, w2Hdr+"expect button a is shown\n", "a button needs a part")
	parseErr(t, w2Hdr+"expect button a text matching \"x\" is shown\n", "matching is not legal here")
}

func TestButtonObservationsAreRefusedStatically(t *testing.T) {
	refuses(t, w2Hdr+"expect button a 2 text \"x\" is shown\n", "2 text", "button N is not legal in an expectation")
	refuses(t, w2Hdr+"expect button a text \"x\" is count -1\n", "-1", "count -1 is below 0")
	refuses(t, w2Hdr+"expect button a text \"x\" is count 99999999999\n", "99999999999", "is outside -2147483648 to 2147483647")
	refuses(t, w2Hdr+"expect button a pattern \"(\" is shown\n", "pattern \"(\"", "pattern")
	refuses(t, w2Hdr+"expect button a text \"  \" is shown\n", "text \"  \"", "button text is empty")
	mustCheck(t, w2Hdr+"expect button b link 1 text \"x\" is shown\n")
	mustCheck(t, w2Hdr+"expect button a link 1 text \"x\" is shown\n")
	refuses(t, w2Hdr+"expect button z text \"x\" is shown\n", "z text", "z is not an object")
	refuses(t, w2Hdr+"expect button hat text \"x\" is shown\n", "hat text", "hat is an item")
	refuses(t, w2Hdr+"expect no button a text \"x\" is shown as $n\n", "$n", "a negative expectation matches nothing to bind")
	refuses(t, w2Hdr+"expect button a text \"x\" is shown within 50ms\n", "50ms", "outside 100ms to 120s")
	refuses(t, w2Hdr+"expect button a text $u is shown\n", "$u", "$u is not bound")
	// A button reading may use a captured label bound earlier.
	mustCheck(t, w2Hdr+"expect say matching \"(?P<l>.+)\" on public from anyone\nthen expect button a text $l is shown\n")
}

func TestTheGuardedTouch(t *testing.T) {
	g := `touch a button text "Open" box if shown`
	s := mustCheck(t, w2Hdr+"before each {\n  "+g+"\n  then expect button a text \"Close\" is shown within 8s\n}\nafter each {\n  "+g+"\n  then expect button a text \"Open\" is shown\n}\ntest \"t\" { touch a anywhere }\n")
	tc := s.Before().Steps[0].Stimulus.Touch
	if tc.Guard == nil || s.Source(*tc.Guard) != "if shown" || tc.Button == nil {
		t.Fatalf("%+v", tc)
	}
	// With a face and a link the guard still follows the whole target.
	mustCheck(t, w2Hdr+"before each {\n  touch a link 1 button text \"x\" face 2 if shown\n  then expect button a text \"y\" is shown\n}\ntest \"t\" { touch a anywhere }\n")
	refuses(t, w2Hdr+"before each {\n  touch a button 2 text \"x\" if shown\n  then expect button a text \"y\" is shown\n}\ntest \"t\" { touch a anywhere }\n", "2 text", "button N is not legal with if shown")
	parseErr(t, w2Hdr+"before each {\n  touch a button text \"x\" if\n}\ntest \"t\" { touch a anywhere }\n", "expected shown")
	parseErr(t, w2Hdr+"touch a button text \"x\" if hidden\n", "expected shown")

	conv := "\n  then expect button a text \"Close\" is shown\n"
	// Where.
	refuses(t, w2Hdr+"test \"t\" {\n  "+g+conv+"}\n", "if shown", "legal only in before each and after each")
	refuses(t, w2Hdr+g+conv, "if shown", "legal only in before each and after each")
	// A sequence is checked in the block that calls it.
	sq := "sequence open {\n  " + g + conv + "}\n"
	mustCheck(t, w2Hdr+sq+"before each { do open }\ntest \"t\" { touch a anywhere }\n")
	pe := refuses(t, w2Hdr+sq+"test \"t\" { do open }\n", "if shown", "legal only in before each and after each")
	if !strings.Contains(pe.Msg, `(in test "t", via do open at line`) {
		t.Fatalf("%q", pe.Msg)
	}
	// Which target.
	for _, tgt := range []string{"anywhere", "face 0", "link 1", "showing " + idOne} {
		refuses(t, w2Hdr+"before each {\n  touch a "+tgt+" if shown\n  expect button a text \"Close\" is shown\n}\ntest \"t\" { touch a anywhere }\n", "if shown", "guards a touch of a button")
	}
	// No expectations of its own.
	refuses(t, w2Hdr+"before each {\n  "+g+"\n  expect button a text \"Close\" is shown\n  expect button a text \"x\" is shown\n}\ntest \"t\" { touch a anywhere }\n",
		"expect button a text \"Close\"", "a guarded touch has no expectations")
	// The next step of the same block holds a positive expectation.
	refuses(t, w2Hdr+"before each {\n  "+g+"\n}\ntest \"t\" { touch a anywhere }\n", "if shown", "followed, in the same block, by a step with a positive expectation")
	refuses(t, w2Hdr+"before each {\n  "+g+"\n  touch a anywhere\n}\ntest \"t\" { touch a anywhere }\n", "if shown", "followed, in the same block")
	refuses(t, w2Hdr+"before each {\n  "+g+"\n  then expect no button a text \"Open\" is shown\n}\ntest \"t\" { touch a anywhere }\n", "if shown", "positive expectation")
	// Not satisfied by the test that follows the block.
	refuses(t, w2Hdr+"before each {\n  "+g+"\n}\ntest \"t\" { expect button a text \"Close\" is shown }\n", "if shown", "followed, in the same block")
	refuses(t, w2Hdr+"before each {\n  "+g+"\n  "+g+"\n  then expect button a text \"x\" is shown\n}\ntest \"t\" { touch a anywhere }\n", "if shown\n  "+g[:5], "followed, in the same block")
}

func TestOrderedAndSorted(t *testing.T) {
	s := mustCheck(t, w2Hdr+`expect dialog from a text "Pick" button "A" button matching "^B" only ordered count 2 sorted
then expect dialog from a button "x" button "y" ordered
then expect dialog from a sorted matching "^n([0-9]+)$"
then expect dialog from a sorted matching "^[^<>]"
then expect dialog from a button "x" button "y" sorted within 5s as $m
`)
	st := body(s)
	d := st[0].Expect[0].Dialog
	if !d.Only || !d.Ordered || d.Count == nil || d.Sorted == nil || d.Sorted.Matching || s.Source(d.OrderedSpan) != "ordered" {
		t.Fatalf("%+v", d)
	}
	if d = st[1].Expect[0].Dialog; !d.Ordered || d.Only || d.Sorted != nil {
		t.Fatalf("%+v", d)
	}
	if d = st[2].Expect[0].Dialog; d.Sorted == nil || !d.Sorted.Matching || d.Sorted.Pattern != "^n([0-9]+)$" {
		t.Fatalf("%+v", d.Sorted)
	}
	if st[4].Expect[0].Within == nil || st[4].Expect[0].As == nil {
		t.Fatal("within or as after sorted lost")
	}
	// The order of the words is the grammar's.
	parseErr(t, w2Hdr+"expect dialog from a button \"x\" button \"y\" ordered only\n", "expected a stimulus, expect, then, or do, found only")
	parseErr(t, w2Hdr+"expect dialog from a button \"x\" button \"y\" sorted count 2\n", "found count")
	parseErr(t, w2Hdr+"expect dialog from a sorted matching $p\n", "a capture is not a pattern")
	parseErr(t, w2Hdr+"expect dialog from a sorted matching\n", "expected a string")

	refuses(t, w2Hdr+"expect dialog from a button \"x\" ordered\n", "ordered", "ordered needs at least two button clauses")
	refuses(t, w2Hdr+"expect dialog from a ordered\n", "ordered", "ordered needs at least two button clauses")
	refuses(t, w2Hdr+"expect dialog from a button \"x\" button \"y\" sorted matching \"(\"\n", "\"(\"", "pattern")
	refuses(t, w2Hdr+"expect dialog from a sorted matching \"(a)(b)\"\n", "\"(a)(b)\"", "at most one capturing group")
	refuses(t, w2Hdr+"expect dialog from a sorted matching \"(?P<n>a)(b)\"\n", "\"(?P<n>a)(b)\"", "at most one capturing group")
	mustCheck(t, w2Hdr+"expect dialog from a sorted matching \"(?:a)(b)\"\n")
	mustCheck(t, w2Hdr+"expect dialog from a sorted matching \"(?P<n>a)\"\n")
	// A named group of a sort pattern is not a capture binding.
	refuses(t, w2Hdr+"expect dialog from a sorted matching \"(?P<n>a)\"\nthen expect dialog from a text $n\n", "$n", "$n is not bound")
	// The other dialog checks still run.
	refuses(t, w2Hdr+"expect dialog from a button \"x\" button \"y\" ordered count 1\n", "last:1\n", "count 1: the 2 button clauses")
}

func TestItemsWearTakeOffAndAttached(t *testing.T) {
	s := mustCheck(t, w2Hdr+`before each {
  wear hat on "HUD Top Left" as hud
  expect attached hud on "hud top left" within 10s
}
test "t" {
  touch hud anywhere
  expect no attached hud off
  expect attached hud on "HUD Top Left"
  wear vest on "chest" as v
  take off v
  expect attached v off
}
after each {
  take off hud
  expect attached hud off
}
`)
	if len(s.Items) != 2 || s.Items[0].Name.Text != "hat" || s.Items[0].World != "Example Hat" || s.Items[0].Folder != "Objects" || s.Items[1].Folder != "Clothing" {
		t.Fatalf("%+v", s.Items)
	}
	w := s.Before().Steps[0].Stimulus.Wear
	if w == nil || w.Item.Text != "hat" || w.Point != "HUD Top Left" || w.As.Text != "hud" || s.Source(w.PointSpan) != `"HUD Top Left"` {
		t.Fatalf("%+v", w)
	}
	at := s.Before().Steps[0].Expect[0].Attached
	if at == nil || at.Off || at.Point != "hud top left" {
		t.Fatalf("%+v", at)
	}
	if off := s.After().Steps[0].Expect[0].Attached; off == nil || !off.Off {
		t.Fatalf("%+v", off)
	}
	if tk := s.After().Steps[0].Stimulus.TakeOff; tk == nil || tk.Name.Text != "hud" {
		t.Fatalf("%+v", tk)
	}

	parseErr(t, "slate 1\nitem hat is \"H\"\ntouch hat anywhere\n", "expected in")
	parseErr(t, "slate 1\nitem hat \"H\" in \"F\"\ntouch hat anywhere\n", "expected is")
	parseErr(t, "slate 1\nitem hat is \"H\" in\n", "expected a string")
	parseErr(t, "slate 1\nitem hat is matching \"H\" in \"F\"\n", "matching is not legal here")
	parseErr(t, w2Hdr+"wear hat on \"Chest\"\n", "expected as")
	parseErr(t, w2Hdr+"wear hat as h\n", "expected on")
	parseErr(t, w2Hdr+"wear hat on Chest as h\n", "expected a string")
	parseErr(t, w2Hdr+"wear hat on matching \"Chest\" as h\n", "matching is not legal here")
	parseErr(t, w2Hdr+"take a\n", "expected off")
	parseErr(t, w2Hdr+"take off\n", "expected a name")
	parseErr(t, w2Hdr+"expect attached a\n", "expected on or off")
	parseErr(t, w2Hdr+"expect attached a on Chest\n", "expected a string")
	// Headers go first, as every header does.
	parseErr(t, suiteHdr+"touch a anywhere\nitem hat is \"H\" in \"F\"\n", "headers go before the first step")

	// Items.
	refuses(t, suiteHdr+"item a is \"X\" in \"F\"\ntouch a anywhere\n", "last:a is \"X\"", "a is already bound")
	refuses(t, "slate 1\nitem h is \"X\" in \"F\"\nobject h is \"H\"\ntouch h anywhere\n", "h is \"H\"", "h is already bound")
	refuses(t, "slate 1\nitem h is \"X\" in \"F\"\nitem h is \"Y\" in \"F\"\nobject a is \"A\"\ntouch a anywhere\n", "h is \"Y\"", "h is already bound")
	refuses(t, "slate 1\nitem h is \"\" in \"F\"\nobject a is \"A\"\ntouch a anywhere\n", "\"\" in", "item name is empty")
	refuses(t, "slate 1\nitem h is \"X\" in \" \"\nobject a is \"A\"\ntouch a anywhere\n", "\" \"", "folder name is empty")
	// An item is used only by wear.
	for _, use := range []string{"touch hat anywhere", "sit hat", "take off hat", "choose \"x\" on hat",
		"expect attached hat off", "expect texture hat face 0 changes", "expect say \"x\" on public from object hat"} {
		src := w2Hdr + use + "\n"
		if strings.HasPrefix(use, "expect") {
			src = w2Hdr + "touch a anywhere\n" + use + "\n"
		}
		refuses(t, src, "last:hat", "hat is an item; only wear uses an item")
	}
	refuses(t, w2Hdr+"probe hat\ntouch a anywhere\n", "hat\ntouch", "hat is not an object")
	// wear takes an item, and a known point.
	refuses(t, w2Hdr+"wear a on \"Chest\" as h\n", "a on", "a is an object; wear takes an item")
	refuses(t, w2Hdr+"wear nothing on \"Chest\" as h\n", "nothing", "nothing is not an item")
	refuses(t, w2Hdr+"wear hat on \"Left Pocket\" as h\n", "\"Left Pocket\"", `"Left Pocket" is not an attachment point sl knows`)
	refuses(t, w2Hdr+"wear hat on \"\" as h\n", "\"\"", "is not an attachment point sl knows")
	refuses(t, w2Hdr+"touch a anywhere\nexpect attached a on \"Lap\"\n", "\"Lap\"", "is not an attachment point sl knows")
	refuses(t, w2Hdr+"touch a anywhere\nexpect no attached a on \"Lap\"\n", "\"Lap\"", "is not an attachment point sl knows")
	mustCheck(t, w2Hdr+"wear hat on \" HUD TOP LEFT \" as h\n")
	// The wear name is a new binding, unique.
	refuses(t, w2Hdr+"wear hat on \"Chest\" as a\n", "last:a\n", "a is already bound")
	refuses(t, w2Hdr+"wear hat on \"Chest\" as vest\n", "last:vest\n", "vest is already bound")
	refuses(t, w2Hdr+"wear hat on \"Chest\" as h\nwear vest on \"Skull\" as h\n", "last:h\n", "h is already bound")
	refuses(t, w2Hdr+"wear hat on \"Chest\" as h\nexpect rez name \"R\" from a as h\n", "last:h\n", "h is already bound")
	refuses(t, w2Hdr+"expect rez name \"R\" from a as h\nwear hat on \"Chest\" as h\n", "last:h\n", "h is already bound")
	refuses(t, w2Hdr+"wear hat on \"Chest\" as h\nthen expect rez name \"R\" from a as h\n", "last:h\n", "h is already bound")
	// It is usable by the same step's expectations and by later steps.
	mustCheck(t, w2Hdr+"wear hat on \"Chest\" as h\nexpect attached h on \"chest\"\nexpect click h is touch\ntouch h anywhere\n")
	// A rez name is still not usable in its own step.
	refuses(t, w2Hdr+"expect rez name \"R\" from a as r\nexpect click r is touch\n", "r is", "r is bound in this step")
	// But a wear name used before the wear is not bound.
	refuses(t, w2Hdr+"touch h anywhere\nwear hat on \"Chest\" as h\n", "h anywhere", "h is not an object")
	// Taken off, a name is done with, from the next step on.
	mustCheck(t, w2Hdr+"wear hat on \"Chest\" as h\ntake off h\nexpect attached h off\n")
	refuses(t, w2Hdr+"wear hat on \"Chest\" as h\ntake off h\ntouch h anywhere\n", "h anywhere", "h was taken off at line 9; it cannot be used again in this test")
	refuses(t, w2Hdr+"wear hat on \"Chest\" as h\ntake off h\nthen expect attached h off\n", "h off", "h was taken off at line 9")
	refuses(t, w2Hdr+"wear hat on \"Chest\" as h\ntake off h\ntake off h\n", "last:h\n", "h was taken off at line 9")
	refuses(t, w2Hdr+"take off a\ntouch a anywhere\n", "a anywhere", "a was taken off at line 8")
	refuses(t, w2Hdr+"wear hat on \"Chest\" as h\ntake off h\nwear hat on \"Chest\" as h\n", "last:h\n", "h is already bound")
	// Another test is another test.
	mustCheck(t, w2Hdr+"test \"one\" {\n take off a\n}\ntest \"two\" {\n touch a anywhere\n}\n")
	refuses(t, w2Hdr+"test \"one\" {\n take off a\n}\nafter each {\n touch a anywhere\n}\n", "a anywhere", "a was taken off at line 9")
	// before each, the body and after each.
	mustCheck(t, w2Hdr+"before each {\n wear hat on \"Chest\" as h\n}\nafter each {\n take off h\n}\ntest \"t\" {\n touch h anywhere\n}\n")
	pe := refuses(t, w2Hdr+"test \"t\" {\n wear hat on \"Chest\" as h\n}\nafter each {\n take off h\n}\n", "last:h\n}", "h is bound in test \"t\", which after each cannot rely on")
	_ = pe
	refuses(t, w2Hdr+"before each {\n wear hat on \"Chest\" as h\n}\ntest \"t\" {\n wear vest on \"Skull\" as h\n}\n", "last:h\n}", "h is already bound")
	// A sequence that wears, called twice, binds its name twice.
	refuses(t, w2Hdr+"sequence put { wear hat on \"Chest\" as h }\ntest \"t\" { do put\n do put }\n", "h }", "h is already bound")
	mustCheck(t, w2Hdr+"sequence put { wear hat on \"Chest\" as h }\ntest \"t\" { do put\n touch h anywhere }\n")
}

func TestTheDescriptionQualifier(t *testing.T) {
	s := mustCheck(t, "slate 1\nobject a is \"Example Sign\" description \"north\"\nobject b is \"Example Sign\" description matching \"^s\"\nobject c is \"Example Sign\" description \"\"\nobject d is \"Example Lamp\"\ntouch a anywhere\n")
	o := s.Objects
	if !o[0].HasDesc || o[0].Desc.Value != "north" || o[0].Desc.Pattern || !o[1].Desc.Pattern || !o[2].HasDesc || o[2].Desc.Value != "" || o[3].HasDesc {
		t.Fatalf("%+v", o)
	}
	if got := s.Source(o[1].Span); got != "object b is \"Example Sign\" description matching \"^s\"" {
		t.Fatalf("span %q", got)
	}
	parseErr(t, "slate 1\nobject a is \"A\" description\ntouch a anywhere\n", "expected a string")
	parseErr(t, "slate 1\nobject a is \"A\" description matching $x\ntouch a anywhere\n", "a capture is not a pattern")
	refuses(t, "slate 1\nobject a is \"A\" description $d\ntouch a anywhere\n", "$d", "a capture cannot be used here")
	refuses(t, "slate 1\nobject a is \"A\" description matching \"(\"\ntouch a anywhere\n", "\"(\"", "pattern")
	// Twins need a description each, and a different one.
	refuses(t, "slate 1\nobject a is \"A\"\nobject b is \"A\" description \"x\"\ntouch a anywhere\n", "\"A\" desc", "in-world name \"A\" is already used; objects of one name need a description each")
	refuses(t, "slate 1\nobject a is \"A\" description \"x\"\nobject b is \"A\"\ntouch a anywhere\n", "\"A\"\ntouch", "objects of one name need a description each")
	refuses(t, "slate 1\nobject a is \"A\" description \"x\"\nobject b is \"A\" description \"x\"\ntouch a anywhere\n", "\"A\" description \"x\"\ntouch", "already used with that description")
	refuses(t, "slate 1\nobject a is \"A\" description matching \"x\"\nobject b is \"A\" description matching \"x\"\ntouch a anywhere\n", "\"A\" description matching \"x\"\ntouch", "already used with that description")
	mustCheck(t, "slate 1\nobject a is \"A\" description \"x\"\nobject b is \"A\" description matching \"x\"\ntouch a anywhere\n")
	refuses(t, "slate 1\nobject a is \"A\" description \"x\"\nobject a is \"B\"\ntouch a anywhere\n", "a is \"B\"", "a is already bound")
}

// The new words are words only where the grammar expects them.
func TestWave2WordsAreNames(t *testing.T) {
	for _, w := range strings.Fields("item wear take off attached ordered sorted shown gone count original if in") {
		mustCheck(t, "slate 1\nobject "+w+" is \"O\"\nprobe "+w+"\nitem i is \"I\" in \"F\"\n"+
			"sequence "+w+" { touch "+w+" anywhere }\n"+
			"test \"t\" {\n"+
			"  do "+w+"\n"+
			"  wear i on \"Chest\" as w2"+w+"\n"+
			"  expect attached w2"+w+" on \"Chest\"\n"+
			"  touch "+w+" button text \"B\"\n"+
			"  expect button "+w+" text \"B\" is shown\n"+
			"  expect dialog from "+w+" button \"a\" button \"b\" ordered sorted\n"+
			"  take off "+w+"\n"+
			"}\n")
		mustCheck(t, "slate 1\nitem "+w+" is \"I\" in \"F\"\nobject o is \"O\"\nwear "+w+" on \"Chest\" as "+w+"2\n")
	}
}
