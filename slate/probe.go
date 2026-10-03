package slate

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// The probe's wire codec: what the runner says to the bridge, what it
// classifies when a line arrives, the nonce and channels of a run, and
// the two LSL scripts it installs.
// Why: doc/slate-runner.md#probe-and-bridge

// wireVersion starts every protocol line, then the run's nonce.
const wireVersion = "slprobe/2"

// probeScript and bridgeScript are the names the scripts are installed
// under; installing under the same name replaces a script a killed run
// left behind.
const (
	probeScript  = "slate probe"
	bridgeScript = "slate bridge"
)

// Channel ranges of a run: CONTROL is positive so that a line may be
// 1023 bytes, COMMAND is negative so the chat bar cannot type it.
const (
	controlMin = 0x40000000
	controlMax = 0x7FFFFFFE
)

// quote wraps s in double quotes, escaping backslash, quote, newline and
// tab, and nothing else.
func quote(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteByte(s[i])
		}
	}
	b.WriteByte('"')
	return b.String()
}

// unquote reverses quote. It rejects a missing quote and a truncated or
// unknown escape. Like the probe's, it does not reject a bare quote
// inside.
func unquote(s string) (string, error) {
	n := len(s)
	if n < 2 || s[0] != '"' || s[n-1] != '"' {
		return "", fmt.Errorf("%q is not a quoted string", s)
	}
	var b strings.Builder
	last := n - 2
	for i := 1; i <= last; i++ {
		c := s[i]
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		i++
		if i > last {
			return "", fmt.Errorf("%q ends inside an escape", s)
		}
		switch s[i] {
		case '\\':
			b.WriteByte('\\')
		case '"':
			b.WriteByte('"')
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		default:
			return "", fmt.Errorf("%q has the unknown escape \\%c", s, s[i])
		}
	}
	return b.String(), nil
}

// draws is the nonce and channels of one run. Every channel it hands out
// differs from every other, from the reserved ones given to newDraws
// (the file's listen channels), from 0 and from 2147483647.
type draws struct {
	r    io.Reader
	used map[int32]bool
}

// newDraws starts a run's draws. A nil r reads crypto/rand; a test gives
// its own reader.
func newDraws(r io.Reader, listens []int32) *draws {
	if r == nil {
		r = rand.Reader
	}
	d := &draws{r: r, used: map[int32]bool{0: true, math.MaxInt32: true}}
	for _, ch := range listens {
		d.used[ch] = true
	}
	return d
}

func (d *draws) uint32() (uint32, error) {
	var b [4]byte
	if _, err := io.ReadFull(d.r, b[:]); err != nil {
		return 0, fmt.Errorf("drawing a channel: %w", err)
	}
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3]), nil
}

// Nonce is 16 lowercase hex characters.
func (d *draws) Nonce() (string, error) {
	var b [8]byte
	if _, err := io.ReadFull(d.r, b[:]); err != nil {
		return "", fmt.Errorf("drawing the nonce: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// Control is the channel the tester speaks to the bridge on, 0x40000000
// to 0x7FFFFFFE.
func (d *draws) Control() (int32, error) {
	return d.channel(func(v uint32) int32 {
		return int32(controlMin + v%(controlMax-controlMin+1))
	})
}

// Command is a probed object's channel, negative.
func (d *draws) Command() (int32, error) {
	return d.channel(func(v uint32) int32 { return -1 - int32(v%(1<<31)) })
}

func (d *draws) channel(from func(uint32) int32) (int32, error) {
	for {
		v, err := d.uint32()
		if err != nil {
			return 0, err
		}
		if ch := from(v); !d.used[ch] {
			d.used[ch] = true
			return ch, nil
		}
	}
}

// relayCommand is the CONTROL line that has the bridge say inner to one
// prim on its COMMAND channel, whatever its length.
func relayCommand(nonce string, prim msg.UUID, command int32, inner string) string {
	return fmt.Sprintf("%s %s relay %s %d %s", wireVersion, nonce, prim, command, inner)
}

func sendInner(nonce string, sender, target, num int32, key msg.UUID, text string) string {
	return fmt.Sprintf("%s %s send %d %d %d %s %s", wireVersion, nonce, sender, target, num, key, quote(text))
}

func checkLine(line string) (string, error) {
	if len(line) > maxSay {
		return "", fmt.Errorf("the line to the bridge is %d bytes and carries at most %d", len(line), maxSay)
	}
	return line, nil
}

// sendLine is the line the tester says on CONTROL so that the probe in
// prim, whose link is sender, calls llMessageLinked(target, num, text,
// key). It is an error when the line is longer than 1023 bytes. The
// text is link text: printable ASCII, tab and newline.
func sendLine(nonce string, prim msg.UUID, command, sender, target, num int32, key msg.UUID, text string) (string, error) {
	return checkLine(relayCommand(nonce, prim, command, sendInner(nonce, sender, target, num, key, text)))
}

// ackLine is the CONTROL line that tells the probe in prim, whose link
// is link, to stop saying hello.
func ackLine(nonce string, prim msg.UUID, command, link int32) (string, error) {
	return checkLine(relayCommand(nonce, prim, command, fmt.Sprintf("%s %s ack %d", wireVersion, nonce, link)))
}

// wireKind says which protocol message a line is.
type wireKind int

const (
	wireNone        wireKind = iota
	wireHello                // probe: hello <link> <primKey>
	wireLink                 // probe: link <heardBy> <sender> <num> <key> "<text>"
	wireOverflow             // probe: overflow <heardBy> <sender> <num> <key> <byteLength>
	wireBad                  // probe: bad
	wireReady                // bridge: ready
	wireFwd                  // bridge: fwd <channel> <speakerKey> "<speakerName>" <tail>
	wireFwdOverflow          // bridge: fwd-overflow <channel> <speakerKey> <byteLength>
)

// wireMessage is a classified protocol line. Which fields are set depends on
// Kind. Tail is the product's chat exactly as the bridge heard it, not
// unescaped; Text is a link's text, unquoted.
type wireMessage struct {
	Kind    wireKind
	Link    int32    // Hello
	Prim    msg.UUID // wireHello
	HeardBy int32    // wireLink, wireOverflow
	Sender  int32    // wireLink, wireOverflow
	Num     int32    // wireLink, wireOverflow
	Key     msg.UUID // wireLink, wireOverflow
	Text    string   // wireLink
	Length  int      // wireOverflow, wireFwdOverflow
	Channel int32    // wireFwd, wireFwdOverflow
	Speaker msg.UUID // wireFwd, wireFwdOverflow
	Name    string   // wireFwd
	Tail    string   // wireFwd
}

// runState is what the classifier needs of a run: the nonce, the prims a
// probe was installed in, and the worn bridge.
type runState struct {
	Nonce  string
	Probes map[msg.UUID]bool
	Bridge msg.UUID
}

// Classify says whether a chat line is protocol. A line is protocol
// when it is ChatDirect from an installed probe's prim, or ChatOwner from
// the worn bridge, and starts with "slprobe/2 <nonce> ". One that is
// protocol by those rules and does not parse is an error, so it is
// never taken for product chat. Anything else returns wireNone.
func (r *runState) Classify(chatType uint8, source msg.UUID, text string) (wireMessage, error) {
	head := wireVersion + " " + r.Nonce + " "
	if !strings.HasPrefix(text, head) {
		return wireMessage{}, nil
	}
	rest := text[len(head):]
	switch {
	case chatType == sl.ChatDirect && r.Probes[source]:
		return parseProbe(rest)
	case chatType == sl.ChatOwner && !r.Bridge.IsZero() && source == r.Bridge:
		return parseBridge(rest)
	}
	return wireMessage{}, nil
}

func badLine(what, rest string) error {
	return fmt.Errorf("protocol line is not a %s: %q", what, rest)
}

func parseProbe(rest string) (wireMessage, error) {
	verb, args, _ := strings.Cut(rest, " ")
	has := strings.Contains(rest, " ")
	switch verb {
	case "bad":
		if !has {
			return wireMessage{Kind: wireBad}, nil
		}
	case "hello":
		f := strings.Split(args, " ")
		if has && len(f) == 2 {
			link, e1 := canonInt(f[0])
			prim, e2 := canonKey(f[1])
			if e1 == nil && e2 == nil {
				return wireMessage{Kind: wireHello, Link: link, Prim: prim}, nil
			}
		}
	case "link":
		f := strings.SplitN(args, " ", 5)
		if has && len(f) == 5 {
			var m wireMessage
			if err := fields(f[:4], &m.HeardBy, &m.Sender, &m.Num, &m.Key); err == nil {
				if text, err := unquote(f[4]); err == nil {
					m.Kind, m.Text = wireLink, text
					return m, nil
				}
			}
		}
	case "overflow":
		f := strings.Split(args, " ")
		if has && len(f) == 5 {
			var m wireMessage
			if err := fields(f[:4], &m.HeardBy, &m.Sender, &m.Num, &m.Key); err == nil {
				if n, err := canonInt(f[4]); err == nil && n >= 0 {
					m.Kind, m.Length = wireOverflow, int(n)
					return m, nil
				}
			}
		}
	}
	return wireMessage{}, badLine("probe message", rest)
}

func parseBridge(rest string) (wireMessage, error) {
	verb, args, has := strings.Cut(rest, " ")
	switch verb {
	case "ready":
		if !has {
			return wireMessage{Kind: wireReady}, nil
		}
	case "fwd":
		if m, ok := parseFwd(args); has && ok {
			return m, nil
		}
	case "fwd-overflow":
		f := strings.Split(args, " ")
		if has && len(f) == 3 {
			var m wireMessage
			ch, e1 := canonInt(f[0])
			key, e2 := canonKey(f[1])
			n, e3 := canonInt(f[2])
			if e1 == nil && e2 == nil && e3 == nil && n >= 0 {
				m.Kind, m.Channel, m.Speaker, m.Length = wireFwdOverflow, ch, key, int(n)
				return m, nil
			}
		}
	}
	return wireMessage{}, badLine("bridge message", rest)
}

// parseFwd reads `<channel> <speakerKey> "<speakerName>" <tail>`. The
// name is quoted with quote, so its closing quote is the first one not
// escaped; everything after the space that follows is the tail, kept raw.
func parseFwd(args string) (wireMessage, bool) {
	f := strings.SplitN(args, " ", 3)
	if len(f) != 3 {
		return wireMessage{}, false
	}
	ch, e1 := canonInt(f[0])
	key, e2 := canonKey(f[1])
	if e1 != nil || e2 != nil || !strings.HasPrefix(f[2], `"`) {
		return wireMessage{}, false
	}
	s := f[2]
	end := -1
	for i := 1; i < len(s) && end < 0; i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			end = i
		}
	}
	if end < 0 {
		return wireMessage{}, false
	}
	name, err := unquote(s[:end+1])
	if err != nil {
		return wireMessage{}, false
	}
	tail := s[end+1:]
	if tail != "" {
		if tail[0] != ' ' {
			return wireMessage{}, false
		}
		tail = tail[1:]
	}
	return wireMessage{Kind: wireFwd, Channel: ch, Speaker: key, Name: name, Tail: tail}, true
}

// fields reads three integers and a key into the places given.
func fields(f []string, a, b, c *int32, k *msg.UUID) error {
	var err error
	for i, p := range []*int32{a, b, c} {
		if *p, err = canonInt(f[i]); err != nil {
			return err
		}
	}
	*k, err = canonKey(f[3])
	return err
}

// canonInt reads an LSL integer in canonical decimal: no plus, no
// leading zeros, no negative zero.
func canonInt(s string) (int32, error) {
	v, err := strconv.ParseInt(s, 10, 32)
	if err != nil || strconv.FormatInt(v, 10) != s {
		return 0, fmt.Errorf("%q is not a canonical integer", s)
	}
	return int32(v), nil
}

// canonKey reads a lowercase 8-4-4-4-12 key, which may be the null key.
func canonKey(s string) (msg.UUID, error) {
	u, err := msg.ParseUUID(s)
	if err != nil || u.String() != s {
		return msg.UUID{}, fmt.Errorf("%q is not a canonical key", s)
	}
	return u, nil
}

// substitute replaces the initializer of one declaration, which must
// appear exactly once, and leaves its comment.
func substitute(src, name, literal string) (string, error) {
	re := regexp.MustCompile(`(?m)^((?:string|key|integer|list)\s+` + name + `\s*=\s*)[^;\n]*;`)
	if n := len(re.FindAllStringIndex(src, -1)); n != 1 {
		return "", fmt.Errorf("the LSL source declares %s %d times, want once", name, n)
	}
	at := re.FindStringSubmatchIndex(src)
	return src[:at[3]] + literal + ";" + src[at[1]:], nil
}

// lslString is s as an LSL string literal. LSL turns "\t" into four
// spaces, so a tab is refused rather than written.
func lslString(s string) (string, error) {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\\' || c == '"':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c == '\n':
			b.WriteString(`\n`)
		case c < 0x20 || c > 0x7e:
			return "", fmt.Errorf("%q cannot go in an LSL string literal", s)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String(), nil
}

var nonceRE = regexp.MustCompile(`^[0-9a-f]{16}$`)

func substituteAll(src string, vals [][2]string) (string, error) {
	var err error
	for _, v := range vals {
		if src, err = substitute(src, v[0], v[1]); err != nil {
			return "", err
		}
	}
	return src, nil
}

func nonceLiteral(nonce string) (string, error) {
	if !nonceRE.MatchString(nonce) {
		return "", fmt.Errorf("nonce %q is not 16 lowercase hex characters", nonce)
	}
	return lslString(nonce)
}

// probeSource is the probe for one object's prims, with NONCE, TESTER,
// BRIDGE and COMMAND substituted.
func probeSource(nonce string, tester, bridge msg.UUID, command int32) (string, error) {
	n, err := nonceLiteral(nonce)
	if err != nil {
		return "", err
	}
	t, _ := lslString(tester.String())
	b, _ := lslString(bridge.String())
	return substituteAll(probeLSL, [][2]string{
		{"NONCE", n}, {"TESTER", t}, {"BRIDGE", b},
		{"COMMAND", strconv.FormatInt(int64(command), 10)},
	})
}

// bridgeSource is the bridge, with NONCE, CONTROL and LISTENS
// substituted. It has no TESTER: the wearer owns it.
func bridgeSource(nonce string, control int32, listens []int32) (string, error) {
	n, err := nonceLiteral(nonce)
	if err != nil {
		return "", err
	}
	parts := make([]string, len(listens))
	for i, ch := range listens {
		parts[i] = strconv.FormatInt(int64(ch), 10)
	}
	return substituteAll(bridgeLSL, [][2]string{
		{"NONCE", n}, {"CONTROL", strconv.FormatInt(int64(control), 10)},
		{"LISTENS", "[" + strings.Join(parts, ", ") + "]"},
	})
}

// probeLSL and bridgeLSL are the listings of doc/slate-runner.md, byte
// for byte; a test fails when they differ.
// Why: doc/slate-runner.md#the-probe
const probeLSL = `// slate probe 2.
integer COMMAND = -7702101; // substituted
key     TESTER  = "00000000-0000-0000-0000-000000000000"; // substituted
key     BRIDGE  = "00000000-0000-0000-0000-000000000000"; // substituted
string  NONCE   = "0000000000000000"; // substituted, 16 hex chars
string  TAB;     // a real tab: "\t" in an LSL literal is four spaces
integer ACKED;
integer UNQ;
integer TAKE_OK;
string  REST;

string quote(string s) {
    string o = "\"";
    integer i = 0;
    integer n = llStringLength(s);
    for (; i < n; ++i) {
        string c = llGetSubString(s, i, i);
        if (c == "\\")      o += "\\\\";
        else if (c == "\"") o += "\\\"";
        else if (c == "\n") o += "\\n";
        else if (c == TAB)  o += "\\t";
        else                o += c;
    }
    return o + "\"";
}

string unquote(string s) {
    UNQ = 0;
    integer n = llStringLength(s);
    if (n < 2) return "";
    if (llGetSubString(s, 0, 0) != "\"" || llGetSubString(s, n - 1, n - 1) != "\"") return "";
    string o = "";
    integer i = 1;
    integer last = n - 2;
    for (; i <= last; ++i) {
        string c = llGetSubString(s, i, i);
        if (c != "\\") {
            o += c;
        } else {
            ++i;
            if (i > last) return "";
            c = llGetSubString(s, i, i);
            if (c == "\\") o += "\\";
            else if (c == "\"") o += "\"";
            else if (c == "n") o += "\n";
            else if (c == "t") o += TAB;
            else return "";
        }
    }
    UNQ = 1;
    return o;
}

integer ascii(string s) {
    integer i = 0;
    integer n = llStringLength(s);
    for (; i < n; ++i) {
        integer k = llOrd(s, i);
        if (k != 9 && k != 10 && (k < 32 || k > 126)) return 0;
    }
    return 1;
}

string take() {
    TAKE_OK = 0;
    integer sp = llSubStringIndex(REST, " ");
    if (sp < 1) return "";
    string field = llGetSubString(REST, 0, sp - 1);
    REST = llGetSubString(REST, sp + 1, -1);
    TAKE_OK = 1;
    return field;
}

integer sayReport(string line) {
    llRegionSayTo(TESTER, 0, line);
    return 0;
}

integer hello() {
    return sayReport("slprobe/2 " + NONCE + " hello "
        + (string)llGetLinkNumber() + " " + (string)llGetKey());
}

integer sayBad() {
    return sayReport("slprobe/2 " + NONCE + " bad");
}

default {
    state_entry() {
        TAB = llChar(9);
        ACKED = 0;
        llListen(COMMAND, "", BRIDGE, "");
        hello();
        llSetTimerEvent(1.0);
    }
    timer() {
        if (!ACKED) hello();
    }
    on_rez(integer n) { llResetScript(); }
    changed(integer c) {
        if (c & (CHANGED_LINK | CHANGED_OWNER | CHANGED_REGION)) llResetScript();
    }
    listen(integer ch, string name, key id, string msg) {
        string head = "slprobe/2 " + NONCE + " ";
        if (llSubStringIndex(msg, head) != 0) return;
        REST = llGetSubString(msg, llStringLength(head), -1);
        if (llSubStringIndex(REST, "ack ") == 0) {
            string num = llGetSubString(REST, 4, -1);
            if (num == (string)((integer)num) && (integer)num == llGetLinkNumber()) {
                ACKED = 1;
                llSetTimerEvent(0.0);
            }
            return;
        }
        if (llSubStringIndex(REST, "send ") != 0) return;
        REST = llGetSubString(REST, 5, -1);
        string senderF = take();
        if (!TAKE_OK || senderF != (string)((integer)senderF)) return;
        if ((integer)senderF != llGetLinkNumber()) return;
        string targetF = take();
        string numF = take();
        string keyF = take();
        if (!TAKE_OK || targetF != (string)((integer)targetF) || numF != (string)((integer)numF)) {
            sayBad();
            return;
        }
        if (llStringLength(keyF) != 36 || (string)((key)keyF) != keyF) {
            sayBad();
            return;
        }
        string text = unquote(REST);
        if (!UNQ) {
            sayBad();
            return;
        }
        llMessageLinked((integer)targetF, (integer)numF, text, (key)keyF);
    }
    link_message(integer sender, integer num, string str, key id) {
        if (!ascii(str)) {
            sayReport("slprobe/2 " + NONCE + " overflow "
                + (string)llGetLinkNumber() + " " + (string)sender + " "
                + (string)num + " " + (string)id + " " + (string)llStringLength(str));
            return;
        }
        string line = "slprobe/2 " + NONCE + " link "
            + (string)llGetLinkNumber() + " " + (string)sender + " "
            + (string)num + " " + (string)id + " " + quote(str);
        if (llStringLength(line) > 1023) {
            sayReport("slprobe/2 " + NONCE + " overflow "
                + (string)llGetLinkNumber() + " " + (string)sender + " "
                + (string)num + " " + (string)id + " " + (string)llStringLength(str));
            return;
        }
        sayReport(line);
    }
}
`

// Why: doc/slate-runner.md#the-bridge-script
const bridgeLSL = `// slate bridge 2.
string  NONCE   = "0000000000000000"; // substituted
integer CONTROL = 1073741824; // substituted, 0x40000000 to 0x7FFFFFFE
list    LISTENS = []; // substituted as a list literal of the file's listen channels
string  TAB;     // a real tab: "\t" in an LSL literal is four spaces
integer TAKE_OK;
string  REST;

string quote(string s) {
    string o = "\"";
    integer i = 0;
    integer n = llStringLength(s);
    for (; i < n; ++i) {
        string c = llGetSubString(s, i, i);
        if (c == "\\")      o += "\\\\";
        else if (c == "\"") o += "\\\"";
        else if (c == "\n") o += "\\n";
        else if (c == TAB)  o += "\\t";
        else                o += c;
    }
    return o + "\"";
}

string take() {
    TAKE_OK = 0;
    integer sp = llSubStringIndex(REST, " ");
    if (sp < 1) return "";
    string field = llGetSubString(REST, 0, sp - 1);
    REST = llGetSubString(REST, sp + 1, -1);
    TAKE_OK = 1;
    return field;
}

default {
    state_entry() {
        TAB = llChar(9);
        llListen(CONTROL, "", llGetOwner(), "");
        integer i = 0;
        integer n = llGetListLength(LISTENS);
        for (; i < n; ++i)
            llListen(llList2Integer(LISTENS, i), "", NULL_KEY, "");
        llOwnerSay("slprobe/2 " + NONCE + " ready");
    }
    listen(integer ch, string name, key id, string msg) {
        if (ch == CONTROL) {
            string head = "slprobe/2 " + NONCE + " relay ";
            if (llSubStringIndex(msg, head) != 0) return;
            REST = llGetSubString(msg, llStringLength(head), -1);
            string target = take();
            if (!TAKE_OK || llStringLength(target) != 36 || (string)((key)target) != target) return;
            string channel = take();
            if (!TAKE_OK || channel != (string)((integer)channel)) return;
            llRegionSayTo((key)target, (integer)channel, REST);
            return;
        }
        string line = "slprobe/2 " + NONCE + " fwd " + (string)ch + " " + (string)id
            + " " + quote(name) + " " + msg;
        if (llStringLength(line) > 1023) {
            llOwnerSay("slprobe/2 " + NONCE + " fwd-overflow " + (string)ch
                + " " + (string)id + " " + (string)llStringLength(msg));
            return;
        }
        llOwnerSay(line);
    }
}
`
