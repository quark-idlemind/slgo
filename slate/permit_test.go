package slate

import (
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

var idSignBand = msg.MustParseUUID("42c47e57-7e57-c0de-b744-b930b5ddead4")

func TestPermitParsesAndChecks(t *testing.T) {
	all := "take-controls trigger-animation attach change-links track-camera control-camera teleport override-animations"
	s := mustCheck(t, hdr+"allow permission "+all+" from sign\nallow permission attach from vendor\nallow permission attach from vendor\nwait 100ms\n")
	if len(s.Permits) != 3 || len(s.Permits[0].Names) != 8 || s.Permits[2].From.Text != "vendor" {
		t.Fatalf("permits = %+v", s.Permits)
	}
	for w, bit := range permitWords {
		if bit == 0 {
			t.Errorf("%s has no bit", w)
		}
	}
	if len(permitWords) != 8 {
		t.Errorf("%d words", len(permitWords))
	}
	// A header may come before the object it names, and an item is an object here.
	mustCheck(t, "slate 1\nallow permission attach from hat\nitem hat is \"Example Hat\" in \"Objects\"\nobject sign is \"Example Sign\"\nwait 100ms\n")
	mustCheck(t, "slate 1\nallow permission change-links from sign\nobject sign is \"Example Sign\"\nwait 100ms\n")
}

func TestPermitStaticErrors(t *testing.T) {
	checkErr(t, hdr+"allow permission debit from sign\nwait 100ms\n", "allow pay and --pay")
	checkErr(t, hdr+"allow permission attach debit from sign\nwait 100ms\n", "never grants debit")
	checkErr(t, hdr+"allow permission return-objects from sign\nwait 100ms\n", "not a permission a file can name")
	checkErr(t, hdr+"allow permission change links from sign\nwait 100ms\n", "not a permission a file can name")
	checkErr(t, hdr+"allow permission attach from nothing\nwait 100ms\n", "nothing is not an object or an item")
	parseErr(t, hdr+"allow permission from sign\nwait 100ms\n", "expected a permission name")
	parseErr(t, hdr+"allow permission attach sign\nwait 100ms\n", "expected from")
	parseErr(t, hdr+"allow permission attach from\nwait 100ms\n", "expected a step or a test block")
}

func TestPermitWordsExcludeDebit(t *testing.T) {
	for w, bit := range permitWords {
		if bit.Any(sl.PermissionDebit) {
			t.Errorf("%s names debit", w)
		}
	}
}

// ask relays a permission request from an object after d.
func (f *fakeGrid) ask(t *testing.T, id msg.UUID, name string, wants sl.Perms) {
	t.Helper()
	m := &msg.ScriptQuestion{}
	m.Data.TaskID, m.Data.ItemID = id, idStray
	m.Data.ObjectName, m.Data.ObjectOwner = []byte(name+"\x00"), []byte("Example Resident\x00")
	m.Data.Questions = int32(wants)
	f.quietly(t, 150*time.Millisecond, m)
}

func (f *fakeGrid) withSignBand() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects = append(f.objects, child(prim(idSignBand, 111, "Example Sign Band", testMe), f.objects[0]))
}

// answers is the Questions mask of each answer the session sent.
func answers(f *fakeGrid) []sl.Perms {
	var out []sl.Perms
	for _, a := range sentOf[*msg.ScriptAnswerYes](f) {
		out = append(out, sl.Perms(a.Data.Questions))
	}
	return out
}

func wantAnswers(t *testing.T, f *fakeGrid, want ...sl.Perms) {
	t.Helper()
	got := answers(f)
	if len(got) != len(want) {
		t.Fatalf("answers = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("answer %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestAGrantedPermissionIsExactlyWhatTheFileNames(t *testing.T) {
	f := newGrid(t)
	f.ask(t, idSign, "Example Sign", sl.PermissionChangeLinks)
	res := play(t, f, hdr+"allow permission change-links from sign\nwait 600ms\n")
	wantExit(t, res, 0)
	mustHave(t, res, "permission granted to sign: change links")
	mustNotHave(t, res, "refused")
	wantAnswers(t, f, sl.PermissionChangeLinks)
}

func TestAChildPrimOfABoundObjectIsGranted(t *testing.T) {
	f := newGrid(t)
	f.withSignBand()
	f.ask(t, idSignBand, "Example Sign Band", sl.PermissionChangeLinks)
	res := play(t, f, hdr+"allow permission change-links from sign\nwait 600ms\n")
	wantExit(t, res, 0)
	mustHave(t, res, "permission granted to sign: change links")
	wantAnswers(t, f, sl.PermissionChangeLinks)
}

func TestDebitAskedWithANamedPermissionIsRefused(t *testing.T) {
	f := newGrid(t)
	f.ask(t, idSign, "Example Sign", sl.PermissionChangeLinks|sl.PermissionDebit)
	res := play(t, f, hdr+"allow permission change-links from sign\nwait 600ms\n")
	wantExit(t, res, 0)
	mustHave(t, res, "permission granted to sign: change links; refused: debit")
	wantAnswers(t, f, sl.PermissionChangeLinks)
}

func TestDebitAloneIsDeniedWhateverTheFileNames(t *testing.T) {
	f := newGrid(t)
	f.ask(t, idSign, "Example Sign", sl.PermissionDebit)
	res := play(t, f, hdr+"allow permission take-controls attach teleport from sign\nwait 600ms\n")
	mustHave(t, res, "permission denied from sign: debit")
	wantAnswers(t, f, 0)
}

func TestTheMaskNeverHoldsDebit(t *testing.T) {
	var all sl.Perms
	for _, bit := range permitWords {
		all |= bit
	}
	s := &Script{Permits: []PermissionAllow{{Names: []Ident{{Text: "attach"}}, From: Ident{Text: "sign"}}}}
	r := &runner{s: s, bind: map[string]*binding{"sign": {seen: &sl.Seen{Object: sl.Object{ID: idSign}}}}}
	q := &sl.Permission{Object: idSign, Wants: sl.PermissionAttach | sl.PermissionDebit}
	if got := r.permitMask(q); got != sl.PermissionAttach {
		t.Errorf("mask = %v", got)
	}
	if all.Any(neverGranted) || neverGranted != sl.PermissionDebit {
		t.Error("a word or the never set names debit")
	}
}

func TestAnotherObjectIsDenied(t *testing.T) {
	f := newGrid(t)
	f.ask(t, idVendor, "Example Tip Jar", sl.PermissionChangeLinks)
	res := play(t, f, hdr+"allow permission change-links from sign\nwait 600ms\n")
	mustHave(t, res, "permission denied from vendor: change links")
	wantAnswers(t, f, 0)
}

func TestAPermissionNotNamedIsRefusedAndNothingNamedDenies(t *testing.T) {
	f := newGrid(t)
	f.ask(t, idSign, "Example Sign", sl.PermissionAttach)
	res := play(t, f, hdr+"allow permission change-links from sign\nwait 600ms\n")
	mustHave(t, res, "permission denied from sign: attach")
	wantAnswers(t, f, 0)

	f = newGrid(t)
	f.ask(t, idSign, "Example Sign", sl.PermissionChangeLinks)
	res = play(t, f, hdr+"wait 600ms\n")
	mustHave(t, res, "permission denied from sign: change links")
	wantAnswers(t, f, 0)
}

func TestAttachIsGrantedToAnItemsWornObjectOnlyWhenNamed(t *testing.T) {
	for _, tc := range []struct {
		allow string
		want  sl.Perms
		line  string
	}{
		{"allow permission attach from hat\n", sl.PermissionAttach, "permission granted to hud: attach"},
		{"allow permission change-links from hat\n", 0, "permission denied from hud: attach"},
		{"", 0, "permission denied from hud: attach"},
	} {
		f := newGrid(t)
		f.withWearing(t)
		id := idWornRoot
		id[15] = 1 // the first wear's root
		f.ask(t, id, "Example Hat", sl.PermissionAttach)
		src := wearHdr + tc.allow + "wear hat on \"HUD centre 2\" as hud\nwait 600ms\n"
		// the request arrives after the wear has returned
		res := play(t, f, src)
		wantExit(t, res, 0)
		if !strings.Contains(res.Transcript, tc.line) {
			t.Errorf("%q: no %q:\n%s", tc.allow, tc.line, res.Transcript)
		}
		wantAnswers(t, f, tc.want)
	}
}

// TestAPermissionHeaderWithoutFromSaysSo: the list of names stops at a
// word that begins something else, so a missing from is named.
func TestAPermissionHeaderWithoutFromSaysSo(t *testing.T) {
	_, err := Parse("t.slate", []byte("slate 1\nobject sign is \"Example Sign\"\nallow permission attach sign\nwait 100ms\n"))
	if err == nil || !strings.Contains(err.Error(), "expected from") {
		t.Errorf("a header without from: %v", err)
	}
}

// TestChangeLinksIsGrantedToAnItemsRezzedObjectOnlyWhenNamed: an object
// a rez step made from a named item counts as that item's, as a worn one
// does; debit is still never granted.
func TestChangeLinksIsGrantedToAnItemsRezzedObjectOnlyWhenNamed(t *testing.T) {
	for _, tc := range []struct {
		allow string
		want  sl.Perms
		line  string
	}{
		{"allow permission change-links from box\n", sl.PermissionChangeLinks, "permission granted to made: change links; refused: debit"},
		{"", 0, "permission denied from made: debit, change links"},
	} {
		f := newGrid(t)
		f.withRezzing(t, true)
		id := idRezzedBox
		id[15] = 1 // the first rez
		src := rezHdr + tc.allow + "rez box at 130 128 25 as made\nexpect position made is 130 128 25 within 1s\n"
		done := make(chan struct{})
		go func() {
			defer close(done)
			time.Sleep(1500 * time.Millisecond)
			f.ask(t, id, "Example Box", sl.PermissionChangeLinks|sl.PermissionDebit)
		}()
		res := play(t, f, src+"wait 2s\n")
		<-done
		wantExit(t, res, 0)
		if !strings.Contains(res.Transcript, tc.line) {
			t.Errorf("%q: no %q:\n%s", tc.allow, tc.line, res.Transcript)
		}
		wantAnswers(t, f, tc.want)
	}
}
