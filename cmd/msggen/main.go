// Command msggen turns Linden Lab's message_template.msg into Go
// structures with Encode and Decode methods.
//
//	msggen -out msg/messages_gen.go            # from the shipping viewer
//	msggen -template ../viewer/scripts/messages/message_template.msg
//	msggen -template https://.../some/other/message_template.msg
//
// The template is Linden Lab's, not ours, and is not kept in this
// repository -- it is fetched, and -template also takes a path for
// building offline or against a checkout.  Whichever was used is
// recorded in the generated file's header, so the output says where it
// came from.
//
// # Where to get it
//
// There are three, and they do not agree.  The default is the first:
//
//	The shipping viewer -- what the grid demonstrably speaks.
//	https://raw.githubusercontent.com/secondlife/viewer/main/scripts/messages/message_template.msg
//
//	The master template -- what Linden Lab's servers accept, which is
//	a superset and moves ahead of the client.
//	https://raw.githubusercontent.com/secondlife/master-message-template/master/message_template.msg
//
//	Firestorm's -- the viewer's, plus additions for OpenSim.
//	https://raw.githubusercontent.com/FirestormViewer/phoenix-firestorm/master/scripts/messages/message_template.msg
//
// # Why the viewer's is the default
//
// It is the only one of the three that describes what the grid actually
// sends.  The other two each carry a block that Second Life never puts
// on the wire: Firestorm adds Size to MapBlockReply, for OpenSim's
// variable-sized regions, and the master template adds NewScriptInfo to
// RezScript.  A message can be wrong in a way that never fails -- a
// trailing Variable block the sender omitted decodes as empty and
// reports no error -- so this was measured rather than assumed.
// Why: doc/wire.md#which-message-template
//
// Take the master template when you want to reach something newer than
// the client, Firestorm's when you want to talk to OpenSim, and expect
// to test what you get either way.
//
// # What this program is not
//
// The wire behaviour lives in package msg, not here: the generated
// structs carry the template's own type names in `ll` struct tags and
// the generic codec interprets them.  So adding a field type is a
// change to msg/codec.go plus one line in the table below, and a new
// message or a changed block needs no code change at all.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"go/format"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/internal/version"
)

// ---------------------------------------------------------------- model

type Field struct {
	Name string
	Type string // template type name, used verbatim in the tag
	Size int    // Variable: prefix width.  Fixed: byte count.
	Line int
}

type Block struct {
	Name  string
	Quant string // Single, Multiple, Variable
	Count int    // Multiple only
	Field []Field
	Line  int
}

type Message struct {
	Name        string
	Freq        string // High, Medium, Low, Fixed
	Number      uint32
	Trusted     bool
	Zerocoded   bool
	Deprecation string
	Block       []Block
	Line        int
}

// ---------------------------------------------------------------- lexer

type token struct {
	text string
	line int
}

var tokenRE = regexp.MustCompile(`[{}]|[^\s{}]+`)

func lex(src []byte) []token {
	var out []token
	for n, line := range strings.Split(string(src), "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		for _, m := range tokenRE.FindAllString(line, -1) {
			out = append(out, token{m, n + 1})
		}
	}
	return out
}

// --------------------------------------------------------------- parser

type parser struct {
	tok []token
	i   int
}

func (p *parser) errf(t token, format string, a ...any) error {
	return fmt.Errorf("line %d: %s", t.line, fmt.Sprintf(format, a...))
}

func (p *parser) peek() (token, bool) {
	if p.i >= len(p.tok) {
		return token{}, false
	}
	return p.tok[p.i], true
}

func (p *parser) next() (token, bool) {
	t, ok := p.peek()
	if ok {
		p.i++
	}
	return t, ok
}

func (p *parser) want(text string) (token, error) {
	t, ok := p.next()
	if !ok {
		return t, fmt.Errorf("unexpected end of template, wanted %q", text)
	}
	if t.text != text {
		return t, p.errf(t, "wanted %q, got %q", text, t.text)
	}
	return t, nil
}

func (p *parser) word() (token, error) {
	t, ok := p.next()
	if !ok {
		return t, fmt.Errorf("unexpected end of template")
	}
	if t.text == "{" || t.text == "}" {
		return t, p.errf(t, "wanted a word, got %q", t.text)
	}
	return t, nil
}

var identRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func Parse(src []byte) ([]Message, error) {
	p := &parser{tok: lex(src)}

	// An optional "version N.N" preamble.
	if t, ok := p.peek(); ok && t.text == "version" {
		p.i++
		if _, err := p.word(); err != nil {
			return nil, err
		}
	}

	var msgs []Message
	seen := map[string]int{}
	for {
		t, ok := p.peek()
		if !ok {
			break
		}
		if t.text != "{" {
			return nil, p.errf(t, "wanted a message, got %q", t.text)
		}
		m, err := p.message()
		if err != nil {
			return nil, err
		}
		if prev, dup := seen[m.Name]; dup {
			return nil, fmt.Errorf("line %d: message %s already defined on line %d", m.Line, m.Name, prev)
		}
		seen[m.Name] = m.Line
		msgs = append(msgs, *m)
	}
	return msgs, nil
}

func (p *parser) message() (*Message, error) {
	open, err := p.want("{")
	if err != nil {
		return nil, err
	}
	m := &Message{Line: open.line}

	name, err := p.word()
	if err != nil {
		return nil, err
	}
	if !identRE.MatchString(name.text) {
		return nil, p.errf(name, "%q is not a usable message name", name.text)
	}
	m.Name = name.text

	freq, err := p.word()
	if err != nil {
		return nil, err
	}
	switch freq.text {
	case "High", "Medium", "Low", "Fixed":
		m.Freq = freq.text
	default:
		return nil, p.errf(freq, "unknown priority %q", freq.text)
	}

	num, err := p.word()
	if err != nil {
		return nil, err
	}
	n, err := strconv.ParseUint(num.text, 0, 32)
	if err != nil {
		return nil, p.errf(num, "bad message number %q", num.text)
	}
	m.Number = uint32(n)

	switch m.Freq {
	case "High", "Medium":
		if n == 0 || n > 0xfe {
			return nil, p.errf(num, "%s number %d is out of range 1..254", m.Freq, n)
		}
	case "Low":
		if n == 0 || n > 0xfff0 {
			return nil, p.errf(num, "Low number %d is out of range", n)
		}
	case "Fixed":
		if n < 0xffffff00 {
			return nil, p.errf(num, "Fixed number %#x must be at least 0xffffff00", n)
		}
	}

	trust, err := p.word()
	if err != nil {
		return nil, err
	}
	switch trust.text {
	case "Trusted":
		m.Trusted = true
	case "NotTrusted":
	default:
		return nil, p.errf(trust, "unknown trust %q", trust.text)
	}

	enc, err := p.word()
	if err != nil {
		return nil, err
	}
	switch enc.text {
	case "Zerocoded":
		m.Zerocoded = true
	case "Unencoded":
	default:
		return nil, p.errf(enc, "unknown encoding %q", enc.text)
	}

	// Zero or more deprecation flags before the first block.
	for {
		t, ok := p.peek()
		if !ok {
			return nil, fmt.Errorf("line %d: message %s is not closed", m.Line, m.Name)
		}
		if t.text == "{" || t.text == "}" {
			break
		}
		switch t.text {
		case "UDPDeprecated", "UDPBlackListed", "Deprecated":
			m.Deprecation = t.text
		default:
			return nil, p.errf(t, "unknown message flag %q", t.text)
		}
		p.i++
	}

	seen := map[string]bool{}
	for {
		t, ok := p.peek()
		if !ok {
			return nil, fmt.Errorf("line %d: message %s is not closed", m.Line, m.Name)
		}
		if t.text != "{" {
			break
		}
		b, err := p.block()
		if err != nil {
			return nil, err
		}
		if seen[b.Name] {
			return nil, fmt.Errorf("line %d: %s has two blocks named %s", b.Line, m.Name, b.Name)
		}
		seen[b.Name] = true
		m.Block = append(m.Block, *b)
	}
	if _, err := p.want("}"); err != nil {
		return nil, err
	}
	return m, nil
}

func (p *parser) block() (*Block, error) {
	open, err := p.want("{")
	if err != nil {
		return nil, err
	}
	b := &Block{Line: open.line}

	name, err := p.word()
	if err != nil {
		return nil, err
	}
	if !identRE.MatchString(name.text) {
		return nil, p.errf(name, "%q is not a usable block name", name.text)
	}
	b.Name = name.text

	q, err := p.word()
	if err != nil {
		return nil, err
	}
	switch q.text {
	case "Single", "Variable":
		b.Quant = q.text
	case "Multiple":
		b.Quant = q.text
		cnt, err := p.word()
		if err != nil {
			return nil, err
		}
		n, err := strconv.Atoi(cnt.text)
		if err != nil || n <= 0 {
			return nil, p.errf(cnt, "bad Multiple count %q", cnt.text)
		}
		b.Count = n
	default:
		return nil, p.errf(q, "unknown block quantifier %q", q.text)
	}

	seen := map[string]bool{}
	for {
		t, ok := p.peek()
		if !ok {
			return nil, fmt.Errorf("line %d: block %s is not closed", b.Line, b.Name)
		}
		if t.text != "{" {
			break
		}
		f, err := p.field()
		if err != nil {
			return nil, err
		}
		if seen[f.Name] {
			return nil, fmt.Errorf("line %d: block %s has two fields named %s", f.Line, b.Name, f.Name)
		}
		seen[f.Name] = true
		b.Field = append(b.Field, *f)
	}
	if _, err := p.want("}"); err != nil {
		return nil, err
	}
	return b, nil
}

func (p *parser) field() (*Field, error) {
	open, err := p.want("{")
	if err != nil {
		return nil, err
	}
	f := &Field{Line: open.line}

	name, err := p.word()
	if err != nil {
		return nil, err
	}
	if !identRE.MatchString(name.text) {
		return nil, p.errf(name, "%q is not a usable field name", name.text)
	}
	f.Name = name.text

	typ, err := p.word()
	if err != nil {
		return nil, err
	}
	if _, ok := goType[typ.text]; !ok && typ.text != "Variable" && typ.text != "Fixed" {
		return nil, p.errf(typ, "unknown field type %q", typ.text)
	}
	f.Type = typ.text

	if typ.text == "Variable" || typ.text == "Fixed" {
		sz, err := p.word()
		if err != nil {
			return nil, err
		}
		n, err := strconv.Atoi(sz.text)
		if err != nil || n <= 0 {
			return nil, p.errf(sz, "bad %s size %q", typ.text, sz.text)
		}
		if typ.text == "Variable" && n != 1 && n != 2 && n != 4 {
			return nil, p.errf(sz, "Variable prefix must be 1, 2 or 4, got %d", n)
		}
		f.Size = n
	}

	if _, err := p.want("}"); err != nil {
		return nil, err
	}
	return f, nil
}

// -------------------------------------------------------------- emitter

// goType maps a template type to the Go type that represents it.  The
// wire behaviour for each is in msg/codec.go, keyed on the same names.
var goType = map[string]string{
	"U8":           "uint8",
	"U16":          "uint16",
	"U32":          "uint32",
	"U64":          "uint64",
	"S8":           "int8",
	"S16":          "int16",
	"S32":          "int32",
	"S64":          "int64",
	"F32":          "float32",
	"F64":          "float64",
	"BOOL":         "bool",
	"LLUUID":       "UUID",
	"LLVector3":    "Vector3",
	"LLVector3d":   "Vector3d",
	"LLVector4":    "Vector4",
	"LLQuaternion": "Quaternion",
	"IPADDR":       "IPAddr",
	"IPPORT":       "IPPort",
}

func fieldGoType(f Field) string {
	switch f.Type {
	case "Variable":
		return "[]byte"
	case "Fixed":
		return fmt.Sprintf("[%d]byte", f.Size)
	}
	return goType[f.Type]
}

func fieldTag(f Field) string {
	if f.Type == "Variable" || f.Type == "Fixed" {
		return fmt.Sprintf("`ll:\"%s,%d\"`", f.Type, f.Size)
	}
	return fmt.Sprintf("`ll:\"%s\"`", f.Type)
}

func blockGoType(m Message, b Block) string {
	return m.Name + "_" + b.Name
}

func Generate(pkg, src string, msgs []Message) ([]byte, error) {
	var w bytes.Buffer

	fmt.Fprintf(&w, "// Code generated by cmd/msggen from %s. DO NOT EDIT.\n\n", src)
	fmt.Fprintf(&w, "package %s\n\n", pkg)

	for _, m := range msgs {
		// Block types first, so the message struct can name them.
		for _, b := range m.Block {
			fmt.Fprintf(&w, "// %s is the %s block of %s.\n", blockGoType(m, b), b.Name, m.Name)
			fmt.Fprintf(&w, "type %s struct {\n", blockGoType(m, b))
			for _, f := range b.Field {
				fmt.Fprintf(&w, "\t%s %s %s\n", f.Name, fieldGoType(f), fieldTag(f))
			}
			fmt.Fprintf(&w, "}\n\n")
		}

		fmt.Fprintf(&w, "// %s is %s %s", m.Name, m.Freq, formatNumber(m))
		if m.Trusted {
			w.WriteString(", trusted")
		}
		if m.Zerocoded {
			w.WriteString(", zerocoded")
		}
		if m.Deprecation != "" {
			fmt.Fprintf(&w, ", %s", m.Deprecation)
		}
		w.WriteString(".\n")
		fmt.Fprintf(&w, "type %s struct {\n", m.Name)
		for _, b := range m.Block {
			typ := blockGoType(m, b)
			switch b.Quant {
			case "Single":
				fmt.Fprintf(&w, "\t%s %s `ll:\"Single\"`\n", b.Name, typ)
			case "Multiple":
				fmt.Fprintf(&w, "\t%s [%d]%s `ll:\"Multiple,%d\"`\n", b.Name, b.Count, typ, b.Count)
			case "Variable":
				fmt.Fprintf(&w, "\t%s []%s `ll:\"Variable\"`\n", b.Name, typ)
			}
		}
		fmt.Fprintf(&w, "}\n\n")

		fmt.Fprintf(&w, "var info%s = Info{\n", m.Name)
		fmt.Fprintf(&w, "\tName: %q,\n", m.Name)
		fmt.Fprintf(&w, "\tID: MakeID(Freq%s, %s),\n", m.Freq, formatNumber(m))
		if m.Trusted {
			fmt.Fprintf(&w, "\tTrusted: true,\n")
		}
		if m.Zerocoded {
			fmt.Fprintf(&w, "\tZerocoded: true,\n")
		}
		if m.Deprecation != "" {
			fmt.Fprintf(&w, "\tDeprecation: %q,\n", m.Deprecation)
		}
		fmt.Fprintf(&w, "}\n\n")

		fmt.Fprintf(&w, "func (m *%s) MsgInfo() *Info { return &info%s }\n", m.Name, m.Name)
		fmt.Fprintf(&w, "func (m *%s) Encode() ([]byte, error) { return Marshal(m) }\n", m.Name)
		fmt.Fprintf(&w, "func (m *%s) Decode(b []byte) error { return Unmarshal(b, m) }\n\n", m.Name)
	}

	w.WriteString("func init() {\n")
	for _, m := range msgs {
		fmt.Fprintf(&w, "\tregister(&info%s, func() Message { return new(%s) })\n", m.Name, m.Name)
	}
	w.WriteString("}\n")

	out, err := format.Source(w.Bytes())
	if err != nil {
		// Emit the unformatted source so the error can be read
		// against real line numbers.
		return w.Bytes(), fmt.Errorf("generated source does not parse: %w", err)
	}
	return out, nil
}

func formatNumber(m Message) string {
	if m.Freq == "Fixed" {
		return fmt.Sprintf("%#x", m.Number)
	}
	return strconv.FormatUint(uint64(m.Number), 10)
}

// ----------------------------------------------------------------- main

// The three places the template is published.  See the package comment
// for how they differ and why ViewerURL is the default.
const (
	// ViewerURL is the shipping client's copy: what the grid speaks.
	ViewerURL = "https://raw.githubusercontent.com/secondlife/viewer/main/scripts/messages/message_template.msg"

	// MasterURL is what Linden Lab's servers accept.  Their README
	// calls it the official public description of the protocol, and
	// the viewer's build verifies its own copy against it -- see
	// TEMPLATE_VERIFIER_MASTER_URL in indra/cmake/Variables.cmake.
	MasterURL = "https://raw.githubusercontent.com/secondlife/master-message-template/master/message_template.msg"

	// FirestormURL is the viewer's plus OpenSim's additions.
	FirestormURL = "https://raw.githubusercontent.com/FirestormViewer/phoenix-firestorm/master/scripts/messages/message_template.msg"
)

// read fetches the template from a URL, or reads it from a file.
//
// Which it is comes from the string itself rather than a second flag: a
// path and a URL are never confusable, and one flag means one thing to
// remember.
func read(src string) ([]byte, error) {
	if !strings.HasPrefix(src, "http://") && !strings.HasPrefix(src, "https://") {
		return os.ReadFile(src)
	}

	// Bounded, because a build that hangs on a network read with no
	// explanation is worse than one that fails saying so.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w\n"+
			"        -template also takes a path, for building offline", src, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching %s: %s", src, resp.Status)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", src, err)
	}
	// A proxy or a login page answering 200 with HTML would otherwise
	// reach the parser and be reported as a syntax error on line 1.
	if !bytes.HasPrefix(data, []byte("//")) && !bytes.Contains(data[:min(len(data), 200)], []byte("version")) {
		return nil, fmt.Errorf("%s did not answer with a message template", src)
	}
	return data, nil
}

func main() {
	var (
		src  = flag.String("template", ViewerURL, "URL or path of message_template.msg")
		out  = flag.String("out", "msg/messages_gen.go", "file to write, or - for stdout")
		pkg  = flag.String("package", "msg", "package name for the generated file")
		stat = flag.Bool("stats", false, "print a summary of what was parsed")
		ver  = flag.Bool("version", false, "say which build this is, and exit")
	)
	flag.Parse()
	if *ver {
		fmt.Println(version.String("msggen"))
		return
	}

	data, err := read(*src)
	if err != nil {
		fmt.Fprintf(os.Stderr, "msggen: %v\n", err)
		os.Exit(1)
	}

	msgs, err := Parse(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "msggen: %s: %v\n", *src, err)
		os.Exit(1)
	}

	code, err := Generate(*pkg, *src, msgs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "msggen: %v\n", err)
		os.Exit(1)
	}

	if *out == "-" {
		os.Stdout.Write(code)
	} else if err := os.WriteFile(*out, code, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "msggen: %v\n", err)
		os.Exit(1)
	}

	if *stat {
		printStats(os.Stderr, msgs)
	}
}

func printStats(w *os.File, msgs []Message) {
	freq := map[string]int{}
	types := map[string]int{}
	quant := map[string]int{}
	blocks, fields := 0, 0
	for _, m := range msgs {
		freq[m.Freq]++
		for _, b := range m.Block {
			blocks++
			quant[b.Quant]++
			for _, f := range b.Field {
				fields++
				types[f.Type]++
			}
		}
	}
	fmt.Fprintf(w, "%d messages, %d blocks, %d fields\n", len(msgs), blocks, fields)
	fmt.Fprintf(w, "priority:  %s\n", counts(freq))
	fmt.Fprintf(w, "quantifier: %s\n", counts(quant))
	fmt.Fprintf(w, "types:     %s\n", counts(types))
}

func counts(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if m[keys[i]] != m[keys[j]] {
			return m[keys[i]] > m[keys[j]]
		}
		return keys[i] < keys[j]
	})
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, m[k]))
	}
	return strings.Join(parts, " ")
}
