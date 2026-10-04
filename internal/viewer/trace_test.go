package viewer

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

func packet(seq uint32, m msg.Message) *msg.Packet {
	return &msg.Packet{
		Header:  msg.Header{Sequence: seq, Flags: msg.FlagReliable},
		ID:      msg.IDOf(m),
		Message: m,
		At:      time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC),
	}
}

func TestDirectionNames(t *testing.T) {
	for _, c := range []struct {
		d    Direction
		want string
	}{
		{FromSim, "sim>slgod"},
		{ToSim, "slgod>sim"},
		{FromViewer, "viewer>slgod"},
		{ToViewer, "slgod>viewer"},
	} {
		if got := c.d.String(); got != c.want {
			t.Errorf("Direction(%d) = %q, want %q", uint8(c.d), got, c.want)
		}
	}
}

// TestCensusAnswersWhatIsMissing is the question the census exists for.
// A viewer stuck on a grey screen is waiting for one message; the census
// has to be able to say that message never went that way, and to
// distinguish that from it having gone and been dropped.
func TestCensusAnswersWhatIsMissing(t *testing.T) {
	c := NewCensus()
	now := time.Now()

	c.Record("RegionHandshake", ToViewer, now, Forwarded)
	c.Record("LayerData", FromSim, now, Forwarded)
	c.Record("LayerData", ToViewer, now, NoViewer)

	// Sent, but never to the viewer -- a bug that looks identical to
	// the next case from inside the viewer, and must not look
	// identical here.
	if !c.Seen("LayerData", ToViewer) {
		t.Error("LayerData toward the viewer was recorded and should be seen")
	}
	// Never sent at all.
	if c.Seen("AgentMovementComplete", ToViewer) {
		t.Error("AgentMovementComplete was never recorded, so it must not be seen")
	}

	rep := c.Report()
	if !strings.Contains(rep, "no viewer attached 1") {
		t.Errorf("report should say why LayerData did not reach the viewer:\n%s", rep)
	}
	if strings.Contains(rep, "AgentMovementComplete") {
		t.Errorf("report invented a message that never arrived:\n%s", rep)
	}
	if c.Total() != 3 {
		t.Errorf("Total = %d, want 3", c.Total())
	}
}

// TestCensusSeparatesDirections: the same message going two ways is two
// rows, because "the simulator sent it" and "the viewer was told" are
// different facts and confusing them is the whole failure mode.
func TestCensusSeparatesDirections(t *testing.T) {
	c := NewCensus()
	now := time.Now()
	for i := 0; i < 3; i++ {
		c.Record("ObjectUpdate", FromSim, now, Forwarded)
	}
	c.Record("ObjectUpdate", ToViewer, now, Forwarded)

	rows := c.Counts()
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want one per direction: %+v", len(rows), rows)
	}
	byDir := map[Direction]uint64{}
	for _, r := range rows {
		byDir[r.Dir] = r.Packets
	}
	if byDir[FromSim] != 3 || byDir[ToViewer] != 1 {
		t.Errorf("counts by direction = %v, want 3 from the sim and 1 to the viewer", byDir)
	}
}

// TestCensusCountsWhatWasNotForwarded: a message the relay did not pass
// on shows up under what became of it, rather than as a silent absence.
func TestCensusCountsWhatWasNotForwarded(t *testing.T) {
	c := NewCensus()
	now := time.Now()
	c.Record("ObjectUpdate", FromSim, now, Dropped)
	c.Record("ObjectUpdate", FromSim, now, Dropped)

	rep := c.Report()
	if !strings.Contains(rep, "dropped, viewer behind 2") {
		t.Errorf("report should say what became of them:\n%s", rep)
	}
	rows := c.Counts()
	if len(rows) != 1 || rows[0].By[Dropped] != 2 {
		t.Errorf("rows = %+v", rows)
	}
}

// TestEveryDispositionIsOneSomethingRecords: a disposition nothing
// records is one the census never shows, which a reader takes for a
// check that runs.  Each one declared is passed to a call somewhere
// outside this file, in this package or in cmd/slgod, which are what
// record them.
func TestEveryDispositionIsOneSomethingRecords(t *testing.T) {
	fset := token.NewFileSet()
	parse := func(file string) *ast.File {
		t.Helper()
		f, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}

	// Every constant in the block whose first is a Disposition.
	var declared []string
	for _, decl := range parse("trace.go").Decls {
		g, ok := decl.(*ast.GenDecl)
		if !ok || g.Tok != token.CONST || len(g.Specs) == 0 {
			continue
		}
		if typ, ok := g.Specs[0].(*ast.ValueSpec).Type.(*ast.Ident); !ok || typ.Name != "Disposition" {
			continue
		}
		for _, spec := range g.Specs {
			for _, name := range spec.(*ast.ValueSpec).Names {
				declared = append(declared, name.Name)
			}
		}
	}
	if len(declared) != len(dispositions) {
		t.Errorf("%d dispositions are declared, %v, and the report lists %d", len(declared), declared, len(dispositions))
	}

	// What is passed to a call: Dropped here, viewer.Dropped in slgod.
	passed := map[string]bool{}
	for _, pattern := range []string{"*.go", "../../cmd/slgod/*.go"} {
		files, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") || file == "trace.go" {
				continue
			}
			ast.Inspect(parse(file), func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				for _, arg := range call.Args {
					switch a := arg.(type) {
					case *ast.Ident:
						passed[a.Name] = true
					case *ast.SelectorExpr:
						if pkg, ok := a.X.(*ast.Ident); ok && pkg.Name == "viewer" {
							passed[a.Sel.Name] = true
						}
					}
				}
				return true
			})
		}
	}
	for _, name := range declared {
		if !passed[name] {
			t.Errorf("nothing records %s", name)
		}
	}
}

func TestCensusReportIsStable(t *testing.T) {
	build := func() string {
		c := NewCensus()
		now := time.Now()
		c.Record("ObjectUpdate", FromSim, now, Forwarded)
		c.Record("AgentUpdate", FromViewer, now, Forwarded)
		c.Record("LayerData", FromSim, now, Forwarded)
		return c.Report()
	}
	if a, b := build(), build(); a != b {
		t.Errorf("report is not deterministic:\n%s\n---\n%s", a, b)
	}
	// Sorted by name, so AgentUpdate precedes LayerData precedes
	// ObjectUpdate whatever order they arrived in.
	rep := build()
	ai := strings.Index(rep, "AgentUpdate")
	li := strings.Index(rep, "LayerData")
	oi := strings.Index(rep, "ObjectUpdate")
	if !(ai < li && li < oi) {
		t.Errorf("rows are not sorted by name:\n%s", rep)
	}
}

// TestTraceKeepsBothCircuitsInOneOrder is why there is one stream and
// not two: the useful question is whether slgod forwarded a thing before
// or after the viewer asked for it, and two files with two clocks cannot
// answer it.
func TestTraceKeepsBothCircuitsInOneOrder(t *testing.T) {
	var buf bytes.Buffer
	tr := NewTrace(&buf, nil, false)

	tr.Write(FromViewer, packet(1, &msg.CompletePingCheck{}), Forwarded)
	tr.Write(ToSim, packet(9, &msg.CompletePingCheck{}), Forwarded)
	tr.Write(FromSim, packet(2, &msg.StartPingCheck{}), Absorbed)

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3:\n%s", len(lines), buf.String())
	}
	for i, want := range []string{"viewer>slgod", "slgod>sim", "sim>slgod"} {
		if !strings.Contains(lines[i], want) {
			t.Errorf("line %d = %q, want it to be the %s entry", i, lines[i], want)
		}
	}
	if !strings.Contains(lines[2], "absorbed") {
		t.Errorf("disposition missing from %q", lines[2])
	}
	if w, s := tr.Stats(); w != 3 || s != 0 {
		t.Errorf("stats = %d written, %d skipped; want 3 and 0", w, s)
	}
}

// TestTraceFilterReportsWhatItDropped: a trace that looks empty is
// usually a filter naming a message that never came, and the skipped
// count is what says so rather than leaving it looking like silence.
func TestTraceFilterReportsWhatItDropped(t *testing.T) {
	var buf bytes.Buffer
	tr := NewTrace(&buf, []string{"StartPingCheck"}, false)

	tr.Write(FromSim, packet(1, &msg.CompletePingCheck{}), Forwarded)
	tr.Write(FromSim, packet(2, &msg.StartPingCheck{}), Forwarded)
	tr.Write(FromSim, packet(3, &msg.CompletePingCheck{}), Forwarded)

	if got := strings.Count(buf.String(), "\n"); got != 1 {
		t.Errorf("wrote %d lines, want only the named message:\n%s", got, buf.String())
	}
	if !strings.Contains(buf.String(), "StartPingCheck") {
		t.Errorf("the named message is missing:\n%s", buf.String())
	}
	w, s := tr.Stats()
	if w != 1 || s != 2 {
		t.Errorf("stats = %d written, %d skipped; want 1 and 2", w, s)
	}
}

func TestTraceBodiesAreYAML(t *testing.T) {
	var buf bytes.Buffer
	tr := NewTrace(&buf, nil, true)
	tr.Write(ToViewer, packet(7, &msg.CompletePingCheck{}), Forwarded)

	out := buf.String()
	for _, want := range []string{"---", "direction: slgod>viewer", "disposition: forwarded", "sequence: 7"} {
		if !strings.Contains(out, want) {
			t.Errorf("trace entry missing %q:\n%s", want, out)
		}
	}
}

// TestTraceBodiesLeaveOutTheAccountDetails: a trace that writes bodies
// writes a UserInfoReply as a line, with no email in it.
// Why: doc/account.md#nothing-logs-it
func TestTraceBodiesLeaveOutTheAccountDetails(t *testing.T) {
	var buf bytes.Buffer
	tr := NewTrace(&buf, nil, true)
	m := &msg.UserInfoReply{}
	m.UserData.EMail = []byte("somebody@example.invalid\x00")
	tr.Write(FromSim, packet(3, m), Forwarded)

	out := buf.String()
	if !strings.Contains(out, "UserInfoReply") {
		t.Errorf("the entry is missing:\n%s", out)
	}
	if strings.Contains(out, "example.invalid") || strings.Contains(out, "EMail") {
		t.Error("the trace wrote the email address")
	}
}

// TestTraceNilIsUsable: a caller holds a Trace whether or not tracing
// was asked for, so the zero case has to be silent rather than fatal.
func TestTraceNilIsUsable(t *testing.T) {
	var tr *Trace
	tr.Write(FromSim, packet(1, &msg.CompletePingCheck{}), Forwarded)
	if w, s := tr.Stats(); w != 0 || s != 0 {
		t.Errorf("a nil trace counted %d/%d", w, s)
	}
}

// TestTraceSurvivesAFailingWriter: the session being diagnosed is worth
// more than the record of it, so a write error stops the trace and not
// the relay.
func TestTraceSurvivesAFailingWriter(t *testing.T) {
	tr := NewTrace(failingWriter{}, nil, false)
	tr.Write(FromSim, packet(1, &msg.CompletePingCheck{}), Forwarded)
	tr.Write(FromSim, packet(2, &msg.CompletePingCheck{}), Forwarded)
	if w, _ := tr.Stats(); w != 0 {
		t.Errorf("wrote %d entries through a broken writer", w)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errBroken }

var errBroken = &brokenError{}

type brokenError struct{}

func (*brokenError) Error() string { return "broken" }

// TestConcurrentWriters is not decoration: the two circuits' dispatch
// goroutines, their send goroutines and the pump all write to the same
// census and the same trace, so this is the ordinary case rather than an
// edge one.  Run
// under -race, it is the only thing that actually checks the claim.
func TestConcurrentWriters(t *testing.T) {
	const writers, each = 8, 200

	c := NewCensus()
	var buf bytes.Buffer
	tr := NewTrace(&buf, nil, false)

	done := make(chan struct{})
	for w := 0; w < writers; w++ {
		go func(w int) {
			defer func() { done <- struct{}{} }()
			dir := directions[w%len(directions)]
			for i := 0; i < each; i++ {
				p := packet(uint32(i), &msg.CompletePingCheck{})
				c.Record("CompletePingCheck", dir, p.At, Forwarded)
				tr.Write(dir, p, Forwarded)
			}
		}(w)
	}
	for w := 0; w < writers; w++ {
		<-done
	}

	if got := c.Total(); got != writers*each {
		t.Errorf("census total = %d, want %d", got, writers*each)
	}
	if w, _ := tr.Stats(); w != writers*each {
		t.Errorf("trace wrote %d entries, want %d", w, writers*each)
	}
	// Every line must be whole: interleaved writes would tear one.
	for i, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if !strings.Contains(line, "CompletePingCheck") {
			t.Fatalf("line %d is torn: %q", i, line)
		}
	}
}

// TestMessageNameFallsBack: a packet that would not decode still has a
// number, and that number is the one detail worth writing down -- it
// says which message this build is missing.
func TestMessageNameFallsBack(t *testing.T) {
	unknown := &msg.Packet{ID: msg.MakeID(msg.FreqHigh, 253)}
	if got := MessageName(unknown); !strings.Contains(got, "253") {
		t.Errorf("MessageName(unknown) = %q, want it to name the number", got)
	}
	if got := MessageName(&msg.Packet{Acks: []uint32{1}}); got != "(acks only)" {
		t.Errorf("MessageName(acks) = %q", got)
	}
	if got := MessageName(&msg.Packet{}); got != "(empty)" {
		t.Errorf("MessageName(empty) = %q", got)
	}
	named := packet(1, &msg.CompletePingCheck{})
	if got := MessageName(named); got != "CompletePingCheck" {
		t.Errorf("MessageName = %q", got)
	}
}
