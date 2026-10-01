// Package tools tests the scripts in this directory that keep real
// identifiers out of the tree: scan-ids, new-id and check-identities.
// They are perl and sh, so these tests run them rather than call them, and
// skip where perl is not installed.
package tools

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func needPerl(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("perl is not installed")
	}
}

// scan runs scan-ids --check over one file holding text and returns what
// it printed and whether it refused.
func scan(t *testing.T, text string) (string, bool) {
	t.Helper()
	needPerl(t)
	f := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(f, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cmd := exec.Command("perl", "scan-ids", "--check", f)
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			t.Fatal(err)
		}
	}
	return out.String(), err != nil
}

// The id every form below spells, built at run time so that none of its
// forms is written down in this file: bytes 0xc3 throughout, which is
// signed by nobody.
const unsignedByte = "c3"

// forms spells one id in every shape the checker looks for, each as the
// whole of a line a file might hold.  hex is 32 digits.
func forms(hex string) map[string]string {
	b := make([]string, 16)
	for i := range b {
		b[i] = "0x" + hex[2*i:2*i+2]
	}
	dec := make([]string, 16)
	for i := range dec {
		var n int
		fmt.Sscanf(hex[2*i:2*i+2], "%x", &n)
		dec[i] = fmt.Sprint(n)
	}
	raw := make([]byte, 16)
	for i := range raw {
		var n int
		fmt.Sscanf(hex[2*i:2*i+2], "%x", &n)
		raw[i] = byte(n)
	}
	hy := hex[:8] + "-" + hex[8:12] + "-" + hex[12:16] + "-" + hex[16:20] + "-" + hex[20:]
	var esc strings.Builder
	for i := range 16 {
		esc.WriteString(`\x` + hex[2*i:2*i+2])
	}
	return map[string]string{
		"hyphenated":        hy,
		"upper case":        strings.ToUpper(hy),
		"braces":            "{" + hy + "}",
		"in a url":          "https://example.invalid/cap/" + hy + "/about",
		"llsd notation":     "u" + hy,
		"no hyphens":        hex,
		"hexdump":           hex[:8] + " " + hex[8:16] + " " + hex[16:24] + " " + hex[24:],
		"wrapped hexdump":   "// " + hex[:8] + " " + hex[8:16] + "\n// " + hex[16:24] + " " + hex[24:],
		"byte pairs":        strings.Join(strings.Split(strings.Join(b, " "), "0x"), ""),
		"groups of four":    hex[0:4] + ":" + hex[4:8] + ":" + hex[8:12] + ":" + hex[12:16] + ":" + hex[16:20] + ":" + hex[20:24] + ":" + hex[24:28] + ":" + hex[28:],
		"uuid literal":      "msg.UUID{" + strings.Join(b, ", ") + "}",
		"wrapped literal":   "msg.UUID{" + strings.Join(b[:8], ", ") + ",\n\t" + strings.Join(b[8:], ", ") + "}",
		"byte slice":        "[]byte{" + strings.Join(b, ", ") + "}",
		"byte array":        "[16]byte{" + strings.Join(b, ", ") + "}",
		"decimal bytes":     "[16]byte{" + strings.Join(dec, ", ") + "}",
		"escapes":           `"` + esc.String() + `"`,
		"base64":            base64.StdEncoding.EncodeToString(raw),
		"elided":            hex[:8] + "-...",
		"elided ellipsis":   hex[:8] + "…",
		"elided and groups": hy[:13] + "-...",
		"literal prefix":    "msg.UUID{" + strings.Join(b[:4], ", ") + "}",
	}
}

func TestAnIdThatIsNotSignedIsFoundInEveryForm(t *testing.T) {
	// A base64 id needs a digit or a plus to be told from a word, so the
	// bytes are not all one value.
	hex := strings.Repeat(unsignedByte, 15) + "fb"
	for name, text := range forms(hex) {
		t.Run(name, func(t *testing.T) {
			if _, refused := scan(t, text+"\n"); !refused {
				t.Errorf("%q was let through", text)
			}
		})
	}
}

func TestAnIdThatIsSignedIsLetThroughInEveryForm(t *testing.T) {
	hex := "1d2e" + "7e577e57c0de" + "0123456789abcdef"
	for name, text := range forms(hex) {
		t.Run(name, func(t *testing.T) {
			if out, refused := scan(t, text+"\n"); refused {
				t.Errorf("%q was refused:\n%s", text, out)
			}
		})
	}
}

func TestTheNullKeyAndLindensOwnIdsAreKnown(t *testing.T) {
	null := "00000000-0000-0000-0000-000000000000"
	// The built-in plywood texture, which known-uuids lists.
	plywood := "89556747-24cb-43ed-920b-47caed15465f"
	for _, id := range []string{null, plywood, strings.ToUpper(plywood), plywood[:8] + "-...", strings.ReplaceAll(plywood, "-", "")} {
		if out, refused := scan(t, id+"\n"); refused {
			t.Errorf("%s was refused:\n%s", id, out)
		}
	}
	// But thirty-two zeros with no hyphens is a digest left blank.
	if _, refused := scan(t, "id0 = "+strings.Repeat("0", 32)+"\n"); !refused {
		t.Error("an all-zero digest was let through")
	}
}

func TestAPlaceholderIsNotAnException(t *testing.T) {
	for _, id := range []string{
		"bbbbbbbb" + "-0000-4000-8000-" + "000000000001",
		"00010203" + "-0405-0607-0809-" + "0a0b0c0d0e0f",
		strings.Repeat("a", 32),
		"0123456789abcdef" + "0123456789abcdef",
	} {
		if _, refused := scan(t, id+"\n"); !refused {
			t.Errorf("%s was let through as a placeholder", id)
		}
	}
}

func TestAnElidedIdMustEndItsFirstGroupInTheSignature(t *testing.T) {
	if _, refused := scan(t, "ab3f9d41"+"-...\n"); !refused {
		t.Error("an elided id with no signature was let through")
	}
	if out, refused := scan(t, "ab3f7e57-...\n"); refused {
		t.Errorf("a signed elided id was refused:\n%s", out)
	}
}

func TestOnlySixteenBytesOfBase64AreAnId(t *testing.T) {
	hex := "ab" + strings.Repeat("c3", 14) + "fb"
	raw := make([]byte, 16)
	for i := range raw {
		fmt.Sscanf(hex[2*i:2*i+2], "%x", &raw[i])
	}
	// Longer, not in a <binary>: not looked at.
	long := base64.StdEncoding.EncodeToString(append(slices.Clone(raw), raw...))
	if out, refused := scan(t, "x = "+long+"\n"); refused {
		t.Errorf("a longer base64 string was refused:\n%s", out)
	}
}

// llsdDoc is an LLSD binary document, header and all: a map with one key
// whose value is an array holding an integer and a uuid, nested so that
// the id is not at a fixed offset.
func llsdDoc(id []byte) []byte {
	d := []byte("<? LLSD/Binary ?>\n{\x00\x00\x00\x01k\x00\x00\x00\x03ids[\x00\x00\x00\x02i\x00\x00\x00\x07u")
	d = append(d, id...)
	return append(d, ']', '}')
}

func xmlBinary(b []byte) string {
	return "<key>k</key><binary>" + base64.StdEncoding.EncodeToString(b) + "</binary>\n"
}

func TestAnIdNestedInLLSDBinaryIsFoundWhatEverItsVersion(t *testing.T) {
	// Byte 6 has a high nibble of 0, so this is not a version 4 uuid and
	// nothing may be assumed from its shape.
	unsigned := bytes.Repeat([]byte{0xc3}, 16)
	unsigned[6], unsigned[8] = 0x0a, 0x1b
	if _, refused := scan(t, xmlBinary(llsdDoc(unsigned))); !refused {
		t.Error("an unsigned id nested in LLSD binary was let through")
	}
	signed := []byte{0x1d, 0x2e, 0x7e, 0x57, 0x7e, 0x57, 0xc0, 0xde, 1, 2, 3, 4, 5, 6, 7, 8}
	if out, refused := scan(t, xmlBinary(llsdDoc(signed))); refused {
		t.Errorf("a signed id nested in LLSD binary was refused:\n%s", out)
	}
	doc := llsdDoc(unsigned)
	out, refused := scan(t, xmlBinary(doc[:len(doc)-6]))
	if !refused || !strings.Contains(out, "unparseable") {
		t.Errorf("truncated LLSD binary was not reported as unparseable:\n%s", out)
	}
}

// TestBytesInABinaryThatAreNotLLSDAreLookedAt: a packed field longer than
// an id can hold one at any offset, and nothing can say where, so a person
// looks rather than the check passing it.
func TestBytesInABinaryThatAreNotLLSDAreLookedAt(t *testing.T) {
	packed := append([]byte{0xff, 0x00, 0x13}, bytes.Repeat([]byte{0xc3}, 17)...)
	out, refused := scan(t, xmlBinary(packed))
	if !refused || !strings.Contains(out, "unparseable") {
		t.Errorf("twenty bytes of packed field in a <binary> were let through:\n%s", out)
	}
	// A string the grid sent as binary, NUL and all, is text.
	if out, refused := scan(t, xmlBinary([]byte("Example Builders and Friends\x00"))); refused {
		t.Errorf("a string sent as binary was refused:\n%s", out)
	}
}

func TestNewIdMakesSignedIdsInOrderAndNotTwiceOverFirstGroups(t *testing.T) {
	needPerl(t)
	out, err := exec.Command("perl", "new-id", "-n", "40").Output()
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Fields(string(out))
	if len(lines) != 40 || !slices.IsSorted(lines) {
		t.Fatalf("want 40 ids in order, got %d: %v", len(lines), lines)
	}
	sig := regexp.MustCompile(`^[0-9a-f]{4}7e57-7e57-c0de-[0-9a-f]{4}-[0-9a-f]{12}$`)
	seen := map[string]bool{}
	for _, l := range lines {
		if !sig.MatchString(l) {
			t.Errorf("%q is not signed", l)
		}
		if seen[l[:8]] {
			t.Errorf("first group %s twice", l[:8])
		}
		seen[l[:8]] = true
	}
	out, err = exec.Command("perl", "new-id", "--hash").Output()
	if err != nil {
		t.Fatal(err)
	}
	if h := strings.TrimSpace(string(out)); !regexp.MustCompile(`^[0-9a-f]{4}7e577e57c0de[0-9a-f]{16}$`).MatchString(h) {
		t.Errorf("--hash gave %q", h)
	}
}

// scanNames runs scan-names --check over one file holding text.  name is
// the file's name, since a few places are read by what kind of file it is.
func scanNames(t *testing.T, name, text string) (string, bool) {
	t.Helper()
	needPerl(t)
	f := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(f, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cmd := exec.Command("perl", "scan-names", "--check", f)
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			t.Fatal(err)
		}
	}
	return out.String(), err != nil
}

// strange is a name that is on nobody's list: it is made of letters that
// no name here uses, so it is refused wherever a name is recognised.
const strange = "Zxqvl Wmbrt"

// TestAnUnlistedNameIsRefusedWhereANameIsRecognised is the table of places
// doc/identities.md says a name is read, each with a line that names
// something not on tools/known-names (refused) and, beside it, the same
// line with a name that is on it (accepted).
func TestAnUnlistedNameIsRefusedWhereANameIsRecognised(t *testing.T) {
	needPerl(t)
	llsdString := func(s string) string { // LLSD binary: a map holding one string
		b := []byte("<? LLSD/Binary ?>\n{\x00\x00\x00\x01k\x00\x00\x00\x01as\x00\x00\x00" + string(rune(len(s)+1)) + s + "\x00}")
		return base64.StdEncoding.EncodeToString(b)
	}
	places := []struct {
		place, file, line string // %s is the name
		listed            string // a name on the list for the same line
	}{
		{"name field", "a_test.go", `	g := Group{Name: "%s"}`, "Quark Engineering Works"},
		{"name field compared", "a_test.go", `	if r.RegionName != "%s" {`, "Pelmar Reach"},
		{"name field as bytes", "a_test.go", `	m.RegionInfo.SimName = []byte("%s\x00")`, "Pelmar Reach"},
		{"fixture key", "a.xml", `<key>SimName</key><string>%s</string>`, "Pelmar Reach"},
		{"JSON key", "a_test.go", `	body := {"GroupName": "%s"}`, "Pelmar Reach Builders"},
		{"helper argument", "a_test.go", `	mapBlock("%s", 43520, 43520, 13)`, "Pelmar Reach"},
		{"id to name map", "a_test.go", `	names := map[msg.UUID]string{a: "%s"}`, "Perrick Hobb"},
		{"labelled variable", "a_test.go", `	owner: "%s",`, "Kerra Yule"},
		{"profile first/last", "a.md", "    first    = %s", "Example"},
		{"--first", "a.md", "    slrun --direct --first %s --last Resident script.lsl", "Quark"},
		{"login", "a.md", "      --login %s 'secret' --autologin", "Taren Holt"},
		{"username", "a_test.go", `	inviting("%s", "join")`, "quark.idlemind"},
		{"username field", "a_test.go", `	Username: "%s"`, "perrick.hobb"},
		{"SLURL", "a.md", "    secondlife://%s/128/128/25", "Pelmar%20Reach"},
		{"map link", "a.md", "    https://maps.secondlife.com/secondlife/%s/128/128/25", "Example%20Region"},
		{"region at a position", "a.md", "    %s at 128, 128, 25", "Nightmire Sea"},
		{"tp", "a.md", "    tp %s 33 73 1000.5", "Sandbox Goguen"},
		{"tp key", "a_test.go", `	x.do(t, "tp %s")`, "Example Landing"},
		{"regions key", "a.md", "    regions %s", "Pelm"},
		{"region listing", "a.md", "%s                      43647, 43648 203.0.113.9:13009  412 heard", "Kelva Sands"},
		{"parcel legend", "a.html", "  a  %s              2048 m²  owned", "Thrushmoor Home"},
		{"acting as group", "a.md", "    acting as group %s (d8467e57-...)", "Example Builders"},
		{"group in a profile", "a.md", "    group    = %s", "Example Builders"},
		{"group flag", "a.md", `    slgod -group "%s" example`, "Example Builders"},
		{"landmark path", "a.md", "    /Landmarks/%s", "Thrushmoor"},
		{"landmark command", "a.md", "    landmark --go %s", "Example Workshop"},
		{"chat sender", "a.md", "    12:01:40 < %s in [Group] Example Builders: hi", "Example Resident"},
		{"presence line", "a.md", "    15:07:40 * %s is online", "Example Resident"},
		{"text in base64", "a.xml", "", "Example Builders"},
		{"string in LLSD binary", "a.xml", "", "Example Builders"},
	}
	for _, p := range places {
		t.Run(p.place, func(t *testing.T) {
			build := func(name string) string {
				switch p.place {
				case "text in base64":
					return `<binary encoding="base64">` + base64.StdEncoding.EncodeToString([]byte(name+"\x00")) + `</binary>`
				case "string in LLSD binary":
					return `<binary encoding="base64">` + llsdString(name) + `</binary>`
				}
				return fmt.Sprintf(p.line, name)
			}
			bad := strings.ReplaceAll(strange, " ", map[bool]string{true: "%20", false: " "}[strings.Contains(p.line, "secondlife")])
			switch p.place {
			case "username", "username field":
				bad = "zxqvl.wmbrt"
			case "profile first/last":
				bad = "Zxqvl"
			case "--first":
				bad = "Zxqvl"
			case "regions key":
				bad = "Zxqv"
			}
			out, refused := scanNames(t, p.file, build(bad)+"\n")
			if !refused || !strings.Contains(out, "is not on tools/known-names") {
				t.Errorf("%q was not refused (%q)", build(bad), out)
			}
			if out, refused := scanNames(t, p.file, build(p.listed)+"\n"); refused {
				t.Errorf("%q is on the list and was refused: %s", build(p.listed), out)
			}
		})
	}
}

// TestNamesAreMatchedLoosely and TestProseIsNotRead pin down what the check
// does not do, which doc/identities.md also says.
func TestNamesAreMatchedLoosely(t *testing.T) {
	for _, line := range []string{
		`Name: "pelmar reach"`, // case is ignored
		`Name: "PELMAR REACH"`,
		`Name: "Rue&amp;Reed"`,       // an HTML entity is the character
		`Name: "Garrow''s Hideaway"`, // a doubled apostrophe is one
		`Name: "Taren"`,              // one word of a name
	} {
		if out, refused := scanNames(t, "a_test.go", line+"\n"); refused {
			t.Errorf("%s was refused: %s", line, out)
		}
	}
}

func TestProseIsNotRead(t *testing.T) {
	// A name in a sentence looks like any capitalised words, so it is left
	// to the person reading; so is a protocol message's name.
	for _, line := range []string{
		strange + " wandered in and said hello.",
		`Name: "ObjectUpdate"`,
		`Name: "NAME"`,
		`Name: "a lamp"`,
	} {
		if out, refused := scanNames(t, "a.md", line+"\n"); refused {
			t.Errorf("%q was refused: %s", line, out)
		}
	}
}

// TestTheListHoldsFiveSparesOfEachKind: a new name is taken from the spares
// first, so there has to be a spare to take.
func TestTheListHoldsFiveSparesOfEachKind(t *testing.T) {
	b, err := os.ReadFile("known-names")
	if err != nil {
		t.Fatal(err)
	}
	spares := map[string]int{}
	for _, l := range strings.Split(string(b), "\n") {
		if kind, ok := strings.CutPrefix(strings.Split(l, "\t")[len(strings.Split(l, "\t"))-1], "spare "); ok && !strings.HasPrefix(l, "#") {
			spares[kind]++
		}
	}
	for _, kind := range []string{"avatar", "region", "parcel", "group", "landmark", "object", "estate"} {
		if spares[kind] < 5 {
			t.Errorf("known-names has %d spare %ss, want 5", spares[kind], kind)
		}
	}
}

// TestCheckIdentitiesRefusesAnUnlistedName runs the whole check, as a
// commit would, over a file that names something not on the list.
func TestCheckIdentitiesRefusesAnUnlistedName(t *testing.T) {
	needPerl(t)
	// check-identities reads files inside the checkout, so the probe is
	// made at its top and removed again.
	probe := "zz_names_probe.go"
	if err := os.WriteFile(filepath.Join("..", probe), []byte("var g = Group{Name: \""+strange+"\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(filepath.Join("..", probe))
	cmd := exec.Command("sh", "tools/check-identities", probe)
	cmd.Dir = ".."
	cmd.Env = append(os.Environ(), "SLGO_IDENTITIES=/nonexistent")
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "these names are not on tools/known-names") {
		t.Errorf("check-identities passed or said the wrong thing (%v): %s", err, out)
	}
}
