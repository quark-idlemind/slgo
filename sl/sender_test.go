package sl

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
)

// TestSenderIsWhatTheNameNames: the kind follows what the viewer makes
// of the same message, so that an object named after a person, a group
// and the grid's own messages are each labelled as what they are.
func TestSenderIsWhatTheNameNames(t *testing.T) {
	t.Parallel()
	somebody := msg.MustParseUUID("876e7e57-7e57-c0de-8597-66b760a8cb5f")
	region := msg.MustParseUUID("36fa7e57-7e57-c0de-9e3a-f16236c34d10")
	for _, c := range []struct {
		name string
		im   IM
		want Sender
	}{
		{"a person's message", IM{Dialog: DialogMessage, From: somebody, FromName: "Some Body"}, SenderPerson},
		{"a message with no sender", IM{Dialog: DialogMessage, FromName: "Some Body"}, SenderGrid},
		{"a message signed by the grid", IM{Dialog: DialogMessage, From: somebody, FromName: SystemName}, SenderGrid},
		{"a message from a group", IM{Dialog: DialogMessage, From: somebody, FromName: "Builders", Group: true}, SenderGroup},
		{"an object's message", IM{Dialog: DialogFromTask, From: somebody, FromName: "Some Body", Region: region}, SenderObject},
		{"a group's object's message", IM{Dialog: DialogFromTask, From: somebody, FromName: "a lamp", Group: true, Region: region}, SenderObject},
		{"an object called Second Life", IM{Dialog: DialogFromTask, From: somebody, FromName: SystemName, Region: region}, SenderObject},
		{"the grid's message as an object's", IM{Dialog: DialogFromTask, From: somebody, FromName: SystemName}, SenderGrid},
		{"an object's item", IM{Dialog: DialogTaskInventoryOffered, From: somebody, FromName: "Some Body"}, SenderObject},
		{"an object's alert", IM{Dialog: DialogFromTaskAsAlert, From: somebody, FromName: "Some Body"}, SenderObject},
		{"a web page to open", IM{Dialog: DialogGotoURL, From: somebody, FromName: "Some Body"}, SenderGrid},
		{"a group invitation", IM{Dialog: DialogGroupInvitation, From: somebody, FromName: "some.body", Group: true}, SenderPerson},
		{"a group notice", IM{Dialog: DialogGroupNotice, From: somebody, FromName: "Some Body", Group: true}, SenderPerson},
		{"an inventory offer", IM{Dialog: DialogInventoryOffered, From: somebody, FromName: "Some Body"}, SenderPerson},
		{"a teleport offer", IM{Dialog: DialogTeleportLure, From: somebody, FromName: "Some Body"}, SenderPerson},
	} {
		if got := c.im.Sender(); got != c.want {
			t.Errorf("%s: Sender() = %d, want %d", c.name, got, c.want)
		}
	}

	// And chat, by the source type the simulator gives it.
	for st, want := range map[uint8]Sender{SourceAgent: SenderPerson, SourceObject: SenderObject, SourceSystem: SenderGrid} {
		if got := (Line{SourceType: st, From: "Some Body"}).Sender(); got != want {
			t.Errorf("chat of source type %d: Sender() = %d, want %d", st, got, want)
		}
	}
}

// TestLabelPutsTheKindInFront, and leaves a person's name alone.
func TestLabelPutsTheKindInFront(t *testing.T) {
	t.Parallel()
	for k, want := range map[Sender]string{
		SenderPerson: "Some Body",
		SenderObject: "[Object] Some Body",
		SenderGroup:  "[Group] Some Body",
		SenderGrid:   "[Grid] Some Body",
	} {
		if got := k.Label("Some Body"); got != want {
			t.Errorf("Label = %q, want %q", got, want)
		}
	}
}

// TestTheGridIsNotSomebodyTalking: a message signed by the grid is not
// conversation, whatever id it carries, so nothing answers it or opens a
// conversation with it.
func TestTheGridIsNotSomebodyTalking(t *testing.T) {
	t.Parallel()
	somebody := msg.MustParseUUID("876e7e57-7e57-c0de-8597-66b760a8cb5f")
	im := &IM{Dialog: DialogMessage, From: somebody, FromName: SystemName, Text: "the region restarts in 5 minutes"}
	if im.Spoken() || im.Conversation() {
		t.Error("a message signed by the grid was taken for somebody talking")
	}
	im.FromName = "Some Body"
	if !im.Conversation() {
		t.Error("a person's message is not conversation")
	}
}

// bareObjectNamesIn finds every read of an ObjectName that is neither
// inside a call to Label nor its reading off the wire in trimNul.
func bareObjectNamesIn(fset *token.FileSet, f *ast.File) []string {
	var out []string
	var walk func(n ast.Node, covered bool)
	walk = func(n ast.Node, covered bool) {
		ast.Inspect(n, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				name := ""
				switch fn := x.Fun.(type) {
				case *ast.SelectorExpr:
					name = fn.Sel.Name
				case *ast.Ident:
					name = fn.Name
				}
				if name == "Label" || name == "trimNul" {
					for _, a := range x.Args {
						walk(a, true)
					}
					walk(x.Fun, covered)
					return false
				}
			case *ast.SelectorExpr:
				if pkg, ok := x.X.(*ast.Ident); ok && pkg.Name == "msg" {
					return false // msg.ObjectName, the message
				}
				if x.Sel.Name == "ObjectName" && !covered {
					out = append(out, fmt.Sprint(fset.Position(x.Pos())))
				}
			}
			return true
		})
	}
	walk(f, false)
	return out
}

// TestNoObjectNameIsPrintedBare: an object's name is whatever its owner
// typed, a person's included, so it is printed only through
// SenderObject.Label, which says it is an object's.  A read of one in
// sl, slsh or slbotd outside a call to Label fails this test.
// Why: doc/im-senders.md#labelling-a-sender
func TestNoObjectNameIsPrintedBare(t *testing.T) {
	fset := token.NewFileSet()
	for _, dir := range []string{".", "../cmd/slsh", "../cmd/slbotd"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		if len(files) == 0 {
			t.Fatalf("no Go files in %s; this test is looking in the wrong place", dir)
		}
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			f, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			for _, at := range bareObjectNamesIn(fset, f) {
				t.Errorf("%s: an object's name is used without SenderObject.Label, "+
					"which would print it as though a person had said it", at)
			}
		}
	}
}

// TestTheBareObjectNameCheckFindsOne: a check that found nothing would
// pass on a tree that did it everywhere.
func TestTheBareObjectNameCheckFindsOne(t *testing.T) {
	const src = `package p

func a(d Dialog, m *msg.ScriptDialog) {
	_ = SenderObject.Label(d.ObjectName)
	_ = sl.SenderObject.Label(orID(d.ObjectName, d.Object))
	_ = Dialog{ObjectName: trimNul(m.Data.ObjectName)}
	_ = &msg.ObjectName{}
	fmt.Printf("%s asks", d.ObjectName)
	return d.ObjectName
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(bareObjectNamesIn(fset, f), ","), "p.go:8:24,p.go:9:9"; got != want {
		t.Errorf("found %q, want %q", got, want)
	}
}
