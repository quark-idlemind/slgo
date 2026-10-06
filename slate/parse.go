package slate

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// Parse lexes and parses a Slate 1 script.
// filename is used in errors. The source is kept on the Script.
// A lexical or grammatical failure is returned as *Error.
// Parse does not apply the static checks and does not dial a region.
func Parse(filename string, src []byte) (*Script, error) {
	p := &parser{
		lex:    newLexer(filename, src),
		file:   filename,
		script: &Script{File: filename, Src: src},
	}
	if err := p.next(); err != nil {
		return nil, err
	}
	if err := p.header(); err != nil {
		return nil, err
	}
	if err := p.headers(); err != nil {
		return nil, err
	}
	if err := p.top(); err != nil {
		return nil, err
	}
	return p.script, nil
}

// top reads what follows the headers: plain steps, which are one implicit
// test, or one or more top-level blocks. The two forms cannot be mixed.
func (p *parser) top() error {
	switch {
	case p.tok.kind == kEOF:
		return p.errorf("a file needs a step or a test")
	case p.startsTopLevel():
		return p.suite()
	case p.startsStep():
		return p.plain()
	case p.isHeader():
		return p.errorf("headers go before the first step")
	default:
		return p.unexpected("expected a step or a test block")
	}
}

func (p *parser) plain() error {
	start := p.tok.span
	steps, err := p.steps(false)
	if err != nil {
		return err
	}
	name := strings.TrimSuffix(filepath.Base(p.file), ".slate")
	p.script.Tests = []Test{{
		Span:     cover(start, p.prev.span),
		Name:     name,
		Implicit: true,
		Steps:    steps,
	}}
	return nil
}

func (p *parser) startsTopLevel() bool {
	if p.tok.kind != kWord {
		return false
	}
	switch p.tok.text {
	case "before", "after", "sequence", "test":
		return true
	default:
		return false
	}
}

const mixed = "a file is either plain steps or test blocks, not both; put the steps in a test block"

func (p *parser) suite() error {
	for p.tok.kind != kEOF {
		start := p.tok.span
		switch {
		case p.kw("before"), p.kw("after"):
			word := p.tok.text
			if err := p.next(); err != nil {
				return err
			}
			if err := p.want("each"); err != nil {
				return err
			}
			steps, err := p.block()
			if err != nil {
				return err
			}
			b := Block{Span: cover(start, p.prev.span), Steps: steps}
			if word == "before" {
				p.script.Befores = append(p.script.Befores, b)
			} else {
				p.script.Afters = append(p.script.Afters, b)
			}
		case p.kw("sequence"):
			if err := p.next(); err != nil {
				return err
			}
			name, err := p.ident()
			if err != nil {
				return err
			}
			steps, err := p.block()
			if err != nil {
				return err
			}
			p.script.Sequences = append(p.script.Sequences, Sequence{Span: cover(start, p.prev.span), Name: name, Steps: steps})
		case p.kw("test"):
			if err := p.next(); err != nil {
				return err
			}
			name, nsp, err := p.str()
			if err != nil {
				return err
			}
			steps, err := p.block()
			if err != nil {
				return err
			}
			p.script.Tests = append(p.script.Tests, Test{Span: cover(start, p.prev.span), Name: name, NameSpan: nsp, Steps: steps})
		case p.startsStep():
			return p.errorf("%s", mixed)
		case p.isHeader():
			return p.errorf("headers go before the first step or block")
		default:
			return p.unexpected("expected test, before each, after each, or sequence")
		}
	}
	return nil
}

// block reads { step+ }.
func (p *parser) block() ([]Step, error) {
	if p.tok.kind != kLBrace {
		return nil, p.unexpected("expected {")
	}
	if err := p.next(); err != nil {
		return nil, err
	}
	if !p.startsStep() {
		if p.tok.kind == kRBrace {
			return nil, p.errorf("a block needs a step")
		}
		return nil, p.unexpected("expected a stimulus, expect, then, or do")
	}
	steps, err := p.steps(true)
	if err != nil {
		return nil, err
	}
	if p.tok.kind != kRBrace {
		return nil, p.unexpected("expected }")
	}
	return steps, p.next()
}

// steps reads step+. What may end it: } in a block, otherwise the end of
// the file; a top-level word or a stray token is an error that names what
// could have come next.
func (p *parser) steps(inBlock bool) ([]Step, error) {
	var out []Step
	for p.startsStep() {
		st, err := p.step()
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	switch {
	case p.tok.kind == kEOF && !inBlock, p.tok.kind == kRBrace && inBlock:
		return out, nil
	case p.startsTopLevel():
		if inBlock {
			return nil, p.errorf("a block cannot hold %s; close it with } first", p.tok.text)
		}
		return nil, p.errorf("%s", mixed)
	case p.isHeader():
		return nil, p.errorf("headers go before the first step")
	case inBlock:
		return nil, p.unexpected("expected a stimulus, expect, then, or do (or } to close the block)")
	default:
		return nil, p.unexpected("expected a stimulus, expect, then, or do")
	}
}

type parser struct {
	lex    *lexer
	file   string
	script *Script
	tok    token
	prev   token
	err    error
}

func (p *parser) next() error {
	if p.err != nil {
		return p.err
	}
	t, err := p.lex.Next()
	if err != nil {
		p.err = err
		p.tok = token{kind: kEOF}
		return err
	}
	p.prev = p.tok
	p.tok = t
	return nil
}

func (p *parser) errorf(format string, args ...any) error {
	sp := p.tok.span
	if sp.Line == 0 {
		sp = p.prev.span
	}
	return &Error{File: p.file, Line: sp.Line, Column: sp.Col, Msg: fmt.Sprintf(format, args...)}
}

func (p *parser) unexpected(what string) error {
	return p.errorf("%s, found %s", what, p.tok.String())
}

func (p *parser) kw(s string) bool {
	return p.tok.kind == kWord && p.tok.text == s
}

func (p *parser) want(kw string) error {
	if p.err != nil {
		return p.err
	}
	if !p.kw(kw) {
		return p.unexpected("expected " + kw)
	}
	return p.next()
}

func (p *parser) header() error {
	if !p.kw("slate") {
		return p.errorf("expected slate 1")
	}
	if err := p.next(); err != nil {
		return err
	}
	// The token text is "1", not the integer value, so slate 01 is rejected.
	if p.tok.kind != kInt || p.tok.text != "1" {
		return p.errorf("expected slate 1")
	}
	return p.next()
}

func (p *parser) isHeader() bool {
	switch p.tok.text {
	case "timeout", "allow", "object", "avatar", "item", "probe", "listen":
		return p.tok.kind == kWord
	default:
		return false
	}
}

func (p *parser) headers() error {
	for p.isHeader() {
		var err error
		switch p.tok.text {
		case "timeout":
			err = p.timeout()
		case "allow":
			err = p.allow()
		case "object":
			err = p.object()
		case "avatar":
			err = p.avatar()
		case "item":
			err = p.item()
		case "probe":
			err = p.probe()
		case "listen":
			err = p.listen()
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (p *parser) timeout() error {
	if err := p.want("timeout"); err != nil {
		return err
	}
	d, err := p.duration()
	if err != nil {
		return err
	}
	p.script.Timeouts = append(p.script.Timeouts, d)
	return nil
}

func (p *parser) allow() error {
	start := p.tok.span
	if err := p.want("allow"); err != nil {
		return err
	}
	if p.kw("permission") {
		return p.allowPermission(start)
	}
	if err := p.want("pay"); err != nil {
		return err
	}
	p.script.Allows = append(p.script.Allows, cover(start, p.prev.span))
	return nil
}

// allowPermission is allow permission NAME [NAME...] from OBJ. The names
// are words here and are checked by Check.
func (p *parser) allowPermission(start Span) error {
	if err := p.want("permission"); err != nil {
		return err
	}
	a := PermissionAllow{}
	// The list ends at from, or at a word that begins something else, so
	// a header missing its from says so rather than swallowing the next
	// line's first word.
	for p.tok.kind == kWord && !p.kw("from") && !p.beginsItem() {
		id, err := p.ident()
		if err != nil {
			return err
		}
		a.Names = append(a.Names, id)
	}
	if len(a.Names) == 0 {
		return p.unexpected("expected a permission name")
	}
	if !p.kw("from") {
		return p.unexpected("expected from and the object the permissions are for")
	}
	if err := p.want("from"); err != nil {
		return err
	}
	from, err := p.ident()
	if err != nil {
		return err
	}
	a.From = from
	a.Span = cover(start, p.prev.span)
	p.script.Permits = append(p.script.Permits, a)
	return nil
}

func (p *parser) object() error {
	start := p.tok.span
	if err := p.want("object"); err != nil {
		return err
	}
	name, err := p.ident()
	if err != nil {
		return err
	}
	if err := p.want("is"); err != nil {
		return err
	}
	world, wsp, err := p.str()
	if err != nil {
		return err
	}
	o := Object{Name: name, World: world, WorldSpan: wsp}
	if p.kw("description") {
		if err := p.next(); err != nil {
			return err
		}
		if o.Desc, err = p.text(); err != nil {
			return err
		}
		o.HasDesc = true
	}
	o.Span = cover(start, p.prev.span)
	p.script.Objects = append(p.script.Objects, o)
	return nil
}

// avatar reads avatar ident: a second avatar, given at run time.
func (p *parser) avatar() error {
	start := p.tok.span
	if err := p.want("avatar"); err != nil {
		return err
	}
	name, err := p.ident()
	if err != nil {
		return err
	}
	p.script.Avatars = append(p.script.Avatars, Avatar{Span: cover(start, p.prev.span), Name: name})
	return nil
}

// item reads item ident is string in string.
func (p *parser) item() error {
	start := p.tok.span
	if err := p.want("item"); err != nil {
		return err
	}
	name, err := p.ident()
	if err != nil {
		return err
	}
	if err := p.want("is"); err != nil {
		return err
	}
	world, wsp, err := p.str()
	if err != nil {
		return err
	}
	if err := p.want("in"); err != nil {
		return err
	}
	folder, fsp, err := p.str()
	if err != nil {
		return err
	}
	p.script.Items = append(p.script.Items, Item{
		Span: cover(start, p.prev.span), Name: name,
		World: world, WorldSpan: wsp, Folder: folder, FolderSpan: fsp,
	})
	return nil
}

func (p *parser) probe() error {
	start := p.tok.span
	if err := p.want("probe"); err != nil {
		return err
	}
	name, err := p.ident()
	if err != nil {
		return err
	}
	p.script.Probes = append(p.script.Probes, Probe{
		Span: cover(start, p.prev.span),
		Name: name,
	})
	return nil
}

func (p *parser) listen() error {
	start := p.tok.span
	if err := p.want("listen"); err != nil {
		return err
	}
	n, err := p.integer()
	if err != nil {
		return err
	}
	p.script.Listens = append(p.script.Listens, Listen{
		Span:    cover(start, p.prev.span),
		Channel: n,
	})
	return nil
}

func (p *parser) startsStep() bool {
	return p.startsStimulus() || p.kw("then") || p.kw("expect") || p.kw("do")
}

func (p *parser) startsStimulus() bool {
	if p.tok.kind != kWord {
		return false
	}
	switch p.tok.text {
	case "touch", "drag", "say", "pay", "sit", "stand", "choose", "answer", "send", "wear", "rez", "take", "wait", "drop", "group":
		return true
	default:
		return false
	}
}

func (p *parser) step() (Step, error) {
	start := p.tok.span
	var st Step
	switch {
	case p.startsStimulus():
		stim, err := p.stimulus()
		if err != nil {
			return Step{}, err
		}
		st.Stimulus = stim
	case p.kw("then"):
		if err := p.next(); err != nil {
			return Step{}, err
		}
		st.Then = true
		if !p.kw("expect") {
			return Step{}, p.errorf("then needs an expectation")
		}
	case p.kw("expect"):
	case p.kw("do"):
		return p.call()
	default:
		return Step{}, p.unexpected("expected a step")
	}
	for p.kw("expect") {
		e, err := p.expectation()
		if err != nil {
			return Step{}, err
		}
		st.Expect = append(st.Expect, e)
	}
	if st.Stimulus == nil && len(st.Expect) == 0 {
		return Step{}, p.errorf("a step needs a stimulus or an expectation")
	}
	st.Span = cover(start, p.prev.span)
	return st, nil
}

func (p *parser) call() (Step, error) {
	start := p.tok.span
	if err := p.want("do"); err != nil {
		return Step{}, err
	}
	name, err := p.ident()
	if err != nil {
		return Step{}, err
	}
	sp := cover(start, p.prev.span)
	return Step{Span: sp, Do: &Do{Span: sp, Name: name}}, nil
}

func (p *parser) stimulus() (*Stimulus, error) {
	start := p.tok.span
	s := &Stimulus{}
	var err error
	switch p.tok.text {
	case "touch":
		s.Touch, err = p.touch()
	case "drag":
		s.Drag, err = p.drag()
	case "say":
		s.Say, err = p.say()
	case "pay":
		s.Pay, err = p.pay()
	case "sit":
		s.Sit, err = p.sit()
	case "stand":
		s.Stand, err = p.stand()
	case "wait":
		s.Wait, err = p.wait()
	case "choose":
		s.Choose, err = p.choose()
	case "answer":
		s.Answer, err = p.answer()
	case "send":
		s.Send, err = p.send()
	case "wear":
		s.Wear, err = p.wear()
	case "rez":
		s.Rez, err = p.rezItem()
	case "take":
		s.TakeOff, err = p.takeOff()
	case "drop":
		s.Drop, err = p.drop()
	case "group":
		s.Group, err = p.setGroup()
	default:
		return nil, p.unexpected("expected a stimulus")
	}
	if err != nil {
		return nil, err
	}
	// An as NAME no stimulus above took is the tester's alone: read so
	// that Check can say so, and not a grammar error at the word.
	if p.kw("as") {
		if err := p.next(); err != nil {
			return nil, err
		}
		id, err := p.ident()
		if err != nil {
			return nil, err
		}
		s.OtherAs = &id
	}
	s.Span = cover(start, p.prev.span)
	return s, nil
}

// asAvatar reads an optional as NAME after a stimulus a second avatar can
// do: the avatar's binding.
func (p *parser) asAvatar() (*Ident, error) {
	if !p.kw("as") {
		return nil, nil
	}
	if err := p.next(); err != nil {
		return nil, err
	}
	id, err := p.ident()
	if err != nil {
		return nil, err
	}
	return &id, nil
}

func (p *parser) touch() (*Touch, error) {
	if err := p.want("touch"); err != nil {
		return nil, err
	}
	name, err := p.ident()
	if err != nil {
		return nil, err
	}
	t := &Touch{Name: name}
	switch {
	case p.kw("anywhere"):
		t.Anywhere = true
		if err := p.next(); err != nil {
			return nil, err
		}
	case p.kw("link"):
		if err := p.next(); err != nil {
			return nil, err
		}
		n, err := p.integer()
		if err != nil {
			return nil, err
		}
		t.Link = &n
		if err := p.refine(t); err != nil {
			return nil, err
		}
	case p.kw("face"), p.kw("button"), p.kw("showing"):
		if err := p.refine(t); err != nil {
			return nil, err
		}
	default:
		return nil, p.unexpected("expected anywhere, link, face, button, or showing")
	}
	if t.Showing != nil || p.kw("showing") {
		// showing stands alone; a face, link or button beside it parses so
		// that Check can name the clash.
		if err := p.beside(t); err != nil {
			return nil, err
		}
	}
	if p.kw("if") {
		start := p.tok.span
		if err := p.next(); err != nil {
			return nil, err
		}
		if err := p.want("shown"); err != nil {
			return nil, err
		}
		sp := cover(start, p.prev.span)
		t.Guard = &sp
	}
	t.AsAvatar, err = p.asAvatar()
	return t, err
}

// beside reads what may follow or precede a showing, so the clash is a
// static error at the word and not a grammar error at the next one.
func (p *parser) beside(t *Touch) error {
	for {
		var err error
		switch {
		case p.kw("showing") && t.Showing == nil:
			err = p.showing(t)
		case p.kw("face") && t.Face == nil && t.Button == nil:
			err = p.faceAt(t)
		case p.kw("button") && t.Button == nil:
			t.Button, err = p.button()
		default:
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// showing reads showing uuidval ( at number number )?, where uuidval is a
// UUID or a capture: any is a reading, and not a thing to touch.
func (p *parser) showing(t *Touch) error {
	start := p.tok.span
	if err := p.next(); err != nil {
		return err
	}
	sh := &Showing{}
	switch {
	case p.tok.kind == kUUID:
		sh.ID = p.tok.text
		if err := p.next(); err != nil {
			return err
		}
	case p.tok.kind == kCapture:
		c, err := p.capture()
		if err != nil {
			return err
		}
		sh.Use = &c
	case p.kw("any"):
		return p.errorf("showing needs a UUID or a capture; any is only a reading, as in is any as $x")
	default:
		return p.unexpected("expected a UUID or a capture")
	}
	if p.kw("at") {
		st, err := p.at()
		if err != nil {
			return err
		}
		sh.At = &st
	}
	sh.Span = cover(start, p.prev.span)
	t.Showing = sh
	return nil
}

func (p *parser) refine(t *Touch) error {
	switch {
	case p.kw("anywhere"):
		t.Anywhere = true
		return p.next()
	case p.kw("face"):
		return p.faceAt(t)
	case p.kw("showing"):
		return p.showing(t)
	case p.kw("button"):
		b, err := p.button()
		if err != nil {
			return err
		}
		t.Button = b
		return nil
	default:
		return nil
	}
}

func (p *parser) faceAt(t *Touch) error {
	if err := p.next(); err != nil {
		return err
	}
	n, err := p.integer()
	if err != nil {
		return err
	}
	t.Face = &n
	if !p.kw("at") {
		return nil
	}
	st, err := p.at()
	if err != nil {
		return err
	}
	t.At = &st
	return nil
}

func (p *parser) at() (ST, error) {
	if err := p.want("at"); err != nil {
		return ST{}, err
	}
	return p.pair()
}

func (p *parser) pair() (ST, error) {
	s, err := p.number()
	if err != nil {
		return ST{}, err
	}
	t, err := p.number()
	if err != nil {
		return ST{}, err
	}
	return ST{Span: cover(s.Span, t.Span), S: s, T: t}, nil
}

func (p *parser) button() (*Button, error) {
	if err := p.next(); err != nil {
		return nil, err
	}
	return p.buttonBody()
}

// buttonBody reads what follows the word button: an optional number, one
// or more parts, an optional face. A touch and a button reading share it.
func (p *parser) buttonBody() (*Button, error) {
	b := &Button{}
	if p.tok.kind == kFloat {
		return nil, p.errorf("a button number is a whole number")
	}
	if p.tok.kind == kInt {
		n, err := p.integer()
		if err != nil {
			return nil, err
		}
		b.Nth = &n
	}
	if !p.startsPart() {
		return nil, p.errorf("a button needs a part")
	}
	for p.startsPart() {
		part, err := p.part()
		if err != nil {
			return nil, err
		}
		b.Parts = append(b.Parts, part)
	}
	if p.kw("face") {
		if err := p.next(); err != nil {
			return nil, err
		}
		n, err := p.integer()
		if err != nil {
			return nil, err
		}
		b.Face = &n
	}
	return b, nil
}

func (p *parser) startsPart() bool {
	if p.tok.kind != kWord {
		return false
	}
	switch p.tok.text {
	case "text", "pattern", "symbol", "image", "box", "circle", "oval":
		return true
	default:
		return false
	}
}

func (p *parser) part() (Part, error) {
	start := p.tok.span
	word := p.tok.text
	if err := p.next(); err != nil {
		return Part{}, err
	}
	kind, textual, ok := partKind(word)
	if !ok {
		return Part{}, p.errorf("expected a button part")
	}
	if !textual {
		return Part{Span: start, Kind: kind}, nil
	}
	if kind == PartText && p.tok.kind == kCapture {
		c, err := p.capture()
		if err != nil {
			return Part{}, err
		}
		return Part{Span: cover(start, c.Span), Kind: kind, Capture: &c}, nil
	}
	s, sp, err := p.str()
	if err != nil {
		return Part{}, err
	}
	return Part{Span: cover(start, sp), Kind: kind, Text: s}, nil
}

func partKind(word string) (PartKind, bool, bool) {
	switch word {
	case "text":
		return PartText, true, true
	case "pattern":
		return PartPattern, true, true
	case "symbol":
		return PartSymbol, true, true
	case "image":
		return PartImage, true, true
	case "box":
		return PartBox, false, true
	case "circle":
		return PartCircle, false, true
	case "oval":
		return PartOval, false, true
	default:
		return 0, false, false
	}
}

func (p *parser) drag() (*Drag, error) {
	if err := p.want("drag"); err != nil {
		return nil, err
	}
	name, err := p.ident()
	if err != nil {
		return nil, err
	}
	d := &Drag{Name: name}
	if p.kw("on") {
		return p.screenDrag(d)
	}
	if p.kw("link") {
		if err := p.next(); err != nil {
			return nil, err
		}
		n, err := p.integer()
		if err != nil {
			return nil, err
		}
		d.Link = &n
	}
	if err := p.want("face"); err != nil {
		return nil, err
	}
	face, err := p.integer()
	if err != nil {
		return nil, err
	}
	d.Face = face
	if err := p.want("from"); err != nil {
		return nil, err
	}
	from, err := p.pair()
	if err != nil {
		return nil, err
	}
	d.From = from
	if err := p.want("to"); err != nil {
		return nil, err
	}
	to, err := p.pair()
	if err != nil {
		return nil, err
	}
	d.To = to
	if err := p.dragTimes(d); err != nil {
		return nil, err
	}
	d.AsAvatar, err = p.asAvatar()
	return d, err
}

// dragTimes reads [over D] [press D] [dwell D], in that order.
func (p *parser) dragTimes(d *Drag) error {
	for _, t := range []struct {
		word string
		into **Duration
	}{{"over", &d.Over}, {"press", &d.Press}, {"dwell", &d.Dwell}} {
		if !p.kw(t.word) {
			continue
		}
		if err := p.next(); err != nil {
			return err
		}
		dur, err := p.duration()
		if err != nil {
			return err
		}
		*t.into = &dur
	}
	return nil
}

// screenDrag reads what follows drag OBJ: on screen from POINT (to X Y |
// by DX DY) [over D] [press D] [dwell D] [settle].
func (p *parser) screenDrag(d *Drag) (*Drag, error) {
	start := p.tok.span
	if err := p.want("on"); err != nil {
		return nil, err
	}
	if err := p.want("screen"); err != nil {
		return nil, err
	}
	if err := p.want("from"); err != nil {
		return nil, err
	}
	sd := &ScreenDrag{}
	var err error
	if p.kw("link") || p.kw("face") {
		if p.kw("link") {
			if err := p.next(); err != nil {
				return nil, err
			}
			n, err := p.integer()
			if err != nil {
				return nil, err
			}
			sd.Link = &n
		}
		if err := p.want("face"); err != nil {
			return nil, err
		}
		f, err := p.integer()
		if err != nil {
			return nil, err
		}
		sd.Face = &f
		if err := p.want("at"); err != nil {
			return nil, err
		}
		if sd.At, err = p.pair(); err != nil {
			return nil, err
		}
	} else if sd.FromPixels, err = p.pair(); err != nil {
		return nil, err
	}
	switch {
	case p.kw("to"):
	case p.kw("by"):
		sd.By = true
	default:
		return nil, p.unexpected("expected to or by")
	}
	if err := p.next(); err != nil {
		return nil, err
	}
	if sd.To, err = p.pair(); err != nil {
		return nil, err
	}
	if err := p.dragTimes(d); err != nil {
		return nil, err
	}
	if p.kw("settle") {
		sd.Settle = true
		if err := p.next(); err != nil {
			return nil, err
		}
	}
	sd.Span = cover(start, p.prev.span)
	d.Screen = sd
	return d, nil
}

func (p *parser) say() (*Say, error) {
	if err := p.want("say"); err != nil {
		return nil, err
	}
	text, tsp, err := p.str()
	if err != nil {
		return nil, err
	}
	if err := p.want("on"); err != nil {
		return nil, err
	}
	ch, err := p.integer()
	if err != nil {
		return nil, err
	}
	s := &Say{Text: text, TextSpan: tsp, Channel: ch}
	if !p.kw("as") {
		return s, nil
	}
	if err := p.next(); err != nil {
		return nil, err
	}
	spk, err := p.stimSpeaker()
	if err != nil {
		return nil, err
	}
	s.As = &spk
	return s, nil
}

func (p *parser) pay() (*Pay, error) {
	if err := p.want("pay"); err != nil {
		return nil, err
	}
	name, err := p.ident()
	if err != nil {
		return nil, err
	}
	linden := false
	if p.tok.kind == kMoney {
		linden = true
		if err := p.next(); err != nil {
			return nil, err
		}
	}
	amt, err := p.integer()
	if err != nil {
		return nil, err
	}
	py := &Pay{Name: name, Amount: amt, Linden: linden}
	if !p.kw("reason") {
		return py, nil
	}
	if err := p.next(); err != nil {
		return nil, err
	}
	reason, rsp, err := p.str()
	if err != nil {
		return nil, err
	}
	py.Reason = reason
	py.HasReason = true
	py.ReasonSpan = rsp
	return py, nil
}

func (p *parser) sit() (*Sit, error) {
	if err := p.want("sit"); err != nil {
		return nil, err
	}
	name, err := p.ident()
	if err != nil {
		return nil, err
	}
	return &Sit{Name: name}, nil
}

// toAvatar reads an optional to NAME on an expectation of what reached a
// second avatar.
func (p *parser) toAvatar() (*Ident, error) {
	if !p.kw("to") {
		return nil, nil
	}
	if err := p.next(); err != nil {
		return nil, err
	}
	id, err := p.ident()
	if err != nil {
		return nil, err
	}
	return &id, nil
}

func (p *parser) stand() (*Stand, error) {
	if err := p.want("stand"); err != nil {
		return nil, err
	}
	return &Stand{}, nil
}

func (p *parser) wait() (*Wait, error) {
	if err := p.want("wait"); err != nil {
		return nil, err
	}
	d, err := p.duration()
	if err != nil {
		return nil, err
	}
	return &Wait{For: d}, nil
}

func (p *parser) choose() (*Choose, error) {
	if err := p.want("choose"); err != nil {
		return nil, err
	}
	c := &Choose{}
	switch {
	case p.kw("matching"):
		if err := p.next(); err != nil {
			return nil, err
		}
		if p.tok.kind == kCapture {
			return nil, p.errorf("a capture is not a pattern; choose $x presses the button it holds")
		}
		label, sp, err := p.str()
		if err != nil {
			return nil, err
		}
		c.Kind, c.Label, c.LabelSpan = ChooseMatching, label, sp
	case p.kw("button"):
		if err := p.next(); err != nil {
			return nil, err
		}
		n, err := p.integer()
		if err != nil {
			return nil, err
		}
		c.Kind, c.Index, c.LabelSpan = ChooseButton, &n, n.Span
	case p.tok.kind == kCapture:
		cp, err := p.capture()
		if err != nil {
			return nil, err
		}
		c.Kind, c.Use, c.LabelSpan = ChooseCapture, &cp, cp.Span
	default:
		label, sp, err := p.str()
		if err != nil {
			return nil, err
		}
		c.Label, c.LabelSpan = label, sp
	}
	if err := p.want("on"); err != nil {
		return nil, err
	}
	name, err := p.ident()
	if err != nil {
		return nil, err
	}
	c.Name = name
	c.AsAvatar, err = p.asAvatar()
	return c, err
}

func (p *parser) answer() (*Answer, error) {
	if err := p.want("answer"); err != nil {
		return nil, err
	}
	text, tsp, err := p.str()
	if err != nil {
		return nil, err
	}
	if err := p.want("on"); err != nil {
		return nil, err
	}
	name, err := p.ident()
	if err != nil {
		return nil, err
	}
	as, err := p.asAvatar()
	if err != nil {
		return nil, err
	}
	return &Answer{Text: text, TextSpan: tsp, Name: name, AsAvatar: as}, nil
}

func (p *parser) send() (*Send, error) {
	if err := p.want("send"); err != nil {
		return nil, err
	}
	if err := p.want("on"); err != nil {
		return nil, err
	}
	name, err := p.ident()
	if err != nil {
		return nil, err
	}
	if err := p.want("from"); err != nil {
		return nil, err
	}
	if err := p.want("link"); err != nil {
		return nil, err
	}
	from, err := p.integer()
	if err != nil {
		return nil, err
	}
	if err := p.want("to"); err != nil {
		return nil, err
	}
	if err := p.want("link"); err != nil {
		return nil, err
	}
	to, err := p.linkTarget()
	if err != nil {
		return nil, err
	}
	if err := p.want("num"); err != nil {
		return nil, err
	}
	num, err := p.integer()
	if err != nil {
		return nil, err
	}
	if err := p.want("text"); err != nil {
		return nil, err
	}
	s := &Send{Name: name, From: from, To: to, Num: num}
	if p.tok.kind == kCapture {
		c, err := p.capture()
		if err != nil {
			return nil, err
		}
		s.TextCapture, s.TextSpan = &c, c.Span
	} else {
		text, tsp, err := p.str()
		if err != nil {
			return nil, err
		}
		s.Text, s.TextSpan = text, tsp
	}
	if !p.kw("key") {
		return s, nil
	}
	if err := p.next(); err != nil {
		return nil, err
	}
	k, err := p.key()
	if err != nil {
		return nil, err
	}
	s.Key = &k
	return s, nil
}

func (p *parser) linkTarget() (LinkTarget, error) {
	if p.tok.kind == kInt {
		n, err := p.integer()
		if err != nil {
			return LinkTarget{}, err
		}
		return LinkTarget{Span: n.Span, Int: n}, nil
	}
	if p.tok.kind == kWord {
		if _, ok := LinkWords[p.tok.text]; ok {
			w := p.tok.text
			sp := p.tok.span
			if err := p.next(); err != nil {
				return LinkTarget{}, err
			}
			return LinkTarget{Span: sp, Word: w}, nil
		}
	}
	return LinkTarget{}, p.unexpected("expected a link target")
}

func (p *parser) expectation() (Expect, error) {
	start := p.tok.span
	if err := p.want("expect"); err != nil {
		return Expect{}, err
	}
	var e Expect
	if p.kw("no") {
		e.Neg = true
		if err := p.next(); err != nil {
			return Expect{}, err
		}
	}
	if err := p.expectBody(&e); err != nil {
		return Expect{}, err
	}
	if p.kw("near") {
		n, err := p.near()
		if err != nil {
			return Expect{}, err
		}
		e.Near = &n
	}
	if p.kw("within") {
		if err := p.next(); err != nil {
			return Expect{}, err
		}
		d, err := p.duration()
		if err != nil {
			return Expect{}, err
		}
		e.Within = &d
	}
	if p.kw("as") {
		if err := p.next(); err != nil {
			return Expect{}, err
		}
		if p.tok.kind != kCapture {
			return Expect{}, p.unexpected("expected a capture such as $name after as")
		}
		c, err := p.capture()
		if err != nil {
			return Expect{}, err
		}
		e.As = &c
	}
	e.Span = cover(start, p.prev.span)
	return e, nil
}

// near reads near N, or near N percent, after an expectation's body.
func (p *parser) near() (Near, error) {
	start := p.tok.span
	if err := p.want("near"); err != nil {
		return Near{}, err
	}
	n, err := p.number()
	if err != nil {
		return Near{}, err
	}
	v := Near{Amount: n}
	if p.kw("percent") {
		v.Percent = true
		if err := p.next(); err != nil {
			return Near{}, err
		}
	}
	v.Span = cover(start, p.prev.span)
	return v, nil
}

func (p *parser) expectBody(e *Expect) error {
	switch {
	case p.kw("say"):
		return p.sayExp(e)
	case p.kw("dialog"):
		return p.dialogExp(e)
	case p.kw("textbox"):
		return p.boxExp(e)
	case p.kw("texture"):
		return p.textureExp(e)
	case p.kw("offset"):
		return p.vecExp(e, false)
	case p.kw("repeats"):
		return p.vecExp(e, true)
	case p.kw("rotation"):
		return p.rotExp(e)
	case p.kw("position"):
		return p.vec3Exp(e, "position")
	case p.kw("size"):
		return p.vec3Exp(e, "size")
	case p.kw("turn"):
		return p.vec3Exp(e, "turn")
	case p.kw("light"):
		return p.lightExp(e, false)
	case p.kw("projector"):
		return p.lightExp(e, true)
	case p.kw("click"):
		return p.clickExp(e)
	case p.kw("text"):
		return p.floatTextExp(e)
	case p.kw("fullbright"):
		return p.fullbrightExp(e)
	case p.kw("glow"):
		return p.glowExp(e)
	case p.kw("colour"):
		return p.colourExp(e)
	case p.kw("alpha"):
		return p.alphaExp(e)
	case p.kw("alphamode"):
		return p.alphaModeExp(e)
	case p.kw("normalmap"), p.kw("specularmap"), p.kw("glossiness"), p.kw("environment"):
		return p.materialExp(e)
	case p.kw("give"):
		return p.giveExp(e)
	case p.kw("rez"):
		return p.rezExp(e)
	case p.kw("link"):
		return p.linkExp(e)
	case p.kw("button"):
		return p.buttonExp(e)
	case p.kw("attached"):
		return p.attachExp(e)
	case p.kw("animation"):
		return p.animationExp(e)
	case p.kw("sound"):
		return p.soundExp(e)
	default:
		return p.unexpected("expected an expectation")
	}
}

func (p *parser) sayExp(e *Expect) error {
	if err := p.want("say"); err != nil {
		return err
	}
	text, err := p.text()
	if err != nil {
		return err
	}
	if err := p.want("on"); err != nil {
		return err
	}
	ch, err := p.expChan()
	if err != nil {
		return err
	}
	if err := p.want("from"); err != nil {
		return err
	}
	from, err := p.expSpeaker()
	if err != nil {
		return err
	}
	e.Say = &SayExp{Text: text, Channel: ch, From: from}
	return nil
}

func (p *parser) expChan() (ExpectChan, error) {
	sp := p.tok.span
	switch {
	case p.kw("public"):
		return ExpectChan{Span: sp, Kind: ChanPublic}, p.next()
	case p.kw("owner"):
		return ExpectChan{Span: sp, Kind: ChanOwner}, p.next()
	case p.kw("debug"):
		return ExpectChan{Span: sp, Kind: ChanDebug}, p.next()
	case p.kw("direct"):
		return ExpectChan{Span: sp, Kind: ChanDirect}, p.next()
	case p.tok.kind == kInt:
		n, err := p.integer()
		if err != nil {
			return ExpectChan{}, err
		}
		return ExpectChan{Span: n.Span, Kind: ChanNumber, Int: n}, nil
	default:
		return ExpectChan{}, p.unexpected("expected a channel")
	}
}

func (p *parser) expSpeaker() (Speaker, error) {
	sp := p.tok.span
	switch {
	case p.kw("tester"):
		return Speaker{Span: sp, Kind: SpeakTester}, p.next()
	case p.kw("anyone"):
		return Speaker{Span: sp, Kind: SpeakAnyone}, p.next()
	case p.kw("owner"):
		if err := p.next(); err != nil {
			return Speaker{}, err
		}
		if err := p.want("of"); err != nil {
			return Speaker{}, err
		}
		id, err := p.ident()
		if err != nil {
			return Speaker{}, err
		}
		return Speaker{Span: cover(sp, id.Span), Kind: SpeakOwner, Name: id}, nil
	case p.kw("avatar"):
		if err := p.next(); err != nil {
			return Speaker{}, err
		}
		if p.tok.kind == kWord && !p.kw("matching") {
			id, err := p.ident()
			if err != nil {
				return Speaker{}, err
			}
			return Speaker{Span: cover(sp, id.Span), Kind: SpeakSecond, Name: id}, nil
		}
		s, ssp, err := p.str()
		if err != nil {
			return Speaker{}, err
		}
		return Speaker{Span: cover(sp, ssp), Kind: SpeakAvatar, Avatar: s}, nil
	case p.kw("object"):
		if err := p.next(); err != nil {
			return Speaker{}, err
		}
		id, err := p.ident()
		if err != nil {
			return Speaker{}, err
		}
		link, err := p.optLink()
		if err != nil {
			return Speaker{}, err
		}
		spk := Speaker{Span: cover(sp, id.Span), Kind: SpeakObject, Name: id, Link: link}
		if link != nil {
			spk.Span = cover(sp, link.Span)
		}
		return spk, nil
	default:
		return Speaker{}, p.unexpected("expected a speaker")
	}
}

func (p *parser) stimSpeaker() (Speaker, error) {
	if p.kw("object") || p.kw("anyone") {
		return Speaker{}, p.errorf("a stimulus speaks as the tester, the owner, an avatar, or a second avatar")
	}
	switch p.tok.text {
	case "tester", "owner", "avatar":
	default:
		// as NAME: a second avatar's binding.
		if p.tok.kind == kWord {
			id, err := p.ident()
			if err != nil {
				return Speaker{}, err
			}
			return Speaker{Span: id.Span, Kind: SpeakSecond, Name: id}, nil
		}
	}
	spk, err := p.expSpeaker()
	if err != nil {
		return Speaker{}, err
	}
	if spk.Kind == SpeakSecond {
		return Speaker{}, p.errorf("a second avatar says as NAME, without avatar")
	}
	return spk, nil
}

// dialogExp reads dialog from OBJ link? ( text TEXT )? dbutton* only?
// ( count N )?. Every clause is optional, so a bare dialog from OBJ is
// any dialog from it.
func (p *parser) dialogExp(e *Expect) error {
	if err := p.want("dialog"); err != nil {
		return err
	}
	if err := p.want("from"); err != nil {
		return err
	}
	name, err := p.ident()
	if err != nil {
		return err
	}
	link, err := p.optLink()
	if err != nil {
		return err
	}
	to, err := p.toAvatar()
	if err != nil {
		return err
	}
	d := &DialogExp{Name: name, Link: link, To: to}
	if p.kw("text") {
		if err := p.next(); err != nil {
			return err
		}
		if d.Text, err = p.text(); err != nil {
			return err
		}
		d.HasText = true
	}
	for p.kw("button") {
		c, err := p.dbutton()
		if err != nil {
			return err
		}
		d.Clauses = append(d.Clauses, c)
		if c.Nth == nil && !c.Text.Pattern && c.Text.Capture == nil {
			d.Buttons = append(d.Buttons, c.Text.Value)
		}
	}
	if p.kw("only") {
		d.Only = true
		if err := p.next(); err != nil {
			return err
		}
	}
	if p.kw("ordered") {
		d.Ordered, d.OrderedSpan = true, p.tok.span
		if err := p.next(); err != nil {
			return err
		}
	}
	if p.kw("count") {
		if err := p.next(); err != nil {
			return err
		}
		n, err := p.integer()
		if err != nil {
			return err
		}
		d.Count = &n
	}
	if p.kw("sorted") {
		sd, err := p.sorted()
		if err != nil {
			return err
		}
		d.Sorted = sd
	}
	e.Dialog = d
	return nil
}

// sorted reads sorted ( matching string )?. The pattern is a literal
// string: a capture is not a pattern.
func (p *parser) sorted() (*Sorted, error) {
	start := p.tok.span
	if err := p.next(); err != nil {
		return nil, err
	}
	sd := &Sorted{}
	if p.kw("matching") {
		if err := p.next(); err != nil {
			return nil, err
		}
		if p.tok.kind == kCapture {
			return nil, p.errorf("a capture is not a pattern; sorted matching takes a string")
		}
		s, sp, err := p.str()
		if err != nil {
			return nil, err
		}
		sd.Matching, sd.Pattern, sd.PatternSpan = true, s, sp
	}
	sd.Span = cover(start, p.prev.span)
	return sd, nil
}

// dbutton reads button integer? text.
func (p *parser) dbutton() (DButton, error) {
	start := p.tok.span
	if err := p.want("button"); err != nil {
		return DButton{}, err
	}
	c := DButton{}
	if p.tok.kind == kFloat {
		return DButton{}, p.errorf("a button number is a whole number")
	}
	if p.tok.kind == kInt {
		n, err := p.integer()
		if err != nil {
			return DButton{}, err
		}
		c.Nth = &n
	}
	t, err := p.text()
	if err != nil {
		return DButton{}, err
	}
	c.Text = t
	c.Span = cover(start, p.prev.span)
	return c, nil
}

func (p *parser) boxExp(e *Expect) error {
	if err := p.want("textbox"); err != nil {
		return err
	}
	if err := p.want("from"); err != nil {
		return err
	}
	name, err := p.ident()
	if err != nil {
		return err
	}
	link, err := p.optLink()
	if err != nil {
		return err
	}
	to, err := p.toAvatar()
	if err != nil {
		return err
	}
	if err := p.want("text"); err != nil {
		return err
	}
	text, err := p.text()
	if err != nil {
		return err
	}
	e.TextBox = &BoxExp{Name: name, Link: link, To: to, Text: text}
	return nil
}

func (p *parser) textureExp(e *Expect) error {
	if err := p.want("texture"); err != nil {
		return err
	}
	name, link, face, all, err := p.faceTarget()
	if err != nil {
		return err
	}
	st, err := p.state()
	if err != nil {
		return err
	}
	x := &TextureExp{Name: name, Link: link, Face: face, FaceAll: all, State: st}
	if st.Kind != StateChanges && !st.Original {
		if x.Any, x.Use, err = p.reading(); err != nil {
			return err
		}
		if !x.Any && x.Use == nil {
			if p.tok.kind != kUUID {
				return p.unexpected("expected a UUID or original")
			}
			x.ID = p.tok.text
			if err := p.next(); err != nil {
				return err
			}
		}
	}
	e.Texture = x
	return nil
}

// faceTarget reads binding link? faceall, where faceall is "face" followed
// by an integer or all. For all the Int is zero with the span of the word.
func (p *parser) faceTarget() (Ident, *Int, Int, bool, error) {
	name, err := p.ident()
	if err != nil {
		return Ident{}, nil, Int{}, false, err
	}
	link, err := p.optLink()
	if err != nil {
		return Ident{}, nil, Int{}, false, err
	}
	if err := p.want("face"); err != nil {
		return Ident{}, nil, Int{}, false, err
	}
	if p.kw("all") {
		face := Int{Span: p.tok.span, Text: "all"}
		return name, link, face, true, p.next()
	}
	face, err := p.integer()
	return name, link, face, false, err
}

// reading reads the value forms every state expectation takes besides its
// own literal: any, or a capture. It reports whether it read one.
func (p *parser) reading() (any bool, use *Capture, err error) {
	switch {
	case p.kw("any"):
		return true, nil, p.next()
	case p.tok.kind == kCapture:
		c, err := p.capture()
		return false, &c, err
	}
	return false, nil, nil
}

// optLink reads link integer, if present.
func (p *parser) optLink() (*Int, error) {
	if !p.kw("link") {
		return nil, nil
	}
	if err := p.next(); err != nil {
		return nil, err
	}
	n, err := p.integer()
	if err != nil {
		return nil, err
	}
	return &n, nil
}

// state reads is, becomes, or changes. A value that follows changes, or
// the word original after is or becomes, is recorded here; the caller
// reads any other value.
func (p *parser) state() (State, error) {
	start := p.tok.span
	var st State
	switch {
	case p.kw("is"):
		st.Kind = StateIs
	case p.kw("becomes"):
		st.Kind = StateBecomes
	case p.kw("changes"):
		st.Kind = StateChanges
	default:
		return State{}, p.unexpected("expected is, becomes, or changes")
	}
	if err := p.next(); err != nil {
		return State{}, err
	}
	if st.Kind == StateChanges {
		switch p.tok.kind {
		case kUUID, kInt, kFloat, kCapture:
			return State{}, p.errorf("changes takes no value")
		}
		if p.kw("original") || p.kw("any") {
			return State{}, p.errorf("changes takes no value")
		}
	} else if p.kw("original") {
		st.Original = true
		if err := p.next(); err != nil {
			return State{}, err
		}
	}
	st.Span = cover(start, p.prev.span)
	return st, nil
}

func (p *parser) vecExp(e *Expect, repeats bool) error {
	word := "offset"
	if repeats {
		word = "repeats"
	}
	if err := p.want(word); err != nil {
		return err
	}
	name, link, face, all, err := p.faceTarget()
	if err != nil {
		return err
	}
	st, err := p.state()
	if err != nil {
		return err
	}
	v := &VecExp{Name: name, Link: link, Face: face, FaceAll: all, State: st}
	if st.Kind != StateChanges && !st.Original {
		if v.Any, v.Use, err = p.reading(); err != nil {
			return err
		}
		if !v.Any && v.Use == nil {
			if v.S, err = p.number(); err != nil {
				return err
			}
			if v.T, err = p.number(); err != nil {
				return err
			}
		}
	}
	if repeats {
		e.Repeats = v
	} else {
		e.Offset = v
	}
	return nil
}

func (p *parser) vec3Exp(e *Expect, word string) error {
	if err := p.want(word); err != nil {
		return err
	}
	name, err := p.ident()
	if err != nil {
		return err
	}
	link, err := p.optLink()
	if err != nil {
		return err
	}
	st, err := p.state()
	if err != nil {
		return err
	}
	v := &VecExp3{Name: name, Link: link, State: st}
	if st.Kind != StateChanges && !st.Original {
		if v.Any, v.Use, err = p.reading(); err != nil {
			return err
		}
		if !v.Any && v.Use == nil {
			for _, n := range []*Number{&v.X, &v.Y, &v.Z} {
				if *n, err = p.number(); err != nil {
					return err
				}
			}
		}
	}
	switch word {
	case "size":
		e.Size = v
	case "turn":
		e.Turn = v
	default:
		e.Position = v
	}
	return nil
}

// lightExp reads light or projector, the object, an optional property
// word and the state, and its value.  The property word is read where the
// state word would be, so the position holds one of two fixed words and
// never a name.
// Why: doc/slate-language.md#light-and-projector
func (p *parser) lightExp(e *Expect, projector bool) error {
	word := "light"
	props := LightProps
	if projector {
		word, props = "projector", ProjectorProps
	}
	if err := p.want(word); err != nil {
		return err
	}
	name, err := p.ident()
	if err != nil {
		return err
	}
	link, err := p.optLink()
	if err != nil {
		return err
	}
	x := &LightExp{Name: name, Link: link, Projector: projector}
	for _, w := range props {
		if p.kw(w) {
			x.Prop, x.PropSpan = w, p.tok.span
			if err := p.next(); err != nil {
				return err
			}
			break
		}
	}
	if x.State, err = p.state(); err != nil {
		return err
	}
	if hasValue(x.State) {
		if x.Any, x.Use, err = p.reading(); err != nil {
			return err
		}
		if !x.Any && x.Use == nil {
			if err := p.lightValue(x); err != nil {
				return err
			}
		}
	}
	e.Light = x
	return nil
}

// lightValue reads the literal a light or projector expectation compares
// with, which is of the kind its property says.
func (p *parser) lightValue(x *LightExp) error {
	var err error
	switch {
	case x.Prop == "" && !x.Projector:
		switch {
		case p.kw("on"):
			x.On = true
		case p.kw("off"):
		default:
			return p.unexpected("expected on, off, original, or a capture")
		}
		return p.next()
	case x.Prop == "" && x.Projector:
		switch {
		case p.kw("off"):
			x.Off = true
		case p.tok.kind == kUUID:
			x.ID = p.tok.text
		default:
			return p.unexpected("expected a UUID, off, original, or a capture")
		}
		return p.next()
	case x.Prop == "colour":
		for _, n := range []*Number{&x.R, &x.G, &x.B} {
			if *n, err = p.number(); err != nil {
				return err
			}
		}
		return nil
	}
	x.Num, err = p.number()
	return err
}

func (p *parser) rotExp(e *Expect) error {
	if err := p.want("rotation"); err != nil {
		return err
	}
	name, link, face, all, err := p.faceTarget()
	if err != nil {
		return err
	}
	st, err := p.state()
	if err != nil {
		return err
	}
	x := &RotExp{Name: name, Link: link, Face: face, FaceAll: all, State: st}
	if st.Kind != StateChanges && !st.Original {
		if x.Any, x.Use, err = p.reading(); err != nil {
			return err
		}
		if !x.Any && x.Use == nil {
			if x.Turns, err = p.number(); err != nil {
				return err
			}
		}
	}
	e.Rot = x
	return nil
}

func (p *parser) clickExp(e *Expect) error {
	if err := p.want("click"); err != nil {
		return err
	}
	name, err := p.ident()
	if err != nil {
		return err
	}
	link, err := p.optLink()
	if err != nil {
		return err
	}
	st, err := p.state()
	if err != nil {
		return err
	}
	x := &ClickExp{Name: name, Link: link, State: st}
	if st.Kind != StateChanges && !st.Original {
		if x.Any, x.Use, err = p.reading(); err != nil {
			return err
		}
		if !x.Any && x.Use == nil {
			if p.tok.kind != kWord || !clickName(p.tok.text) {
				return p.unexpected("expected a click action or original")
			}
			x.Action = p.tok.text
			if err := p.next(); err != nil {
				return err
			}
		}
	}
	e.Click = x
	return nil
}

// floatTextExp reads text OBJ link? and the state: a string, matching, a
// capture, or after is, any.
func (p *parser) floatTextExp(e *Expect) error {
	if err := p.want("text"); err != nil {
		return err
	}
	name, err := p.ident()
	if err != nil {
		return err
	}
	link, err := p.optLink()
	if err != nil {
		return err
	}
	st, err := p.state()
	if err != nil {
		return err
	}
	x := &TextExp{Name: name, Link: link, State: st}
	if st.Kind != StateChanges && !st.Original {
		if p.kw("any") {
			x.Any = true
			if err := p.next(); err != nil {
				return err
			}
		} else if x.Value, err = p.text(); err != nil {
			return err
		}
	}
	e.FloatText = x
	return nil
}

// faceProp reads the head every face property shares, OBJ link? faceall
// and the state, after the property's own word has been read.
func (p *parser) faceProp(word string) (name Ident, link *Int, face Int, all bool, st State, err error) {
	if err = p.want(word); err != nil {
		return
	}
	if name, link, face, all, err = p.faceTarget(); err != nil {
		return
	}
	st, err = p.state()
	return
}

func (p *parser) fullbrightExp(e *Expect) error {
	name, link, face, all, st, err := p.faceProp("fullbright")
	if err != nil {
		return err
	}
	x := &FullbrightExp{Name: name, Link: link, Face: face, FaceAll: all, State: st}
	if hasValue(st) {
		if x.Any, x.Use, err = p.reading(); err != nil {
			return err
		}
		if !x.Any && x.Use == nil {
			switch {
			case p.kw("on"):
				x.On = true
			case p.kw("off"):
			default:
				return p.unexpected("expected on, off, original, or a capture")
			}
			if err := p.next(); err != nil {
				return err
			}
		}
	}
	e.Fullbright = x
	return nil
}

func (p *parser) glowExp(e *Expect) error {
	name, link, face, all, st, err := p.faceProp("glow")
	if err != nil {
		return err
	}
	x := &GlowExp{Name: name, Link: link, Face: face, FaceAll: all, State: st}
	if x.Value, x.Any, x.Use, err = p.numval(st); err != nil {
		return err
	}
	e.Glow = x
	return nil
}

func (p *parser) alphaExp(e *Expect) error {
	name, link, face, all, st, err := p.faceProp("alpha")
	if err != nil {
		return err
	}
	x := &AlphaExp{Name: name, Link: link, Face: face, FaceAll: all, State: st}
	if x.Value, x.Any, x.Use, err = p.numval(st); err != nil {
		return err
	}
	e.Alpha = x
	return nil
}

// alphaModeExp reads alphamode OBJ link? face N and the state, and for an
// is or becomes one of AlphaModes. Unlike the other face properties it has
// no any and no capture.
func (p *parser) alphaModeExp(e *Expect) error {
	name, link, face, all, st, err := p.faceProp("alphamode")
	if err != nil {
		return err
	}
	x := &AlphaModeExp{Name: name, Link: link, Face: face, FaceAll: all, State: st}
	if hasValue(st) {
		if p.tok.kind != kWord || !slices.Contains(AlphaModes, p.tok.text) {
			return p.unexpected("expected default, none, blend, mask, emissive or original")
		}
		x.Mode, x.ModeAt = p.tok.text, p.tok.span
		if err := p.next(); err != nil {
			return err
		}
	}
	e.AlphaMode = x
	return nil
}

// materialExp reads normalmap, specularmap, glossiness or environment
// OBJ link? faceall and the state. A map takes a uuid or the word none,
// which is the null key a face without that map reads; a level takes a
// number.
// Why: doc/slate-language.md#material-maps
func (p *parser) materialExp(e *Expect) error {
	prop := p.tok.text
	name, link, face, all, st, err := p.faceProp(prop)
	if err != nil {
		return err
	}
	x := &MaterialExp{Prop: prop, Name: name, Link: link, Face: face, FaceAll: all, State: st}
	e.Material = x
	if !x.IsMap() {
		x.Value, x.Any, x.Use, err = p.numval(st)
		return err
	}
	if !hasValue(st) {
		return nil
	}
	if x.Any, x.Use, err = p.reading(); err != nil || x.Any || x.Use != nil {
		return err
	}
	switch {
	case p.tok.kind == kUUID:
		x.ID = p.tok.text
	case p.kw("none"):
		x.ID = nullKey
	default:
		return p.unexpected("expected a UUID, none or original")
	}
	x.IDAt = p.tok.span
	return p.next()
}

func (p *parser) colourExp(e *Expect) error {
	name, link, face, all, st, err := p.faceProp("colour")
	if err != nil {
		return err
	}
	x := &ColourExp{Name: name, Link: link, Face: face, FaceAll: all, State: st}
	if hasValue(st) {
		if x.Any, x.Use, err = p.reading(); err != nil {
			return err
		}
		if !x.Any && x.Use == nil {
			for _, n := range []*Number{&x.R, &x.G, &x.B} {
				if *n, err = p.number(); err != nil {
					return err
				}
			}
		}
	}
	e.Colour = x
	return nil
}

// numval reads the value of a state that has one: a number, any, or a capture.
func (p *parser) numval(st State) (n Number, any bool, use *Capture, err error) {
	if !hasValue(st) {
		return
	}
	if any, use, err = p.reading(); err != nil || any || use != nil {
		return
	}
	n, err = p.number()
	return
}

func clickName(s string) bool {
	_, ok := ClickBytes[s]
	return ok
}

func (p *parser) giveExp(e *Expect) error {
	if err := p.want("give"); err != nil {
		return err
	}
	folder := p.kw("folder")
	if folder {
		if err := p.next(); err != nil {
			return err
		}
	}
	item, err := p.text()
	if err != nil {
		return err
	}
	if err := p.want("from"); err != nil {
		return err
	}
	from, err := p.ident()
	if err != nil {
		return err
	}
	var holding []Text
	if folder && p.kw("holding") {
		if err := p.next(); err != nil {
			return err
		}
		for {
			t, err := p.text()
			if err != nil {
				return err
			}
			holding = append(holding, t)
			if p.tok.kind != kString && p.tok.kind != kCapture && !p.kw("matching") {
				break
			}
		}
	}
	if folder && p.kw("to") {
		return p.errorf("expect give folder takes no to: a folder offered to a second avatar is declined as it is heard, so there is no folder to hold")
	}
	to, err := p.toAvatar()
	if err != nil {
		return err
	}
	e.Give = &GiveExp{Folder: folder, Item: item, From: from, Holding: holding, To: to}
	return nil
}

func (p *parser) rezExp(e *Expect) error {
	if err := p.want("rez"); err != nil {
		return err
	}
	if err := p.want("name"); err != nil {
		return err
	}
	name, err := p.text()
	if err != nil {
		return err
	}
	rx := &RezExp{Name: name}
	if p.kw("description") {
		if err := p.next(); err != nil {
			return err
		}
		desc, err := p.text()
		if err != nil {
			return err
		}
		rx.HasDesc = true
		rx.Desc = desc
	}
	if err := p.want("from"); err != nil {
		return err
	}
	from, err := p.ident()
	if err != nil {
		return err
	}
	rx.From = from
	if p.kw("as") {
		if err := p.next(); err != nil {
			return err
		}
		as, err := p.ident()
		if err != nil {
			return err
		}
		rx.As = as
	}
	e.Rez = rx
	return nil
}

func (p *parser) linkExp(e *Expect) error {
	if err := p.want("link"); err != nil {
		return err
	}
	if err := p.want("on"); err != nil {
		return err
	}
	name, err := p.ident()
	if err != nil {
		return err
	}
	if err := p.want("from"); err != nil {
		return err
	}
	if err := p.want("link"); err != nil {
		return err
	}
	from, err := p.integer()
	if err != nil {
		return err
	}
	if err := p.want("num"); err != nil {
		return err
	}
	num, err := p.integer()
	if err != nil {
		return err
	}
	if err := p.want("text"); err != nil {
		return err
	}
	text, err := p.text()
	if err != nil {
		return err
	}
	lx := &LinkExp{Name: name, From: from, Num: num, Text: text}
	if p.kw("key") {
		if err := p.next(); err != nil {
			return err
		}
		k, err := p.key()
		if err != nil {
			return err
		}
		lx.Key = &k
	}
	if p.kw("heard") {
		if err := p.next(); err != nil {
			return err
		}
		if err := p.want("by"); err != nil {
			return err
		}
		n, err := p.integer()
		if err != nil {
			return err
		}
		lx.HeardBy = &n
	}
	e.Link = lx
	return nil
}

// wear reads wear ident on string as ident.
func (p *parser) wear() (*Wear, error) {
	if err := p.want("wear"); err != nil {
		return nil, err
	}
	item, err := p.ident()
	if err != nil {
		return nil, err
	}
	if err := p.want("on"); err != nil {
		return nil, err
	}
	point, sp, err := p.str()
	if err != nil {
		return nil, err
	}
	if err := p.want("as"); err != nil {
		return nil, err
	}
	as, err := p.ident()
	if err != nil {
		return nil, err
	}
	return &Wear{Item: item, Point: point, PointSpan: sp, As: as}, nil
}

// rezItem reads rez ident (at | by) number number number as ident. Only a
// step's first word is this stimulus; after expect, rez is the expectation.
func (p *parser) rezItem() (*RezItem, error) {
	if err := p.want("rez"); err != nil {
		return nil, err
	}
	item, err := p.ident()
	if err != nil {
		return nil, err
	}
	r := &RezItem{Item: item}
	switch {
	case p.kw("at"):
	case p.kw("by"):
		r.By = true
	default:
		return nil, p.unexpected("expected at or by")
	}
	if err := p.next(); err != nil {
		return nil, err
	}
	for _, n := range []*Number{&r.X, &r.Y, &r.Z} {
		if *n, err = p.number(); err != nil {
			return nil, err
		}
	}
	if err := p.want("as"); err != nil {
		return nil, err
	}
	if r.As, err = p.ident(); err != nil {
		return nil, err
	}
	return r, nil
}

// takeOff reads take off binding.
func (p *parser) takeOff() (*TakeOff, error) {
	if err := p.want("take"); err != nil {
		return nil, err
	}
	if err := p.want("off"); err != nil {
		return nil, err
	}
	name, err := p.ident()
	if err != nil {
		return nil, err
	}
	return &TakeOff{Name: name}, nil
}

// drop reads drop ident (into | onto) binding link? and, for onto, face
// integer.
func (p *parser) drop() (*Drop, error) {
	if err := p.want("drop"); err != nil {
		return nil, err
	}
	item, err := p.ident()
	if err != nil {
		return nil, err
	}
	d := &Drop{Item: item}
	switch {
	case p.kw("into"):
	case p.kw("onto"):
		d.Onto = true
	default:
		return nil, p.unexpected("expected into or onto")
	}
	if err := p.next(); err != nil {
		return nil, err
	}
	if d.Name, err = p.ident(); err != nil {
		return nil, err
	}
	if d.Link, err = p.optLink(); err != nil {
		return nil, err
	}
	if d.Onto {
		if err := p.want("face"); err != nil {
			return nil, err
		}
		if d.Face, err = p.integer(); err != nil {
			return nil, err
		}
	}
	return d, nil
}

// setGroup reads group ident (string | none).
func (p *parser) setGroup() (*SetGroup, error) {
	if err := p.want("group"); err != nil {
		return nil, err
	}
	av, err := p.ident()
	if err != nil {
		return nil, err
	}
	g := &SetGroup{Avatar: av}
	if p.kw("none") {
		g.None = true
		g.GroupSpan = p.tok.span
		return g, p.next()
	}
	g.Group, g.GroupSpan, err = p.str()
	return g, err
}

// buttonExp reads button binding link? part+ face? ( changes / state
// buttonval ). The negative forms are the expectation's own no.
func (p *parser) buttonExp(e *Expect) error {
	if err := p.want("button"); err != nil {
		return err
	}
	name, err := p.ident()
	if err != nil {
		return err
	}
	link, err := p.optLink()
	if err != nil {
		return err
	}
	b, err := p.buttonBody()
	if err != nil {
		return err
	}
	st, err := p.state()
	if err != nil {
		return err
	}
	x := &ButtonExp{Name: name, Link: link, Button: b, State: st}
	if hasValue(st) {
		x.ValSpan = p.tok.span
		switch {
		case p.kw("shown"):
			x.Val = ButtonShown
		case p.kw("gone"):
			x.Val = ButtonGone
		case p.kw("count"):
			x.Val = ButtonCount
			if err := p.next(); err != nil {
				return err
			}
			if p.tok.kind == kFloat {
				return p.errorf("a count is a whole number")
			}
			if x.Count, err = p.integer(); err != nil {
				return err
			}
			x.ValSpan = cover(x.ValSpan, x.Count.Span)
			e.Button = x
			return nil
		default:
			return p.unexpected("expected shown, gone, count N, or original")
		}
		if err := p.next(); err != nil {
			return err
		}
	}
	e.Button = x
	return nil
}

// attachExp reads attached binding ( on string / off ).
func (p *parser) attachExp(e *Expect) error {
	if err := p.want("attached"); err != nil {
		return err
	}
	name, err := p.ident()
	if err != nil {
		return err
	}
	x := &AttachExp{Name: name}
	switch {
	case p.kw("off"):
		x.Off = true
		if err := p.next(); err != nil {
			return err
		}
	case p.kw("on"):
		if err := p.next(); err != nil {
			return err
		}
		if x.Point, x.PointSpan, err = p.str(); err != nil {
			return err
		}
	default:
		return p.unexpected("expected on or off")
	}
	e.Attached = x
	return nil
}

// animationExp reads animation uuid ( changes / state on-or-off ) ( from
// binding )?.  There is no original and no any and no capture: an
// animation is on or it is off, and the id is written.
func (p *parser) animationExp(e *Expect) error {
	if err := p.want("animation"); err != nil {
		return err
	}
	if p.tok.kind != kUUID {
		return p.unexpected("expected an animation's asset UUID")
	}
	x := &AnimationExp{ID: p.tok.text, IDSpan: p.tok.span}
	if err := p.next(); err != nil {
		return err
	}
	st, err := p.state()
	if err != nil {
		return err
	}
	if st.Original {
		return p.errorf("an animation is on or off and has no original; write on or off")
	}
	x.State = st
	if st.Kind != StateChanges {
		switch {
		case p.kw("on"):
			x.On = true
		case p.kw("off"):
		default:
			return p.unexpected("expected on or off")
		}
		if err := p.next(); err != nil {
			return err
		}
	}
	if p.kw("from") {
		if err := p.next(); err != nil {
			return err
		}
		from, err := p.ident()
		if err != nil {
			return err
		}
		x.From = &from
	}
	e.Animation = x
	return nil
}

// soundExp reads sound uuid, and then either ( from binding )? ( gain
// number )? for a sound heard, or ( is / becomes ) ( looping / stopped ) or
// changes, ( from binding )? for a loop's state.  A sound is not a state
// the tester is in, so there is no original, any or capture; and a gain
// belongs to a play, which a loop's state has none of.
func (p *parser) soundExp(e *Expect) error {
	if err := p.want("sound"); err != nil {
		return err
	}
	if p.tok.kind != kUUID {
		return p.unexpected("expected a sound's asset UUID")
	}
	x := &SoundExp{ID: p.tok.text, IDSpan: p.tok.span}
	if err := p.next(); err != nil {
		return err
	}
	if p.kw("is") || p.kw("becomes") || p.kw("changes") {
		st, err := p.state()
		if err != nil {
			return err
		}
		if st.Original {
			return p.errorf("a loop is looping or stopped and has no original; write looping or stopped")
		}
		x.State = &st
		if st.Kind != StateChanges {
			switch {
			case p.kw("looping"):
				x.Loop = true
			case p.kw("stopped"):
			default:
				return p.unexpected("expected looping or stopped")
			}
			if err := p.next(); err != nil {
				return err
			}
		}
	}
	if p.kw("from") {
		if err := p.next(); err != nil {
			return err
		}
		from, err := p.ident()
		if err != nil {
			return err
		}
		x.From = &from
	}
	if x.State == nil && p.kw("gain") {
		if err := p.next(); err != nil {
			return err
		}
		n, err := p.number()
		if err != nil {
			return err
		}
		x.Gain = &n
	}
	e.Sound = x
	return nil
}

// ident reads any word as a name: a binding may be spelled like a word the
// grammar uses, since a name only ever sits where the grammar expects one.
func (p *parser) ident() (Ident, error) {
	if p.tok.kind != kWord {
		return Ident{}, p.unexpected("expected a name")
	}
	id := Ident{Text: p.tok.text, Span: p.tok.span}
	return id, p.next()
}

func (p *parser) integer() (Int, error) {
	if p.tok.kind != kInt {
		return Int{}, p.unexpected("expected an integer")
	}
	n := Int{Span: p.tok.span, Text: p.tok.text, Value: p.tok.intv, OK: p.tok.intOK}
	return n, p.next()
}

func (p *parser) number() (Number, error) {
	switch p.tok.kind {
	case kFloat:
		n := Number{Span: p.tok.span, Value: p.tok.f, Zero: p.tok.f == 0, Exact: true}
		return n, p.next()
	case kInt:
		n := Number{Span: p.tok.span}
		if p.tok.intOK {
			f := float64(p.tok.intv)
			n.Value = f
			n.Zero = p.tok.intv == 0
			n.Exact = int64(f) == p.tok.intv
		}
		return n, p.next()
	default:
		return Number{}, p.unexpected("expected a number")
	}
}

// duration reads a duration token. A float glued to s, ms, or m, such as
// 1.5s, is kept as Duration.Bad so Check can report the static error.
// The lexer has already split that pair; it is not a duration token.
func (p *parser) duration() (Duration, error) {
	if p.tok.kind == kDuration {
		d := Duration{Span: p.tok.span, Value: p.tok.dur}
		return d, p.next()
	}
	if p.tok.kind == kFloat {
		n, err := p.lex.Peek()
		if err != nil {
			return Duration{}, err
		}
		if n.kind == kWord && (n.text == "s" || n.text == "ms" || n.text == "m") {
			sp := cover(p.tok.span, n.span)
			if err := p.next(); err != nil {
				return Duration{}, err
			}
			if err := p.next(); err != nil {
				return Duration{}, err
			}
			return Duration{Span: sp, Bad: true}, nil
		}
	}
	return Duration{}, p.unexpected("expected a duration")
}

// capture reads $name.
func (p *parser) capture() (Capture, error) {
	if p.tok.kind != kCapture {
		return Capture{}, p.unexpected("expected a capture such as $name")
	}
	c := Capture{Span: p.tok.span, Name: strings.TrimPrefix(p.tok.text, "$")}
	return c, p.next()
}

// text reads text = string / "matching" string / capture.
func (p *parser) text() (Text, error) {
	start := p.tok.span
	if p.tok.kind == kCapture {
		c, err := p.capture()
		if err != nil {
			return Text{}, err
		}
		return Text{Span: c.Span, ValueSpan: c.Span, Capture: &c}, nil
	}
	if p.kw("any") {
		return Text{}, p.errorf("any is only a reading of a state expectation (is any as $x); here a string, matching or a capture is expected")
	}
	pattern := p.kw("matching")
	if pattern {
		if err := p.next(); err != nil {
			return Text{}, err
		}
		if p.tok.kind == kCapture {
			return Text{}, p.errorf("a capture is not a pattern; write the capture alone to compare with the value whole")
		}
	}
	s, sp, err := p.str()
	if err != nil {
		return Text{}, err
	}
	return Text{Span: cover(start, sp), ValueSpan: sp, Value: s, Pattern: pattern}, nil
}

// str reads a literal string. matching is refused with its own message
// where only a literal is legal: stimuli, button labels, choose, answer.
func (p *parser) str() (string, Span, error) {
	if p.kw("matching") {
		return "", Span{}, p.errorf("matching is not legal here; only text an expectation compares can be a pattern")
	}
	if p.tok.kind != kString {
		return "", Span{}, p.unexpected("expected a string")
	}
	s, sp := p.tok.text, p.tok.span
	return s, sp, p.next()
}

func (p *parser) key() (Key, error) {
	if p.tok.kind == kCapture {
		c, err := p.capture()
		return Key{Span: c.Span, Use: &c}, err
	}
	if p.tok.kind == kUUID {
		k := Key{Span: p.tok.span, ID: p.tok.text}
		return k, p.next()
	}
	if p.kw("null") {
		k := Key{Span: p.tok.span, Null: true, ID: nullKey}
		return k, p.next()
	}
	return Key{}, p.unexpected("expected a key")
}

// beginsItem says the current word starts a step, a header or a block,
// and so cannot be part of a list before it.
func (p *parser) beginsItem() bool {
	if p.startsStimulus() {
		return true
	}
	switch p.tok.text {
	case "expect", "then", "do", "test", "sequence", "before", "after",
		"object", "avatar", "item", "allow", "probe", "listen", "timeout", "slate":
		return true
	}
	return false
}
