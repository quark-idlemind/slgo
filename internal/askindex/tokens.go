package askindex

// Turning text into the words an index is kept by.
//
// The words are applied twice -- to the documents when the index is
// built and to a question when it is searched -- and the two only meet
// if both went through exactly the same stemming.  Tokens is that.
// A question then weighs some of those words less: tokenWeights.  A
// document does not, so a page that says "sit" counts sit in full.
//
// # What a word is
//
// Lower case, and a run of letters and digits.  Everything else divides
// words, including an apostrophe: "grid's" is "grid", and the "s" left
// behind is one letter, which is dropped with every other word of one
// letter because none of them tells two commands apart.
//
// A long flag is the exception, because it is the thing a person most
// often copies out of a page and pastes back as a question.
// "--set-home" is kept whole, so that a question naming the flag finds
// the paragraph about that flag rather than every paragraph that says
// "set" and "home", and it is ALSO split into those words, so that a
// question that says "set home" in plain words finds the flag too.  A
// short flag, "-l", is kept whole for the same first reason and has no
// parts to add.
//
// # Stemming
//
// Light, and English only: plurals, -ing, -ed, -ment, and a final e.
// The aim is that "homes", "setting home" and "set my home" meet the
// words the pages use, not that every word reaches a dictionary root.
// A heavier stemmer (Porter's) conflates more, and here that is mostly
// harm: the vocabulary is small and specific, and "organise" and
// "organ" meeting would find nothing anybody asked for.  What matters is
// only that a word comes out the same whichever side it was on, which
// is true of anything deterministic; a stem that is not a real word --
// "mak" for make and making -- is invisible, since nobody reads it.
//
// A stem of three letters or fewer reached by taking off -ing or -ed
// counts for less in a question than a word that was typed (see
// stemCommandWeight).  "sitting" has to meet "sit", or a question about
// sitting never finds that command, and it also has to not count as the
// word "sit": a box described as sitting on the floor is not a question
// about sitting down, and an unweighted "sit" outranks the page that
// answers.  Plurals and a final e stay at a full weight.  Documents are
// indexed at full weight either way; only the question is scaled.
//
// # Find, asking to learn something
//
// "find" followed by a question word or by "out" -- "find where I am",
// "find out whether" -- asks to learn a thing, not to search for one,
// and it counts for less in a question too (see learnVerbWeight).
// Counted in full it is the heaviest word in "how do I find where I
// am", and puts the find command, and the commands that have find in
// their keywords, above the one that answers.  "find an item by name"
// is left at a full weight.
//
// # Stopwords
//
// A short list of the words every question has in it and no page is
// about: articles, pronouns, "how do I".  It is deliberately short.  A
// word dropped here is a word no question can ever search for, and
// several words that look ordinary are the names of commands -- who,
// where, look, find, get, give, take, set, new, no -- which is why none
// of those is on the list.  The package cannot know the command names;
// the caller's tests check that none of its names is stopped.

import (
	"strings"
	"unicode"
)

// stemCommandWeight is how much a question counts a stem of three
// letters or fewer that was reached by stripping -ing or -ed.  See the
// stemming note above.  Documents are not scaled: a page that says
// "sit" said sit.
const stemCommandWeight = 0.25

// learnVerbWeight is how much a question counts "find" when a question
// word or "out" follows it.  See the note above.  A quarter, and not
// nothing: "find where my hair is" is still asking for the find command.
// Why: doc/slsh.md#find-asking-to-learn-something
const learnVerbWeight = 0.25

// Tokens is the words of s, in order, as the index keeps them: lower
// case, stemmed, stopwords and single letters dropped, a long flag both
// whole and in parts.  The same word may appear more than once.
func Tokens(s string) []string {
	words, _ := scanTokens(s)
	return words
}

// tokenWeights is Tokens, and how much each distinct word counts for in
// a question.  A word typed as itself counts 1.  A short -ing or -ed
// stem counts stemCommandWeight, and "find" asking to learn something
// learnVerbWeight, unless the same word was also typed at full weight
// ("sit" beside "sitting").
func tokenWeights(s string) map[string]float64 {
	_, w := scanTokens(s)
	return w
}

// scanTokens is the one walk Tokens and tokenWeights share.
func scanTokens(s string) ([]string, map[string]float64) {
	var out []string
	weights := map[string]float64{}
	rs := []rune(strings.ToLower(s))
	for i := 0; i < len(rs); {
		r := rs[i]
		// A flag starts at a dash that is not in the middle of a word:
		// "--wait" is a flag, and "no-copy" is two words.
		if r == '-' && (i == 0 || !isWordRune(rs[i-1])) {
			j := i
			for j < len(rs) && rs[j] == '-' {
				j++
			}
			dashes := j - i
			k := j
			for k < len(rs) && (isWordRune(rs[k]) || rs[k] == '-') {
				k++
			}
			name := strings.TrimRight(string(rs[j:k]), "-")
			switch {
			case dashes == 2 && name != "" && unicode.IsLetter(rs[j]):
				out = append(out, "--"+name)
				weights["--"+name] = 1
				for _, part := range strings.Split(name, "-") {
					out = appendWord(out, weights, part, false)
				}
				i = j + len([]rune(name))
				continue
			case dashes == 1 && len([]rune(name)) == 1 && unicode.IsLetter(rs[j]):
				out = append(out, "-"+name)
				weights["-"+name] = 1
				i = k
				continue
			}
			i = j
			continue
		}
		if !isWordRune(r) {
			i++
			continue
		}
		j := i
		for j < len(rs) && isWordRune(rs[j]) {
			j++
		}
		w := string(rs[i:j])
		out = appendWord(out, weights, w, asksToLearn(w, rs[j:]))
		i = j
	}
	return out, weights
}

// appendWord adds one plain word, if it is one worth keeping, and
// records the weight it counts for in a question.  learning is whether
// it is "find" asking to learn something (asksToLearn).
func appendWord(out []string, weights map[string]float64, w string, learning bool) []string {
	if len([]rune(w)) < 2 || stopwords[w] {
		return out
	}
	stemmed, weak := stemWeak(w)
	wt := 1.0
	switch {
	case weak:
		wt = stemCommandWeight
	case learning:
		wt = learnVerbWeight
	}
	if weights[stemmed] < wt {
		weights[stemmed] = wt
	}
	return append(out, stemmed)
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// asksToLearn is whether w, with rest after it, is "find" followed by a
// question word or "out".  See learnVerbWeight.
func asksToLearn(w string, rest []rune) bool {
	if w != "find" {
		return false
	}
	i := 0
	for i < len(rest) && !isWordRune(rest[i]) {
		i++
	}
	j := i
	for j < len(rest) && isWordRune(rest[j]) {
		j++
	}
	return learnFollowers[string(rest[i:j])]
}

// learnFollowers are the words after "find" that make it ask to learn
// something rather than to search.
var learnFollowers = map[string]bool{
	"out": true, "what": true, "where": true, "which": true, "who": true, "whose": true,
	"when": true, "why": true, "how": true, "whether": true, "if": true,
}

// stopwords are dropped from documents and questions alike.  See the
// head of this file for why the list is short, and why it holds no
// word that is also the name of a command.
var stopwords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`
		a an the and or but if then so than
		to of in on at by for from with into onto about as
		is are was were be been being am
		it its this that these those
		i me my mine we us our you your yours he him his she her they them their
		do does did can could would should will shall might must
		how what which when why whether
		want wants wanted like just please some also very too
		have has had there`) {
		stopwords[w] = true
	}
}

// Stopword reports whether a word is one Tokens drops.  It is for the
// caller's test that none of its command names is one.
func Stopword(w string) bool { return stopwords[strings.ToLower(w)] }

// stem takes the common English endings off a lower-case word.  See the
// head of this file for how far it goes and why no further.
//
// Words of three letters or fewer are left alone: they are mostly
// command names and abbreviations -- cat, rez, ls, tp -- and taking a
// letter off one makes it a different word.
func stem(w string) string {
	s, _ := stemWeak(w)
	return s
}

// stemWeak is stem, and whether the result is a short -ing or -ed stem
// that a question should count lightly.  See stemCommandWeight.
func stemWeak(w string) (string, bool) {
	if len(w) <= 3 {
		return w, false
	}
	weak := false
	switch {
	case strings.HasSuffix(w, "sses"):
		w = w[:len(w)-2]
	case strings.HasSuffix(w, "ies"), strings.HasSuffix(w, "ied"):
		if len(w) > 4 {
			w = w[:len(w)-3] + "y"
		}
	case strings.HasSuffix(w, "ss"), strings.HasSuffix(w, "us"), strings.HasSuffix(w, "is"):
	case strings.HasSuffix(w, "s"):
		w = w[:len(w)-1]
	}
	switch {
	case strings.HasSuffix(w, "ing"):
		if base := w[:len(w)-3]; len(base) >= 2 && hasVowel(base) {
			w = undouble(base)
			if len(w) <= 3 {
				weak = true
			}
		}
	case strings.HasSuffix(w, "eed"):
		// "need" and "speed" are not need-ed.
	case strings.HasSuffix(w, "ed"):
		if base := w[:len(w)-2]; len(base) >= 2 && hasVowel(base) {
			w = undouble(base)
			if len(w) <= 3 {
				weak = true
			}
		}
	case strings.HasSuffix(w, "ment"):
		// Only off a long word, so that "attachment" meets "attach" and
		// "comment" and "argument" are not cut down to nothing.
		if base := w[:len(w)-4]; len(base) >= 5 {
			w = base
		}
	}
	if len(w) > 3 && strings.HasSuffix(w, "e") && !strings.HasSuffix(w, "ee") {
		w = w[:len(w)-1]
	}
	return w, weak
}

func hasVowel(s string) bool { return strings.ContainsAny(s, "aeiouy") }

// undouble makes "sitting" and "rezzed" meet "sit" and "rez", by taking
// one of a final doubled consonant off what is left once -ing or -ed is
// gone.  A double l or s is kept, because install and dress end that
// way without any suffix having been taken off.
func undouble(s string) string {
	n := len(s)
	if n < 3 || s[n-1] != s[n-2] || strings.ContainsRune("aeioulsy", rune(s[n-1])) {
		return s
	}
	return s[:n-1]
}
