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
	// In a <binary>, a window shaped like a random uuid is an id.
	v4 := slices.Clone(raw)
	v4[6], v4[8] = 0x4a, 0x9b
	blob := base64.StdEncoding.EncodeToString(append([]byte{1, 2, 3}, v4...))
	if _, refused := scan(t, "<key>k</key><binary>"+blob+"</binary>\n"); !refused {
		t.Error("an id inside an LLSD binary was let through")
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
