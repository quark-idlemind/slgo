// Package slate parses a Slate 1 script.
//
// Parse is the lexer and the grammar. Check is the static rules in the
// Slate specification. Expand lists each test's steps as a runner runs
// them. None of them dials a region. A file that fails Parse or Check is
// exit 2 for the command, and the process has not looked at the grid.
// Run (run.go) drives a checked script against an sl.Session.
//
// # Experimental
//
// Slate, this package and the slate command are experimental: not
// covered by slgo's compatibility promise, and they may change in any
// release, the language included.
package slate

import (
	"fmt"
	"time"

	"github.com/quark-idlemind/slgo/sl"
)

// DefaultTimeout is the deadline when the script has no timeout header
// and an expectation has no within.
const DefaultTimeout = 10 * time.Second

// Duration bounds. A shorter deadline can expire between the send and
// the first read. The cap stops a stray unit from becoming an hour.
const (
	MinDuration = 100 * time.Millisecond
	MaxDuration = 120 * time.Second
)

// maxSay is the chat field. Session.Say does not enforce it; a
// non-negative say is checked here so the line is not cut. A negative
// say uses sl.MaxDialogReply, because that path is sayNegative.
const maxSay = 1023

// maxListens is the most listen channels a script may name. The bridge
// script opens one listen for its control channel and one per listen
// channel; 65 is the published per-script limit, and one is kept spare.
const maxListens = 63

// nullKey is the UUID the wire sends when key is omitted or written null.
const nullKey = "00000000-0000-0000-0000-000000000000"

// nonceHex is the width of a probe nonce. The value is substituted at
// install; only the length matters to the static relay-line check.
const nonceHex = "0000000000000000"

// LinkWords are the link targets written as words. The numbers are the
// LSL constants LINK_SET, LINK_ALL_OTHERS, LINK_ALL_CHILDREN, LINK_THIS,
// and LINK_ROOT.
var LinkWords = map[string]int32{
	"all":      -1,
	"others":   -2,
	"children": -3,
	"this":     -4,
	"root":     1,
}

// ClickBytes maps a click name to PRIM_CLICK_ACTION. touch and none are
// both zero. The values are Linden's published constants.
var ClickBytes = map[string]uint8{
	"touch":    sl.ClickTouch,
	"none":     sl.ClickTouch,
	"sit":      sl.ClickSit,
	"buy":      sl.ClickBuy,
	"pay":      sl.ClickPay,
	"open":     sl.ClickOpen,
	"play":     sl.ClickPlay,
	"media":    sl.ClickOpenMedia,
	"zoom":     sl.ClickZoom,
	"disabled": sl.ClickDisabled,
}

// Error is a lexical, grammatical, or static failure at one byte.
// The text is the exit-2 line: filename:line:column: reason.
type Error struct {
	File   string
	Line   int
	Column int
	Msg    string
}

func (e *Error) Error() string {
	if e.Line == 0 {
		return e.Msg
	}
	if e.File == "" {
		return fmt.Sprintf("%d:%d: %s", e.Line, e.Column, e.Msg)
	}
	return fmt.Sprintf("%s:%d:%d: %s", e.File, e.Line, e.Column, e.Msg)
}

// Span is a half-open byte range of Script.Src. Line and Column are
// 1-based and count bytes, at the first byte of the range.
type Span struct {
	Start, End int
	Line, Col  int
}

func cover(a, b Span) Span {
	if a.Line == 0 {
		return b
	}
	if b.Line == 0 {
		return a
	}
	return Span{Start: a.Start, End: b.End, Line: a.Line, Col: a.Col}
}

// Ident is a script name, not an in-world name. In-world names are strings.
type Ident struct {
	Text string
	Span Span
}

// Int is an integer token. OK is false when the digits do not fit in int64.
// A channel still has to fit in int32, and that is checked at the use.
type Int struct {
	Span  Span
	Text  string
	Value int64
	OK    bool
}

// Number is a float, or an integer used where a float is legal.
// Zero is true for 0 and 0.0, the values placeTouches treats as not given.
// Exact is false when an integer has no exact float64 value.
type Number struct {
	Span  Span
	Value float64
	Zero  bool
	Exact bool
}

// Duration is a timeout, a within, or a drag's over.
// Bad is the form with a dot, such as 1.5s, which is not a duration token.
type Duration struct {
	Span  Span
	Value time.Duration
	Bad   bool
}

// Script is one Slate 1 file. Check has not been applied unless the
// caller ran it; Parse only lexes and parses. A file of plain steps has
// one implicit Test and nothing else; a suite has Tests in file order and
// any before each, after each and sequences.
type Script struct {
	File string
	Src  []byte

	Timeouts  []Duration
	Allows    []Span
	Permits   []PermissionAllow
	Objects   []Object
	Avatars   []Avatar
	Items     []Item
	Probes    []Probe
	Listens   []Listen
	Tests     []Test
	Befores   []Block // Check allows at most one
	Afters    []Block // Check allows at most one
	Sequences []Sequence
}

// PermissionAllow is allow permission NAME... from OBJ: the permissions a
// request from OBJ may be granted. Names are as written; Check validates them.
type PermissionAllow struct {
	Span  Span
	Names []Ident
	From  Ident
}

// Before is the before each block, or nil. It is meaningful after Check.
func (s *Script) Before() *Block {
	if s == nil || len(s.Befores) == 0 {
		return nil
	}
	return &s.Befores[0]
}

// After is the after each block, or nil. It is meaningful after Check.
func (s *Script) After() *Block {
	if s == nil || len(s.Afters) == 0 {
		return nil
	}
	return &s.Afters[0]
}

// Sequence returns the sequence called name, or nil.
func (s *Script) Sequence(name string) *Sequence {
	for i := range s.Sequences {
		if s.Sequences[i].Name.Text == name {
			return &s.Sequences[i]
		}
	}
	return nil
}

// Test is one test. In a file of plain steps it is the only one, Implicit
// is true and Name is the file's base name without .slate; Span is then
// the whole body.
type Test struct {
	Span     Span
	Name     string
	NameSpan Span
	Implicit bool
	Steps    []Step
}

// Block is a before each or after each block. Span covers the keywords
// through the closing brace.
type Block struct {
	Span  Span
	Steps []Step
}

// Sequence is a named list of steps that do inlines.
type Sequence struct {
	Span  Span
	Name  Ident
	Steps []Step
}

// Do is the step do NAME.
type Do struct {
	Span Span
	Name Ident
}

// Capture is a captured value written $name: a binding on an expectation
// (as $name) or a use in a value position. Name is without the $. A
// capture is data and is never a pattern.
// Why: doc/slate-language.md#lexical-grammar
type Capture struct {
	Span Span
	Name string
}

func (c Capture) String() string { return "$" + c.Name }

// CaptureType is what a capture holds, the type of the place that bound it.
type CaptureType int

const (
	CapText   CaptureType = iota // a line, a message, an item name, a group, a label
	CapUUID                      // a texture reading
	CapPair                      // an offset or repeats reading
	CapNumber                    // a rotation, glow or alpha reading
	CapClick                     // a click action reading
	CapTriple                    // a colour reading
	CapOnOff                     // a fullbright reading
	CapVector                    // a position or size reading
)

func (t CaptureType) String() string {
	switch t {
	case CapText:
		return "text"
	case CapUUID:
		return "uuid"
	case CapPair:
		return "pair"
	case CapNumber:
		return "number"
	case CapClick:
		return "click"
	case CapTriple:
		return "colour triple"
	case CapOnOff:
		return "on or off"
	case CapVector:
		return "vector"
	default:
		return "value"
	}
}

// Text is a literal string, a pattern when written matching "RE", or a
// capture. Span covers the whole phrase; ValueSpan is the string or
// capture token. A Pattern is Go RE2 syntax, unanchored, and is compiled
// by Check. When Capture is set Value is empty and Pattern is false.
type Text struct {
	Span      Span
	ValueSpan Span
	Value     string
	Pattern   bool
	Capture   *Capture
}

// StateKind is the comparison word of a state expectation.
type StateKind int

const (
	StateIs      StateKind = iota // is VALUE
	StateBecomes                  // becomes VALUE
	StateChanges                  // changes, which takes no value
)

// State is is, becomes or changes. When Kind is StateChanges there is no
// value and Original is false. Original means the value is original.
type State struct {
	Span     Span
	Kind     StateKind
	Original bool
}

// Source returns the bytes of sp. The caller must not modify Src.
func (s *Script) Source(sp Span) string {
	if s == nil || sp.Start < 0 || sp.End > len(s.Src) || sp.Start > sp.End {
		return ""
	}
	return string(s.Src[sp.Start:sp.End])
}

// TimeoutDuration is the header, or DefaultTimeout when the header is absent.
// It is meaningful after Check has accepted the script.
func (s *Script) TimeoutDuration() time.Duration {
	if s == nil || len(s.Timeouts) == 0 || s.Timeouts[0].Bad {
		return DefaultTimeout
	}
	return s.Timeouts[0].Value
}

// Object is one object header. Name is the script binding. World is the
// prim name ObjectsNamed matches, exactly, in the case it has. With
// HasDesc, Desc keeps only the matches whose description it fits: the
// qualifier that tells apart objects of one name.
type Object struct {
	Span      Span
	Name      Ident
	World     string
	WorldSpan Span
	HasDesc   bool
	Desc      Text
}

// Avatar is one avatar header: a binding for a second avatar the run is
// given, with --avatar NAME=PROFILE. It holds no in-world name.
// Why: doc/slate-language.md#a-second-avatar
type Avatar struct {
	Span Span
	Name Ident
}

// Item is one item header: an inventory item, by name, in a top-level
// folder. Name is the script binding, which wear, rez and drop use.
type Item struct {
	Span       Span
	Name       Ident
	World      string // the item's name in inventory
	WorldSpan  Span
	Folder     string
	FolderSpan Span
}

// Probe is one probe header: the object that gets a probe script.
type Probe struct {
	Span Span
	Name Ident
}

// Listen is one listen header: a channel the viewer does not deliver,
// forwarded by the bridge.
type Listen struct {
	Span    Span
	Channel Int
}

// Step is one stimulus and the expectations armed with it, an
// expectation-only step, or a do call. Then is true when the step was
// opened with then. A nil Stimulus is the expectation-only form, whose
// failure line is stimulus: (none). When Do is set the step is only the
// call: no stimulus, no expectations.
type Step struct {
	Span     Span
	Then     bool
	Stimulus *Stimulus
	Expect   []Expect
	Do       *Do
}

// Stimulus is one action. Exactly one field is set.
type Stimulus struct {
	Span    Span
	Touch   *Touch
	Drag    *Drag
	Say     *Say
	Pay     *Pay
	Sit     *Sit
	Stand   *Stand
	Wait    *Wait
	Choose  *Choose
	Answer  *Answer
	Send    *Send
	Wear    *Wear
	Rez     *RezItem
	TakeOff *TakeOff
	Drop    *Drop
	Group   *SetGroup

	// OtherAs is an as NAME written after a stimulus that is the
	// tester's alone; Check refuses it.
	OtherAs *Ident
}

// Wear is wear ITEM on "point" as NAME. As is a new object binding, the
// worn root; it is usable by the step's own expectations.
type Wear struct {
	Item      Ident
	Point     string // an attachment point name, which Check resolves
	PointSpan Span
	As        Ident
}

// RezItem is rez ITEM (at X Y Z | by DX DY DZ) as NAME, the stimulus.
// By is an offset from the tester's own position, else the numbers are a
// region position. As is a new object binding, the rezzed root. It is not
// the rez expectation (RezExp), which a step reaches only after expect.
type RezItem struct {
	Item    Ident
	By      bool
	X, Y, Z Number
	As      Ident
}

// TakeOff is take off NAME: the binding stops being usable.
type TakeOff struct{ Name Ident }

// Drop is drop ITEM into OBJ (link N)? or drop ITEM onto OBJ (link N)? face N:
// an inventory item put into a prim's contents, or a texture put on one
// face. Item is an item header; Name is the object. Face is set only with
// Onto.
// Why: doc/slate-language.md#stimuli
type Drop struct {
	Item Ident
	Onto bool
	Name Ident
	Link *Int
	Face Int
}

// SetGroup is group NAME "Group Name" or group NAME none: the active group
// of the second avatar NAME. Group is as written; None is the word none.
// Why: doc/slate-language.md#stimuli
type SetGroup struct {
	Avatar    Ident
	Group     string
	GroupSpan Span
	None      bool
}

// Touch is touch OBJ, then anywhere, a link, a face, or a button.
// A link with no further refine is the zero touch on that prim.
type Touch struct {
	Name     Ident
	Link     *Int
	Anywhere bool
	Face     *Int
	At       *ST
	Button   *Button
	Showing  *Showing // touch OBJ showing ...; Check refuses it beside any other target
	Guard    *Span    // if shown, after a button; Check allows it in before each and after each only
	AsAvatar *Ident   // as NAME: a second avatar touches; nil is the tester
}

// Showing is the refine "showing" uuidval ( "at" number number )?: touch
// the one face of the binding's linkset that shows a texture. Exactly one
// of ID and Use is set.
type Showing struct {
	Span Span
	ID   string   // canonical lowercase UUID
	Use  *Capture // a uuid capture
	At   *ST
}

// ST is a pair of surface coordinates. Both zero is the static origin error.
type ST struct {
	Span Span
	S, T Number
}

// Button is the finder request for a touch. Nth is 1-based when set.
// Face restricts the search; without it every face of the prim is searched.
type Button struct {
	Nth   *Int
	Parts []Part
	Face  *Int
}

// PartKind is one piece of a button.
type PartKind int

const (
	PartText PartKind = iota
	PartPattern
	PartSymbol
	PartImage
	PartBox
	PartCircle
	PartOval
)

// Part is one button part, in the order the author wrote them.
type Part struct {
	Span    Span
	Kind    PartKind
	Text    string
	Capture *Capture // PartText only: text $name, whose value is Text
}

// Drag is one segment. A nil Over means the runner moves for 500ms. A
// drag given on the screen has Screen set, and then Link, Face, From and
// To are unused.
type Drag struct {
	Name Ident
	Link *Int
	Face Int
	From ST
	To   ST
	Over *Duration
	// Press is how long to hold still where the drag starts before
	// moving, and Dwell how long at the end before letting go; nil is
	// none.
	Press, Dwell *Duration
	Screen       *ScreenDrag
	AsAvatar     *Ident // as NAME after a face drag: a second avatar drags; nil is the tester
}

// ScreenDrag is the rest of drag OBJ on screen from ...: where it starts,
// where it ends, and whether to settle after the press.
// Why: doc/slate-language.md#stimuli
type ScreenDrag struct {
	Span Span

	// The start is a point in pixels (Face nil; S is X and T is Y), or a
	// point on a face of the binding's linkset (Face set; a nil Link is
	// the root, and At is S,T).
	FromPixels ST
	Link       *Int
	Face       *Int
	At         ST

	// To is X,Y in pixels from the top left of the world view; with By
	// it is the distance from the start instead.
	To ST
	By bool

	Settle bool
}

// Say is the tester speaking. A nil As is the tester, the default.
type Say struct {
	Text     string
	TextSpan Span
	Channel  Int
	As       *Speaker
}

// SpeakerKind is who speaks a say, or who an expectation attributes it to.
type SpeakerKind int

const (
	SpeakTester SpeakerKind = iota
	SpeakOwner
	SpeakAvatar
	SpeakObject
	SpeakAnyone
	SpeakSecond // a second avatar's binding (Name): as NAME, or from avatar NAME
)

// Speaker is an as or from clause. Name is set for the owner, for an
// object and for a second avatar. Avatar is the displayed name, matched with EqualFold of the whole
// string. Anyone is an expectation only.
type Speaker struct {
	Span   Span
	Kind   SpeakerKind
	Name   Ident
	Avatar string
	Link   *Int // from object OBJ link N; SpeakObject only
}

// Pay is one payment. The header allow pay is the file half of the gate;
// the process flag is not visible here.
type Pay struct {
	Name       Ident
	Amount     Int
	Linden     bool
	Reason     string
	HasReason  bool
	ReasonSpan Span
}

// Sit seats the tester on Name.
type Sit struct{ Name Ident }

// Stand stands the tester up. It names no object.
type Stand struct{}

// Wait does nothing for a while: what a product needs between one touch
// and the next, which it ignores when they come too close together.
// Why: doc/slate-language.md#stimuli
type Wait struct{ For Duration }

// ChooseKind is how choose names the button.
type ChooseKind int

const (
	ChooseLiteral  ChooseKind = iota // choose "Red"
	ChooseMatching                   // choose matching "RE"
	ChooseButton                     // choose button N
	ChooseCapture                    // choose $x
)

// Choose presses a dialog button. Label is the string of a literal or of
// matching (empty otherwise); Index is the N of button N; Use is the
// capture of choose $x.
type Choose struct {
	Kind      ChooseKind
	Label     string
	LabelSpan Span
	Index     *Int
	Use       *Capture
	Name      Ident
	AsAvatar  *Ident // as NAME: the dialog held for a second avatar; nil is the tester's
}

// Answer types into a text box.
type Answer struct {
	Text     string
	TextSpan Span
	Name     Ident
	AsAvatar *Ident // as NAME: the text box held for a second avatar; nil is the tester's
}

// Send asks a probe to llMessageLinked. A nil Key is the null key.
type Send struct {
	Name     Ident
	From     Int
	To       LinkTarget
	Num      Int
	Text     string
	TextSpan Span
	// TextCapture is set for text $name; Text is then empty.
	TextCapture *Capture
	Key         *Key
}

// LinkTarget is an integer or one of LinkWords.
type LinkTarget struct {
	Span Span
	Word string
	Int  Int
}

// Key is a UUID, null, or a uuid capture. ID is the canonical lowercase
// form for the first two and empty for a capture, which is in Use.
type Key struct {
	Span Span
	Null bool
	ID   string
	Use  *Capture
}

// Expect is one expectation. Neg is expect no. Exactly one body is set.
type Expect struct {
	Span       Span
	Neg        bool
	Within     *Duration
	Near       *Near    // near N or near N percent, after the body and before within; Check allows it on a numeric state expectation
	As         *Capture // as $name after within; Check allows it on a positive state, say, dialog, textbox or give
	Say        *SayExp
	Dialog     *DialogExp
	TextBox    *BoxExp
	Texture    *TextureExp
	Offset     *VecExp
	Repeats    *VecExp
	Rot        *RotExp
	Click      *ClickExp
	FloatText  *TextExp
	Fullbright *FullbrightExp
	Glow       *GlowExp
	Colour     *ColourExp
	Alpha      *AlphaExp
	AlphaMode  *AlphaModeExp
	Material   *MaterialExp // normalmap, specularmap, glossiness or environment
	Position   *VecExp3
	Size       *VecExp3
	Turn       *VecExp3 // a prim's own rotation, as Euler degrees X Y Z
	Give       *GiveExp
	Rez        *RezExp
	Link       *LinkExp
	Button     *ButtonExp
	Attached   *AttachExp
}

// Near is the tolerance of a state expectation: near N, an amount in
// the reading's own unit, or near N percent, a share of the wanted
// component. The parser reads it after any expectation body so that Check
// can say which ones take none.
// Why: doc/slate-language.md#tolerances
type Near struct {
	Span    Span
	Amount  Number
	Percent bool
}

// SayExp is expect say. The order in the file is text, channel, speaker.
type SayExp struct {
	Text    Text
	Channel ExpectChan
	From    Speaker
}

// ChanKind is how an expectation names a channel.
type ChanKind int

const (
	ChanNumber ChanKind = iota
	ChanPublic
	ChanOwner
	ChanDebug
	ChanDirect
)

// ExpectChan is on public, on owner, on debug, on direct, or on an integer.
// The integer 0 is public chat. The integer 2147483647 is debug chat.
type ExpectChan struct {
	Span Span
	Kind ChanKind
	Int  Int
}

// DialogExp is a dialog offered to the tester, or with To to a second avatar. Only rejects extra buttons.
// HasText is false when no text clause was written: any message matches.
// Clauses are the button clauses in source order. Buttons is the legacy
// list: the value of each clause that is a plain literal with no number.
// Count is the N of count N, when written.
type DialogExp struct {
	Name    Ident
	Link    *Int
	To      *Ident // to NAME: the dialog came to a second avatar; nil is the tester
	Text    Text
	HasText bool
	Buttons []string
	Clauses []DButton
	Only    bool
	Count   *Int

	// Ordered is the word ordered, with its span for the static check;
	// Sorted is sorted and its optional pattern.
	Ordered     bool
	OrderedSpan Span
	Sorted      *Sorted
}

// Sorted is sorted ( matching "RE" )?. Without a pattern every label is
// compared; with one only the labels it matches, by its group when it has one.
type Sorted struct {
	Span        Span
	Matching    bool
	Pattern     string
	PatternSpan Span
}

// DButton is one button clause: button integer? text, where text is a
// literal, matching "RE", or a capture. Nth is 1-based when set.
type DButton struct {
	Span Span
	Nth  *Int
	Text Text
}

// BoxExp is a text box. It has no button list.
type BoxExp struct {
	Name Ident
	Link *Int
	To   *Ident // as for DialogExp
	Text Text
}

// TextureExp is one face's texture id, canonical lowercase. With
// State.Original set, ID is empty.
//
// Face is the number; with FaceAll set (face all) it is the zero Int whose
// Span is the word all. Any is is any, a reading that matches anything;
// Use is a uuid capture in the place of the UUID.
type TextureExp struct {
	Name    Ident
	Link    *Int
	Face    Int
	FaceAll bool
	State   State
	ID      string
	Any     bool
	Use     *Capture
}

// VecExp is offset or repeats. Both components are floats; with
// State.Original or StateChanges they are zero.
// FaceAll, Any and Use are as for TextureExp; Use is a pair capture.
type VecExp struct {
	Name    Ident
	Link    *Int
	Face    Int
	FaceAll bool
	State   State
	S, T    Number
	Any     bool
	Use     *Capture
}

// RotExp is a rotation as a fraction of a turn, in [-1, 1].
// FaceAll, Any and Use are as for TextureExp; Use is a number capture.
type RotExp struct {
	Name    Ident
	Link    *Int
	Face    Int
	FaceAll bool
	State   State
	Turns   Number
	Any     bool
	Use     *Capture
}

// VecExp3 is position or size of a prim: three numbers, with no face. With
// State.Original or StateChanges they are zero. Any and Use are as for
// TextureExp; Use is a vector capture.
type VecExp3 struct {
	Name    Ident
	Link    *Int
	State   State
	X, Y, Z Number
	Any     bool
	Use     *Capture
}

// ClickExp names a PRIM_CLICK_ACTION. The byte is ClickBytes[Action];
// Action is empty for changes and original.
// Any and Use are as for TextureExp; Use is a click capture.
type ClickExp struct {
	Name   Ident
	Link   *Int
	State  State
	Action string
	Any    bool
	Use    *Capture
}

// TextExp is expect text OBJ link? changes / is / becomes: the floating text
// of a prim. Value is the text of an is or becomes with a value, a literal,
// a pattern or a capture (its Use); it is zero for changes and original.
// Any is as for TextureExp.
type TextExp struct {
	Name  Ident
	Link  *Int
	State State
	Value Text
	Any   bool
}

// FullbrightExp is expect fullbright OBJ link? faceall changes / is on|off.
// On is the value for on and off; the other values are State.Original,
// Any (is any, with as) and Use, an on-or-off capture. FaceAll is
// face all, with Face as for TextureExp.
type FullbrightExp struct {
	Name    Ident
	Link    *Int
	Face    Int
	FaceAll bool
	State   State
	On      bool
	Any     bool
	Use     *Capture
}

// GlowExp is a face's glow, 0 to 1. Value is the literal; Use is a
// number capture. The other fields are as for FullbrightExp.
type GlowExp struct {
	Name    Ident
	Link    *Int
	Face    Int
	FaceAll bool
	State   State
	Value   Number
	Any     bool
	Use     *Capture
}

// ColourExp is a face's colour, three numbers 0 to 1. Use is a triple
// capture. The other fields are as for FullbrightExp.
type ColourExp struct {
	Name    Ident
	Link    *Int
	Face    Int
	FaceAll bool
	State   State
	R, G, B Number
	Any     bool
	Use     *Capture
}

// AlphaExp is a face's opacity, 0 to 1, as GlowExp.
type AlphaExp struct {
	Name    Ident
	Link    *Int
	Face    Int
	FaceAll bool
	State   State
	Value   Number
	Any     bool
	Use     *Capture
}

// AlphaModeExp is expect alphamode OBJ link? face N changes / is / becomes:
// how a face is drawn where its texture has alpha. Mode is the word of an
// is or becomes with a value, one of AlphaModes; it is empty for changes
// and original. FaceAll is kept so that the check can refuse it with the
// place it was written: a mode is read from one face's material.
// Why: doc/slate-runner.md#alpha-mode
type AlphaModeExp struct {
	Name    Ident
	Link    *Int
	Face    Int
	FaceAll bool
	State   State
	Mode    string
	ModeAt  Span
}

// AlphaModes are the words an alphamode expectation takes, as sl.AlphaMode
// names them.
var AlphaModes = []string{"default", "none", "blend", "mask", "emissive"}

// MaterialProps are the words of the expectations that read one field of a
// face's material: the two maps, by texture id, and the two levels, 0 to
// 255 as PRIM_SPECULAR gives them.
var MaterialProps = []string{"normalmap", "specularmap", "glossiness", "environment"}

// MaterialExp is expect normalmap, specularmap, glossiness or environment
// OBJ link? face N or face all: one field of the face's material, which a
// face with no material reads as the null key or 0. Prop is the word. ID
// is the literal of a map, canonical lowercase, and the null key for the
// word none; Value is the literal of a level. Both are empty for changes
// and original. Any and Use are as for TextureExp, Use a uuid capture for
// a map and a number capture for a level.
// Why: doc/slate-runner.md#material-maps
type MaterialExp struct {
	Prop    string
	Name    Ident
	Link    *Int
	Face    Int
	FaceAll bool
	State   State
	ID      string
	IDAt    Span
	Value   Number
	Any     bool
	Use     *Capture
}

// IsMap is whether the expectation reads a texture id and not a level.
func (x *MaterialExp) IsMap() bool { return x.Prop == "normalmap" || x.Prop == "specularmap" }

// GiveExp is an inventory offer of Item from Name. Folder is the form
// `give folder`: Item then names a folder, and Holding the items it must
// hold (none when the form has no holding).
type GiveExp struct {
	Folder  bool
	Item    Text
	From    Ident
	Holding []Text
	To      *Ident // to NAME: the offer came to a second avatar; nil is the tester
}

// RezExp is a new root. As is set on a positive rez and absent on a negative one.
type RezExp struct {
	Name    Text
	HasDesc bool
	Desc    Text
	From    Ident
	As      Ident
}

// LinkExp is a probe report. A nil Key is the null key. A nil HeardBy
// accepts the first prim in the linkset that reports the message.
type LinkExp struct {
	Name    Ident
	From    Int
	Num     Int
	Text    Text
	Key     *Key
	HeardBy *Int
}

// ButtonValKind is the value word of a button reading.
type ButtonValKind int

const (
	ButtonShown ButtonValKind = iota // one tuple or more
	ButtonGone                       // no tuple
	ButtonCount                      // exactly Count tuples
)

// ButtonExp is expect button OBJ link? parts face? and a state. The
// search is a touch's: Button.Nth is for Check to refuse. With State
// changes, or State.Original, there is no value; otherwise Val says which,
// and Count is the N of count N.
type ButtonExp struct {
	Name    Ident
	Link    *Int
	Button  *Button
	State   State
	Val     ButtonValKind
	ValSpan Span
	Count   Int
}

// AttachExp is attached OBJ on "point" or attached OBJ off.
type AttachExp struct {
	Name      Ident
	Off       bool
	Point     string
	PointSpan Span
}
