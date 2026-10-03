package slate

import (
	"strings"
	"testing"
)

// The base-language worked examples, one file each, as the language page
// gave them before the four changes (ids are invented, names are Example).
var baseExamples = []string{
	`
slate 1
timeout 10s

object hud is "Test HUD"
object sign is "Example Sign"

touch hud button text "Open"
expect texture sign face 0 is ` + idOne + ` within 8s
expect offset sign face 0 is 0.25 0 within 8s
expect repeats sign face 0 is 2 1 within 8s
`, `
slate 1

object vendor is "Example Tip Jar"

say "menu" on -7 as owner of vendor
expect dialog from vendor text "Choose a colour" button "Red" button "Blue"

choose "Red" on vendor
expect say "red" on public from object vendor
expect give "Example Red Swatch" from vendor
`, `
slate 1
allow pay

object vendor is "Example Tip Jar"

pay vendor L$5 reason "tip"
expect give "Example Thank You" from vendor
expect rez name "Example Balloon" description "left" from vendor as left
expect rez name "Example Balloon" description "right" from vendor as right

touch left button text "Pop"
expect say "pop" on public from object left
`, `
slate 1

object chair is "Example Chair"

sit chair
expect click chair is touch within 10s

stand
`, `
slate 1

object slider is "Example Slider"

drag slider face 0 from 0.1 0.5 to 0.9 0.5 over 500ms
expect offset slider face 0 is 0.4 0 within 10s
`, `
slate 1

object panel is "Example Panel"
probe panel

touch panel anywhere
expect say "root" on public from object panel

touch panel face 2 at 0.9 0.5
expect say "face" on public from object panel

touch panel link 3
expect say "child" on public from anyone
`, `
slate 1

object hud is "Test HUD"
object sign is "Example Sign"

touch hud button text "Next" box symbol "right arrow" circle
expect say "next" on public from object sign
`, `
slate 1

object board is "Example Guest Book"

touch board button text "Sign"
expect textbox from board text "Name yourself"

answer "Example Resident" on board
expect say "Hello, Example Resident" on public from object board
`, `
slate 1

object vendor is "Example Tip Jar"
probe vendor

send on vendor from link 1 to link others num 7 text "ready"
expect link on vendor from link 1 num 7 text "ready"

then
expect link on vendor from link 3 num 9 text "go" heard by 1
`, `
slate 1

object sign is "Example Sign"

say "ping" on 1 as tester
expect say "pong" on public from object sign within 1s
`, `
slate 1

object vendor is "Example Tip Jar"

say "menu" on -7 as tester
expect dialog from vendor text "Choose a colour" button "Red" button "Blue"
choose "Red" on vendor
expect say "red" on public from object vendor

then
expect no say "error" on public from object vendor within 2s
`, `
slate 1

object vendor is "Example Tip Jar"
listen 1

say "ping" on 1 as tester
expect say "pong" on 1 from object vendor within 1s
`,
}

func TestBaseLanguageWorkedExamples(t *testing.T) {
	for i, src := range baseExamples {
		s := mustCheck(t, src)
		if len(s.Tests) != 1 || !s.Tests[0].Implicit || len(s.Tests[0].Steps) == 0 {
			t.Fatalf("example %d: %+v", i+1, s.Tests)
		}
	}
}

// Each example, with its steps moved into a test block, is the same test.
func TestBaseExamplesAsATestBlock(t *testing.T) {
	for i, src := range baseExamples {
		var head, rest []string
		for _, line := range strings.Split(strings.TrimSpace(src), "\n") {
			switch strings.Fields(line + " x")[0] {
			case "slate", "timeout", "allow", "object", "probe", "listen":
				head = append(head, line)
			default:
				rest = append(rest, line)
			}
		}
		suite := strings.Join(head, "\n") + "\ntest \"example\" {\n" + strings.Join(rest, "\n") + "\n}\n"
		plain := mustCheck(t, src)
		blocks := mustCheck(t, suite)
		if len(blocks.Tests) != 1 || blocks.Tests[0].Implicit || blocks.Tests[0].Name != "example" {
			t.Fatalf("example %d: %+v", i+1, blocks.Tests)
		}
		if a, b := len(plain.Tests[0].Steps), len(blocks.Tests[0].Steps); a != b {
			t.Fatalf("example %d: %d steps plain, %d in a block", i+1, a, b)
		}
	}
}

func TestBaseExampleDetails(t *testing.T) {
	s := mustCheck(t, baseExamples[0])
	ex := body(s)[0].Expect
	if ex[0].Texture.State.Kind != StateIs || ex[0].Texture.ID != idOne || ex[0].Within.Value.Seconds() != 8 {
		t.Fatalf("%+v", ex[0].Texture)
	}
	s = mustCheck(t, baseExamples[8])
	if st := body(s); len(st) != 2 || !st[1].Then || st[1].Expect[0].Link.HeardBy.Value != 1 || st[1].Expect[0].Link.Text.Pattern {
		t.Fatalf("%+v", st)
	}
	s = mustCheck(t, baseExamples[5])
	if st := body(s); len(st) != 3 || st[2].Expect[0].Say.From.Kind != SpeakAnyone {
		t.Fatalf("%+v", st)
	}
}
