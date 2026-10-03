package slate

// Dialogs and text boxes: the expectation, the hold a match leaves, and
// the choose and answer stimuli that use it.
// Why: doc/slate-runner.md#dialog-hold

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/sl"
)

// hold is the dialog a binding is holding for choose and answer. It is
// not dropped when the step that matched it ends: choose and answer are
// stimuli, a stimulus begins a new step, and there would be nothing left
// to answer.
type hold struct {
	binding string
	d       sl.Dialog
}

// holdDialog keeps d for the binding, forgetting the one it replaces.
func (t *testRun) holdDialog(name string, d sl.Dialog) {
	if old := t.holds[name]; old != nil {
		t.r.sess.ForgetDialog(old.d)
	}
	t.holds[name] = &hold{binding: name, d: d}
}

// dropHolds forgets every dialog still held, at the end of a test, and
// says what was left for the failure block. There is no decline to send.
func (t *testRun) dropHolds() string {
	var left []string
	for _, h := range t.orderedHolds() {
		left = append(left, describeHeld(h.d))
		t.r.sess.ForgetDialog(h.d)
	}
	t.holds = map[string]*hold{}
	if len(left) == 0 {
		return "no"
	}
	return strings.Join(left, "; ")
}

// orderedHolds is the holds oldest first, so a report does not depend on
// map order.
func (t *testRun) orderedHolds() []*hold {
	var out []*hold
	for _, h := range t.holds {
		out = append(out, h)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].d.At.Before(out[j-1].d.At); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// describeHeld is a held dialog for the failure block, its object's name
// labelled as an object's.
// Why: doc/im-senders.md#labelling-a-sender
func describeHeld(d sl.Dialog) string {
	from := sl.SenderObject.Label(strconv.Quote(d.ObjectName))
	if d.IsTextBox() {
		return fmt.Sprintf("%s textbox %q", from, d.Message)
	}
	return fmt.Sprintf("%s %q buttons %s", from, d.Message, quoteAll(d.Buttons))
}

func quoteAll(ss []string) string {
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = fmt.Sprintf("%q", s)
	}
	return strings.Join(q, " ")
}

func fold(s string) string { return strings.TrimSpace(strings.ToLower(s)) }

// dialogExpect makes expect dialog (dx set) or expect textbox (dx nil).
// The dialog's Object is the id of the binding's prim, or its ObjectName
// the prim's name; the message is exact or a pattern. Every listed button
// must be present by Dialog.Button, and with only the set is exactly the
// list. A text box matches textbox and not dialog. The match is held.
// Why: doc/slate-runner.md#dialog-and-text-box
func (s *stepRun) dialogExpect(x *expState, name Ident, link *Int, text Text, dx *DialogExp) error {
	b := s.r.lookup(name.Text)
	if b == nil {
		return fmt.Errorf("%s is not an object", name.Text)
	}
	// With a probe the link is its prim now; without, it is the prim at
	// that number when the dialog arrives.
	var prim *binding
	if link != nil && s.r.probed(b) {
		var err error
		if prim, err = s.r.linkBinding(s.r.ctx, nil, b, link.Value); err != nil {
			return err
		}
	}
	linkWhy := ""
	msgOK := textMatch{ok: func(string) bool { return true }}
	if dx == nil || dx.HasText {
		var err error
		if msgOK, err = s.textMatch(text); err != nil {
			return err
		}
	}
	var cl []clauseMatch
	if dx != nil {
		var err error
		if cl, err = s.clauseMatchers(dx); err != nil {
			return err
		}
	}
	why := "" // why the last dialog that met the object and message did not match
	x.noteFn = func() string {
		if linkWhy != "" {
			return fmt.Sprintf("; slate: step %d: %s", s.n, linkWhy)
		}
		if why == "" {
			return ""
		}
		return "; " + why
	}
	x.match = func(ev *event) bool {
		if ev.kind != evDialog {
			return false
		}
		d := ev.dialog
		if d.IsTextBox() != (dx == nil) {
			return false
		}
		if link != nil && prim == nil {
			lb, err := s.r.linkBinding(s.r.ctx, nil, b, link.Value)
			if err != nil {
				linkWhy = err.Error()
				return false
			}
			linkWhy = ""
			if d.Object != lb.seen.ID {
				return false
			}
		} else if prim != nil {
			// The object is that prim, not any prim of the linkset.
			if d.Object != prim.seen.ID {
				return false
			}
		} else if !b.owns(d.Object) && !b.nameIs(func(n string) bool { return n == d.ObjectName }) {
			return false
		}
		if !msgOK.match(d.Message) {
			return false
		}
		if dx == nil {
			return true
		}
		_, why = assignButtons(dx, cl, d.Buttons)
		return why == ""
	}
	if !x.neg {
		x.onMatch = func(ev *event) {
			s.t.holdDialog(name.Text, ev.dialog)
			v := capValue{typ: CapText, text: ev.dialog.Message}
			srcs := []groupSrc{{msgOK, ev.dialog.Message}}
			if dx != nil {
				as, _ := assignButtons(dx, cl, ev.dialog.Buttons)
				for i, c := range cl {
					if c.groups.re != nil && as != nil {
						srcs = append(srcs, groupSrc{c.groups, strings.TrimSpace(ev.dialog.Buttons[as[i]])})
					}
				}
			}
			s.bindMatched(x, &v, srcs...)
		}
	}
	return nil
}

// held is the dialog held for a binding, or why there is none.
func (s *stepRun) held(name string) (*hold, error) {
	h := s.t.holds[name]
	if h == nil {
		return nil, fmt.Errorf("no dialog is held for %s", name)
	}
	return h, nil
}

// chooseStimulus presses a button on the held dialog. A label that folds
// to two buttons, a label that is none, a pattern that matches none or
// several, a number past the last button, no hold, and a held text box all
// fail the step before anything is sent.
func (s *stepRun) chooseStimulus(c *Choose) *stimulus {
	var h *hold
	var index int
	return &stimulus{
		prepare: func(context.Context) error {
			var err error
			if h, err = s.held(c.Name.Text); err != nil {
				return err
			}
			if h.d.IsTextBox() {
				return fmt.Errorf("the dialog held for %s is a text box; use answer", c.Name.Text)
			}
			bs := h.d.Buttons
			switch c.Kind {
			case ChooseButton:
				n := int(c.Index.Value)
				if n < 1 || n > len(bs) {
					return fmt.Errorf("there is no button %d on the dialog held for %s: %s", n, c.Name.Text, quoteAll(bs))
				}
				index = n - 1
				return nil
			case ChooseMatching:
				re, err := regexp.Compile(c.Label)
				if err != nil {
					return err
				}
				var hits []int
				for i, b := range bs {
					if re.MatchString(strings.TrimSpace(b)) {
						hits = append(hits, i)
					}
				}
				switch len(hits) {
				case 1:
					index = hits[0]
					return nil
				case 0:
					return fmt.Errorf("no button matches %q on the dialog held for %s: %s", c.Label, c.Name.Text, quoteAll(bs))
				}
				return fmt.Errorf("%d buttons match %q on the dialog held for %s: %s", len(hits), c.Label, c.Name.Text, quoteAll(bs))
			}
			label := c.Label
			if c.Kind == ChooseCapture {
				v, err := s.capture(c.Use, CapText)
				if err != nil {
					return err
				}
				label = v.text
			}
			var same []int
			for i, b := range bs {
				if fold(b) == fold(label) {
					same = append(same, i)
				}
			}
			switch {
			case len(same) > 1:
				var names []string
				for _, i := range same {
					names = append(names, bs[i])
				}
				return fmt.Errorf("%d buttons are called %q (%s); a label must name one", len(same), label, quoteAll(names))
			case len(same) == 0:
				return fmt.Errorf("%q is not one of the buttons of the dialog held for %s: %s", label, c.Name.Text, quoteAll(bs))
			}
			index = same[0]
			return nil
		},
		send: func(ctx context.Context, _ time.Duration) (string, error) {
			label := h.d.Buttons[index]
			if err := s.r.sess.AnswerIndex(ctx, h.d, index); err != nil {
				return "", err
			}
			delete(s.t.holds, c.Name.Text)
			return fmt.Sprintf("chose %q on the dialog held for %s", label, c.Name.Text), nil
		},
	}
}

// answerStimulus types into the held text box.
func (s *stepRun) answerStimulus(a *Answer) *stimulus {
	var h *hold
	return &stimulus{
		prepare: func(context.Context) error {
			var err error
			if h, err = s.held(a.Name.Text); err != nil {
				return err
			}
			if !h.d.IsTextBox() {
				return fmt.Errorf("the dialog held for %s is not a text box; its buttons are %s", a.Name.Text, quoteAll(h.d.Buttons))
			}
			return nil
		},
		send: func(ctx context.Context, _ time.Duration) (string, error) {
			if err := s.r.sess.AnswerText(ctx, h.d, a.Text); err != nil {
				return "", err
			}
			delete(s.t.holds, a.Name.Text)
			return fmt.Sprintf("answered %q on the text box held for %s", a.Text, a.Name.Text), nil
		},
	}
}

// clauseMatch is one button clause made ready: ok judges a label, groups
// binds the named groups of its pattern, desc names it for a failure.
type clauseMatch struct {
	ok     func(label string) bool
	groups textMatch
	nth    int // 1-based, or 0
	desc   string
}

// clauseMatchers makes the button clauses of an expectation ready. A
// literal and a capture compare as Dialog.Button does, a pattern is
// unanchored against the trimmed label.
func (s *stepRun) clauseMatchers(dx *DialogExp) ([]clauseMatch, error) {
	var out []clauseMatch
	for _, c := range dx.Clauses {
		cm := clauseMatch{desc: "button"}
		if c.Nth != nil {
			cm.nth = int(c.Nth.Value)
			cm.desc = fmt.Sprintf("button %d", cm.nth)
		}
		switch {
		case c.Text.Capture != nil:
			v, err := s.capture(c.Text.Capture, CapText)
			if err != nil {
				return nil, err
			}
			want := fold(v.text)
			cm.ok = func(l string) bool { return fold(l) == want }
			cm.desc += " " + c.Text.Capture.String()
		case c.Text.Pattern:
			m, err := textMatcher(c.Text)
			if err != nil {
				return nil, err
			}
			cm.groups = m
			cm.ok = func(l string) bool { return m.match(strings.TrimSpace(l)) }
			cm.desc += fmt.Sprintf(" matching %q", c.Text.Value)
		default:
			want := fold(c.Text.Value)
			cm.ok = func(l string) bool { return fold(l) == want }
			cm.desc += fmt.Sprintf(" %q", c.Text.Value)
		}
		out = append(out, cm)
	}
	return out, nil
}

// assignButtons gives each clause its own button by a maximum bipartite
// matching (augmenting paths), so that a clause that could take several
// buttons does not starve a later one. It returns the button of each
// clause, and why the dialog does not match, or "" when it does.
func assignButtons(dx *DialogExp, cl []clauseMatch, buttons []string) ([]int, string) {
	if dx.Count != nil && int(dx.Count.Value) != len(buttons) {
		return nil, fmt.Sprintf("count %d but the dialog has %d %s", dx.Count.Value, len(buttons), plural(len(buttons), "button", "buttons"))
	}
	adj := make([][]int, len(cl))
	for i, c := range cl {
		for j, b := range buttons {
			if (c.nth == 0 || c.nth == j+1) && c.ok(b) {
				adj[i] = append(adj[i], j)
			}
		}
	}
	owner := make([]int, len(buttons)) // clause that holds each button, or -1
	for j := range owner {
		owner[j] = -1
	}
	var try func(i int, seen []bool) bool
	try = func(i int, seen []bool) bool {
		for _, j := range adj[i] {
			if seen[j] {
				continue
			}
			seen[j] = true
			if owner[j] < 0 || try(owner[j], seen) {
				owner[j] = i
				return true
			}
		}
		return false
	}
	for i := range cl {
		if !try(i, make([]bool, len(buttons))) {
			how := "has no button"
			if len(adj[i]) > 0 {
				how = "has no button of its own"
			}
			return nil, fmt.Sprintf("clauses could not all be assigned: %s %s", cl[i].desc, how)
		}
	}
	as := make([]int, len(cl))
	for j, i := range owner {
		if i >= 0 {
			as[i] = j
		} else if dx.Only {
			return nil, fmt.Sprintf("only: %q is not claimed by a clause", strings.TrimSpace(buttons[j]))
		}
	}
	var why []string
	if dx.Ordered {
		if o := orderedAssignment(adj, len(buttons)); o != nil {
			as = o
		} else {
			why = append(why, firstOutOfOrder(as, buttons))
		}
	}
	if dx.Sorted != nil {
		if w := sortedWhy(dx.Sorted, buttons); w != "" {
			why = append(why, w)
		}
	}
	if len(why) > 0 {
		return nil, strings.Join(why, "; ")
	}
	return as, ""
}

// orderedAssignment looks for an assignment of one button to each clause,
// every clause after the one before it in button order, or nil. adj holds
// the buttons each clause can take; clauses and buttons are few, and a
// clause and the last button used decide the rest, so a failure is
// remembered.
func orderedAssignment(adj [][]int, nb int) []int {
	as := make([]int, len(adj))
	dead := make([][]bool, len(adj)) // dead[i][last+1]: no way on from clause i
	for i := range dead {
		dead[i] = make([]bool, nb+1)
	}
	var walk func(i, last int) bool
	walk = func(i, last int) bool {
		if i == len(adj) {
			return true
		}
		if dead[i][last+1] {
			return false
		}
		for _, j := range adj[i] {
			if j > last {
				as[i] = j
				if walk(i+1, j) {
					return true
				}
			}
		}
		dead[i][last+1] = true
		return false
	}
	if !walk(0, -1) {
		return nil
	}
	return as
}

// firstOutOfOrder names the first pair of clauses whose buttons are the
// wrong way round in the assignment the matcher found.
func firstOutOfOrder(as []int, buttons []string) string {
	for i := range as {
		for k := i + 1; k < len(as); k++ {
			if as[k] < as[i] {
				return fmt.Sprintf("ordered: %q comes before %q", strings.TrimSpace(buttons[as[k]]), strings.TrimSpace(buttons[as[i]]))
			}
		}
	}
	return "ordered: the clauses cannot be given buttons in order"
}

// decimal is a plain decimal number: an integer or one with a fraction.
var decimal = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)

// sortedWhy checks that the labels are in non-decreasing order, case
// folded, and returns the first pair that is not, or "". With a pattern
// only the labels it matches count, and its group, when it has one, is
// what is compared: as numbers when every group is a decimal number, as
// text otherwise.
func sortedWhy(sd *Sorted, buttons []string) string {
	var re *regexp.Regexp
	if sd.Matching {
		var err error
		if re, err = regexp.Compile(sd.Pattern); err != nil {
			return fmt.Sprintf("sorted: pattern %q: %v", sd.Pattern, err)
		}
	}
	var labels, keys []string
	for _, b := range buttons {
		l := strings.TrimSpace(b)
		k := l
		if re != nil {
			m := re.FindStringSubmatch(l)
			if m == nil {
				continue
			}
			if len(m) > 1 {
				k = m[1]
			}
		}
		labels = append(labels, l)
		keys = append(keys, strings.TrimSpace(k))
	}
	numeric := true
	nums := make([]float64, len(keys))
	for i, k := range keys {
		f, err := strconv.ParseFloat(k, 64)
		if !decimal.MatchString(k) || err != nil {
			numeric = false
			break
		}
		nums[i] = f
	}
	for i := 1; i < len(keys); i++ {
		var after bool // keys[i] sorts before keys[i-1]
		if numeric {
			after = nums[i] < nums[i-1]
		} else {
			after = strings.ToLower(keys[i]) < strings.ToLower(keys[i-1])
		}
		if after {
			return fmt.Sprintf("sorted: %q comes before %q", labels[i], labels[i-1])
		}
	}
	return ""
}
