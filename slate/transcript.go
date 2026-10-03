package slate

// What the run prints: event lines, step results and the failure block.
// The shapes are those of doc/slate-language.md#reading-the-result. They
// are not frozen until PR 8.

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

const stampLayout = "15:04:05.000"

// liveOut is the transcript so far, and, when live is set, a copy of
// each write made to it as it is made (Options.Out).
type liveOut struct {
	strings.Builder
	live io.Writer
}

func (o *liveOut) Write(p []byte) (int, error) {
	o.Builder.Write(p)
	if o.live != nil {
		o.live.Write(p) // a failing Out must not end the run
	}
	return len(p), nil
}

func (o *liveOut) WriteString(s string) (int, error) { return o.Write([]byte(s)) }

func (r *runner) printf(format string, args ...any) {
	fmt.Fprintf(&r.out, format+"\n", args...)
}

// printEvent writes an event's line, with the time it was observed.
func (r *runner) printEvent(ev *event) {
	if ev.text != "" {
		fmt.Fprintf(&r.out, "%s %s\n", ev.at.UTC().Format(stampLayout), ev.text)
	}
}

// whoPrim is the script's name for a prim that a binding holds, by its id
// or, when is is not nil, by a name is accepts; or "".
func (r *runner) whoPrim(id msg.UUID, is func(string) bool) string {
	var names []string
	for n := range r.bind {
		names = append(names, n)
	}
	if r.cur != nil {
		for n := range r.cur.as {
			names = append(names, n)
		}
	}
	best := ""
	for _, n := range names {
		b := r.lookup(n)
		if b != nil && (b.owns(id) || (is != nil && b.nameIs(is))) && (best == "" || n < best) {
			best = n
		}
	}
	return best
}

// chatText is the transcript line of a heard line: how, who and what.
// A name that is not a bound prim's is the displayed name, and an object's
// is labelled as one, since anything can be called anything.
func (r *runner) chatText(l sl.Line) string {
	how := sl.ChatTypeName(l.Type)
	if isPublic(l.Type) {
		how = "public"
	}
	who := r.whoPrim(l.Source, nil)
	if who == "" {
		who = l.From
		if l.FromObject() {
			who = sl.SenderObject.Label(l.From)
		}
	}
	return fmt.Sprintf("chat %s from %s: %q", how, who, l.Text)
}

func (r *runner) dialogText(d sl.Dialog) string {
	who := r.whoPrim(d.Object, func(n string) bool { return n != "" && n == d.ObjectName })
	if who == "" {
		who = sl.SenderObject.Label(d.ObjectName)
	}
	if d.IsTextBox() {
		return fmt.Sprintf("textbox from %s: %q", who, d.Message)
	}
	return fmt.Sprintf("dialog from %s: %q buttons %s", who, d.Message, quoteAll(d.Buttons))
}

// failBlock is a failed step, kept until the test ends so that the
// dialog left unanswered can be filled in.
type failBlock struct {
	file, test string
	phase      Phase
	n          int
	l1, l2     int
	via        string
	stimulus   string
	sent       string
	exps       []string
	waited     time.Duration
	heard      []string
	unanswered string
	seated     string
	payment    string
	captured   string
	why        string
}

// block describes a step that failed.
func (s *stepRun) block() *failBlock {
	sc := s.r.s
	b := &failBlock{
		file: sc.File, test: s.t.et.Test.Name, phase: s.x.Phase, n: s.n,
		sent: s.sent, stimulus: "(none)",
	}
	if !s.start.IsZero() {
		b.waited = s.end.Sub(s.start)
	}
	sp := s.st.Span
	b.l1 = sp.Line
	b.l2 = sp.Line + strings.Count(sc.Source(sp), "\n")
	if len(s.x.Via) > 0 {
		b.via = Via(s.x.Via)
	}
	b.why = s.why
	b.seated, b.payment, b.captured = s.r.seatedText(), s.payment, s.t.capturedLine()
	if b.payment == "" {
		b.payment = "none"
	}
	if st := s.st.Stimulus; st != nil {
		b.stimulus = strings.Join(strings.Fields(sc.Source(st.Span)), " ")
	}
	for _, e := range s.exps {
		b.exps = append(b.exps, e.describe())
	}
	for _, ev := range s.r.log.events[s.t.mark:] {
		if ev.text != "" && !ev.at.Before(s.arm) && !ev.at.After(s.end) {
			b.heard = append(b.heard, ev.at.UTC().Format(stampLayout)+" "+ev.text)
		}
	}
	return b
}

// describe is an expectation's line in the failure block: matched,
// unmatched or forbidden, the source, and for an event the time and what
// was seen. A negative that held counts as matched.
func (e *expState) describe() string {
	switch {
	case e.forbidden:
		return fmt.Sprintf("forbidden %s at %s: %s", e.text, e.ev.at.UTC().Format(stampLayout), e.ev.text)
	case e.matched:
		return fmt.Sprintf("matched %s at %s: %s", e.text, e.ev.at.UTC().Format(stampLayout), e.ev.text)
	case e.note != "":
		return "unmatched " + e.text + e.note
	case e.neg && !e.limit.IsZero() && !time.Now().Before(e.limit):
		return "matched " + e.text
	case e.neg:
		return "unmatched " + e.text + " (window still open)"
	}
	if e.noteFn != nil {
		return "unmatched " + e.text + e.noteFn()
	}
	return "unmatched " + e.text
}

func (b *failBlock) render() string {
	var w strings.Builder
	phase := ""
	if b.phase == PhaseAfter {
		phase = " after each"
	}
	via := ""
	if b.via != "" {
		via = " " + b.via
	}
	fmt.Fprintf(&w, "slate: fail %s test %q%s step %d lines %d-%d%s\n", b.file, b.test, phase, b.n, b.l1, b.l2, via)
	fmt.Fprintf(&w, "  stimulus: %s\n", b.stimulus)
	if b.sent != "" {
		fmt.Fprintf(&w, "    %s\n", b.sent)
	}
	w.WriteString("  expectations:\n")
	for _, e := range b.exps {
		fmt.Fprintf(&w, "    %s\n", e)
	}
	if len(b.exps) == 0 {
		w.WriteString("    (none)\n")
	}
	if b.why != "" {
		fmt.Fprintf(&w, "  failed: %s\n", b.why)
	}
	fmt.Fprintf(&w, "  waited: %s\n", b.waited.Round(10*time.Millisecond))
	w.WriteString("  heard during the step:\n")
	for _, h := range b.heard {
		fmt.Fprintf(&w, "    %s\n", h)
	}
	if len(b.heard) == 0 {
		w.WriteString("    (none)\n")
	}
	fmt.Fprintf(&w, "  seated: %s\n", b.seated)
	fmt.Fprintf(&w, "  dialog left unanswered: %s\n", b.unanswered)
	fmt.Fprintf(&w, "  payment: %s\n", b.payment)
	fmt.Fprintf(&w, "  captured: %s\n", b.captured)
	return w.String()
}
