package askindex

// Turning text into the words an index is kept by.
//
// Everything here is applied twice -- to the documents when the index
// is built and to a question when it is searched -- and the two only
// meet if both went through exactly the same function.  So there is one,
// Tokens, and nothing about a question is handled anywhere else.
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

// Tokens is the words of s, in order, as the index keeps them: lower
// case, stemmed, stopwords and single letters dropped, a long flag both
// whole and in parts.  The same word may appear more than once.
func Tokens(s string) []string {
	var out []string
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
				for _, part := range strings.Split(name, "-") {
					out = appendWord(out, part)
				}
				i = j + len([]rune(name))
				continue
			case dashes == 1 && len([]rune(name)) == 1 && unicode.IsLetter(rs[j]):
				out = append(out, "-"+name)
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
		out = appendWord(out, string(rs[i:j]))
		i = j
	}
	return out
}

// appendWord adds one plain word, if it is one worth keeping.
func appendWord(out []string, w string) []string {
	if len([]rune(w)) < 2 || stopwords[w] {
		return out
	}
	return append(out, stem(w))
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

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
	if len(w) <= 3 {
		return w
	}
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
		}
	case strings.HasSuffix(w, "eed"):
		// "need" and "speed" are not need-ed.
	case strings.HasSuffix(w, "ed"):
		if base := w[:len(w)-2]; len(base) >= 2 && hasVowel(base) {
			w = undouble(base)
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
	return w
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
