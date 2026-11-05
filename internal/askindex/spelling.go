package askindex

// Spelling: a British spelling and an American one are the same word.
//
// The pages are written in American spelling and a question may be
// written in either, so both the pages and the question are folded to
// the American spelling before they meet.  The fold runs on both sides,
// like the stemming, so what matters is that each pair comes out the
// same, not that every stem is a word.  A rule that also catches a word
// with no British form ("promise" by the -ise rule) changes it the same
// way on both sides, and nothing is lost.
//
// Most of it works on the stem, after the endings are off, so that one
// rule covers every form of a word: "colours", "coloured" and
// "colouring" all stem to "colour", and become "color".

import "strings"

// britishWords are folded whole, before stemming: words whose two
// spellings differ in a way the stem does not reach, or which are not
// spellings of each other but are the same word to someone asking.
var britishWords = map[string]string{
	"spelt":           "spelled",
	"learnt":          "learned",
	"burnt":           "burned",
	"dreamt":          "dreamed",
	"judgement":       "judgment",
	"acknowledgement": "acknowledgment",
	"ageing":          "aging",
	"artefact":        "artifact",
	"artefacts":       "artifacts",
	"enrol":           "enroll",
	"enrols":          "enrolls",
	"fulfil":          "fulfill",
	"fulfils":         "fulfills",
	"sceptical":       "skeptical",
	"aluminium":       "aluminum",
	"anticlockwise":   "counterclockwise",
	"programme":       "program",
	"programmes":      "programs",
}

// britishRoots are stems that begin a word the same way in every form,
// mostly -our: "favour" covers favourite and favoured.
var britishRoots = []struct{ uk, us string }{
	{"behaviour", "behavior"},
	{"neighbour", "neighbor"},
	{"endeavour", "endeavor"},
	{"splendour", "splendor"},
	{"colour", "color"},
	{"favour", "favor"},
	{"honour", "honor"},
	{"labour", "labor"},
	{"flavour", "flavor"},
	{"rumour", "rumor"},
	{"harbour", "harbor"},
	{"armour", "armor"},
	{"humour", "humor"},
	{"vapour", "vapor"},
	{"odour", "odor"},
	{"savour", "savor"},
	{"parlour", "parlor"},
	{"rigour", "rigor"},
	{"clamour", "clamor"},
	{"grey", "gray"},
}

// britishStems are whole stems: -re words, which stem to "centr", and
// a few more.
var britishStems = map[string]string{
	"centr":     "center",
	"metr":      "meter",
	"centimetr": "centimeter",
	"millimetr": "millimeter",
	"kilometr":  "kilometer",
	"litr":      "liter",
	"fibr":      "fiber",
	"theatr":    "theater",
	"calibr":    "caliber",
	"meagr":     "meager",
	"sombr":     "somber",
	"lustr":     "luster",
	"spectr":    "specter",
	"licenc":    "licens",
	"defenc":    "defens",
	"offenc":    "offens",
	"pretenc":   "pretens",
	"programm":  "program",
	"practis":   "practic",
	"tyr":       "tir",
	"aeroplan":  "airplan",
	"catalogu":  "catalog",
	"dialogu":   "dialog",
	"analogu":   "analog",
	"prologu":   "prolog",
	"epilogu":   "epilog",
	"monologu":  "monolog",
	"traveller": "traveler",
}

// singleL are the words American spelling does not double the l of
// before -ed and -ing: cancelled and canceled.  The stemmer keeps a
// double l, because install ends that way, so the fold takes it off
// these only.
var singleL = map[string]bool{
	"cancel": true, "label": true, "travel": true, "model": true,
	"level": true, "signal": true, "total": true, "fuel": true,
	"channel": true, "dial": true, "tunnel": true, "panel": true,
	"jewel": true, "equal": true, "counsel": true, "rival": true,
	"pedal": true, "marshal": true, "shovel": true, "funnel": true,
	"libel": true, "quarrel": true, "revel": true, "marvel": true,
	"unravel": true, "grovel": true, "initial": true,
}

// american folds a stem, as stemWeak made it, to its American
// spelling.  The whole words in britishWords are folded before
// stemming, by appendWord.
func american(stem string) string {
	if a, ok := britishStems[stem]; ok {
		return a
	}
	for _, r := range britishRoots {
		if strings.HasPrefix(stem, r.uk) {
			return r.us + stem[len(r.uk):]
		}
	}
	n := len(stem)
	switch {
	case n > 3 && strings.HasSuffix(stem, "ll") && singleL[stem[:n-1]]:
		return stem[:n-1]
	case strings.Contains(stem, "isat") && n >= 8:
		// organisation, organisations: -isation is -ization.
		return strings.Replace(stem, "isat", "izat", 1)
	case n >= 6 && strings.HasSuffix(stem, "is") && !strings.HasSuffix(stem, "wis"):
		// recognise, recognised, recognising: the stem ends in -is
		// and the American one in -iz.  Not -wise: otherwise, likewise.
		return stem[:n-1] + "z"
	case n >= 6 && strings.HasSuffix(stem, "ys"):
		// analyse, paralyse.
		return stem[:n-1] + "z"
	}
	return stem
}
