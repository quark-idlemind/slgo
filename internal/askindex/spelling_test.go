package askindex

import (
	"strings"
	"testing"
)

// TestBritishAndAmericanSpellingsMeet: a question in one spelling finds a
// page in the other, in every form of the word.
func TestBritishAndAmericanSpellingsMeet(t *testing.T) {
	for _, pair := range [][2]string{
		{"colour", "color"},
		{"colours", "colors"},
		{"coloured", "colored"},
		{"neighbours", "neighbors"},
		{"neighbouring", "neighboring"},
		{"favourite", "favorite"},
		{"behaviour", "behavior"},
		{"honoured", "honored"},
		{"grey", "gray"},
		{"centre", "center"},
		{"centred", "centered"},
		{"metres", "meters"},
		{"centimetre", "centimeter"},
		{"cancelled", "canceled"},
		{"cancelling", "canceling"},
		{"labelled", "labeled"},
		{"travelling", "traveling"},
		{"modelled", "modeled"},
		{"recognise", "recognize"},
		{"recognised", "recognized"},
		{"recognising", "recognizing"},
		{"capitalised", "capitalized"},
		{"organisation", "organization"},
		{"analyse", "analyze"},
		{"licence", "license"},
		{"defence", "defense"},
		{"dialogue", "dialog"},
		{"dialogues", "dialogs"},
		{"catalogue", "catalog"},
		{"programme", "program"},
		{"spelt", "spelled"},
		{"judgement", "judgment"},
		{"anticlockwise", "counterclockwise"},
	} {
		uk, us := Tokens(pair[0]), Tokens(pair[1])
		if strings.Join(uk, " ") != strings.Join(us, " ") {
			t.Errorf("%q is %v and %q is %v; they should meet", pair[0], uk, pair[1], us)
		}
	}
}

// TestTheFoldLeavesOtherWordsApart: a double l that is not a British
// spelling stays, and words that only look alike do not meet.
func TestTheFoldLeavesOtherWordsApart(t *testing.T) {
	for _, pair := range [][2]string{
		{"install", "instal"},
		{"spell", "spel"},
		{"called", "cal"},
		{"metric", "meter"},
		{"otherwise", "otherwize"},
		{"hour", "hor"},
		{"your", "yor"},
		{"centre", "cent"},
	} {
		a, b := Tokens(pair[0]), Tokens(pair[1])
		if strings.Join(a, " ") == strings.Join(b, " ") {
			t.Errorf("%q and %q both come out %v", pair[0], pair[1], a)
		}
	}
}

// TestAFlagIsKeptAsWrittenAndItsWordsAreFolded: --colour is not the flag
// --color, but a question that says it still finds the page about color.
func TestAFlagIsKeptAsWrittenAndItsWordsAreFolded(t *testing.T) {
	got := Tokens("--map-friend-colour")
	if got[0] != "--map-friend-colour" || got[len(got)-1] != "color" {
		t.Errorf("Tokens(--map-friend-colour) = %v", got)
	}
}
