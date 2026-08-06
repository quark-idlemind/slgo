package sl

import (
	"bytes"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/llsd"
)

// The shape the LSLSyntax capability answers with, cut down to four
// entries.  Taken from what the live grid returned, so the parser is
// tested against the real document rather than an idea of it.
const syntaxDocument = `<?xml version="1.0" ?>
<llsd><map>
  <key>llsd-lsl-syntax-version</key><integer>2</integer>
  <key>functions</key><map>
    <key>llSay</key><map>
      <key>arguments</key><array>
        <map><key>Channel</key><map>
          <key>tooltip</key><string>Channel to use to say text on.</string>
          <key>type</key><string>integer</string></map></map>
        <map><key>Text</key><map>
          <key>tooltip</key><string>Text to say.</string>
          <key>type</key><string>string</string></map></map>
      </array>
      <key>energy</key><integer>10</integer>
      <key>return</key><string>void</string>
      <key>sleep</key><integer>0</integer>
      <key>tooltip</key><string>Says Text on Channel.</string>
    </map>
    <key>llSleep</key><map>
      <key>arguments</key><array>
        <map><key>Time</key><map><key>type</key><string>float</string></map></map>
      </array>
      <key>energy</key><integer>0</integer>
      <key>return</key><string>void</string>
      <key>sleep</key><real>0.0</real>
    </map>
    <key>llMakeExplosion</key><map>
      <key>deprecated</key><boolean>true</boolean>
      <key>energy</key><integer>10</integer>
      <key>return</key><string>void</string>
      <key>sleep</key><real>0.1</real>
    </map>
    <key>llGetKey</key><map>
      <key>energy</key><integer>10</integer>
      <key>return</key><string>key</string>
      <key>sleep</key><integer>0</integer>
    </map>
  </map>
  <key>constants</key><map>
    <key>CHANGED_COLOR</key><map>
      <key>tooltip</key><string>The object color has changed.</string>
      <key>type</key><string>integer</string>
      <key>value</key><string>0x2</string>
    </map>
  </map>
  <key>events</key><map>
    <key>listen</key><map>
      <key>arguments</key><array>
        <map><key>Channel</key><map><key>type</key><string>integer</string></map></map>
        <map><key>Name</key><map><key>type</key><string>string</string></map></map>
        <map><key>ID</key><map><key>type</key><string>key</string></map></map>
        <map><key>Text</key><map><key>type</key><string>string</string></map></map>
      </array>
      <key>tooltip</key><string>Raised on hearing chat.</string>
    </map>
  </map>
  <key>types</key><map>
    <key>integer</key><map><key>tooltip</key><string>32 bit integer value.</string></map>
  </map>
  <key>controls</key><map>
    <key>jump</key><map><key>tooltip</key><string>Jump to a label.</string></map>
  </map>
</map></llsd>`

func parseTestSyntax(t *testing.T) *Syntax {
	t.Helper()
	v, err := llsd.Decode(bytes.NewReader([]byte(syntaxDocument)))
	if err != nil {
		t.Fatal(err)
	}
	m := llsd.Map(v)
	if m == nil {
		t.Fatalf("document decoded as %T", v)
	}
	return parseSyntax(m)
}

func TestParseSyntax(t *testing.T) {
	s := parseTestSyntax(t)

	if s.Version != 2 {
		t.Errorf("version = %d", s.Version)
	}
	if len(s.Functions) != 4 || len(s.Constants) != 1 || len(s.Events) != 1 {
		t.Fatalf("counts: %d functions, %d constants, %d events",
			len(s.Functions), len(s.Constants), len(s.Events))
	}

	say, ok := s.Functions["llSay"]
	if !ok {
		t.Fatal("llSay is missing")
	}
	if say.Return != "void" || say.Energy != 10 || say.Sleep != 0 {
		t.Errorf("llSay = %+v", say)
	}
	// The arguments are ordered, and the order is what a call site has
	// to match; a map would have lost it.
	if len(say.Arguments) != 2 ||
		say.Arguments[0].Name != "Channel" || say.Arguments[0].Type != "integer" ||
		say.Arguments[1].Name != "Text" || say.Arguments[1].Type != "string" {
		t.Errorf("llSay arguments = %+v", say.Arguments)
	}
	if got := say.Signature(); got != "void llSay(integer Channel, string Text)" {
		t.Errorf("signature = %q", got)
	}

	// A function that sleeps, and one that is deprecated.
	if s.Functions["llMakeExplosion"].Sleep != 0.1 {
		t.Errorf("llMakeExplosion sleep = %v", s.Functions["llMakeExplosion"].Sleep)
	}
	if !s.Functions["llMakeExplosion"].Deprecated {
		t.Error("llMakeExplosion should be deprecated")
	}
	if s.Functions["llSay"].Deprecated {
		t.Error("llSay is not deprecated")
	}

	// A function with no arguments still parses, and renders.
	if got := s.Functions["llGetKey"].Signature(); got != "key llGetKey()" {
		t.Errorf("llGetKey signature = %q", got)
	}

	c := s.Constants["CHANGED_COLOR"]
	if c.Type != "integer" || c.Value != "0x2" {
		t.Errorf("CHANGED_COLOR = %+v", c)
	}

	e := s.Events["listen"]
	if len(e.Arguments) != 4 || e.Arguments[2].Name != "ID" || e.Arguments[2].Type != "key" {
		t.Errorf("listen arguments = %+v", e.Arguments)
	}

	if s.Types["integer"] == "" || s.Controls["jump"] == "" {
		t.Errorf("types and controls: %v %v", s.Types, s.Controls)
	}
}

func TestSyntaxNamesAreSorted(t *testing.T) {
	s := parseTestSyntax(t)
	got := s.FunctionNames()
	want := []string{"llGetKey", "llMakeExplosion", "llSay", "llSleep"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("FunctionNames = %v, want %v", got, want)
	}
}

// TestParseSyntaxSurvivesRubbish: the document is Linden Lab's to
// change, and a field that is missing or of the wrong shape must not
// take the rest of the language with it.
func TestParseSyntaxSurvivesRubbish(t *testing.T) {
	s := parseSyntax(map[string]any{
		"llsd-lsl-syntax-version": "two",
		"functions": map[string]any{
			"llFine":    map[string]any{"return": "void", "energy": int64(10)},
			"llOdd":     "not a map",
			"llNoTypes": map[string]any{"arguments": []any{map[string]any{"X": "not a map"}}},
		},
		"constants": "not a map",
		"events":    map[string]any{},
	})
	if s.Version != 0 {
		t.Errorf("an unreadable version should be zero, got %d", s.Version)
	}
	if len(s.Functions) != 3 {
		t.Errorf("functions = %v", s.FunctionNames())
	}
	if s.Functions["llFine"].Energy != 10 {
		t.Error("the good entry was lost with the bad ones")
	}
	if got := s.Functions["llNoTypes"].Arguments; len(got) != 1 || got[0].Name != "X" || got[0].Type != "" {
		t.Errorf("arguments = %+v", got)
	}
}

func TestFeatures(t *testing.T) {
	f := &Features{Raw: map[string]any{
		"MeshRezEnabled":       true,
		"LuaScriptsEnabled":    false,
		"MaxAgentGroups":       int64(50),
		"MaxTextureResolution": float64(2048),
		"HostName":             "simhost-0f5.agni.secondlife.io",
		"LSLSyntaxId":          "c9767e57-7e57-c0de-6bd6-02d6d9429743",
		"PhysicsShapeTypes":    map[string]any{"convex": true, "none": true, "prim": true},
	}}

	if !f.MeshRezEnabled() || f.LuaScriptsEnabled() {
		t.Error("bool features read wrong")
	}
	if f.MaxAgentGroups() != 50 {
		t.Errorf("MaxAgentGroups = %d", f.MaxAgentGroups())
	}
	// A number that arrived as a real still reads as a number.
	if f.MaxTextureResolution() != 2048 {
		t.Errorf("MaxTextureResolution = %d", f.MaxTextureResolution())
	}
	if f.HostName() == "" || f.LSLSyntaxID().IsZero() {
		t.Error("string and uuid features read wrong")
	}
	if len(f.Map("PhysicsShapeTypes")) != 3 {
		t.Error("nested map feature read wrong")
	}

	// A feature nobody has heard of is absent, not false-because-off.
	if f.Has("NoSuchFeature") || f.Bool("NoSuchFeature") || f.Int("NoSuchFeature") != 0 {
		t.Error("an unknown feature should read as absent and zero")
	}
	if !f.Has("LuaScriptsEnabled") {
		t.Error("a feature that is present but off is still present")
	}
	if got := f.Names(); len(got) != 7 || got[0] != "HostName" {
		t.Errorf("Names = %v", got)
	}
}
