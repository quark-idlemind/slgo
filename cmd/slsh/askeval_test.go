package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"testing"
)

// askEvalFile is the question set both evals read.  Its head comment
// says what the columns are.
const askEvalFile = "testdata/ask-questions.tsv"

// askEvalQuestion is one line of askEvalFile.
type askEvalQuestion struct {
	Line     int
	Question string
	Want     []string // any of these; empty means slsh cannot do it
	Flags    []string // a right answer uses at least one, when given
}

// answerable is whether slsh has a command for the question at all.
func (q askEvalQuestion) answerable() bool { return len(q.Want) > 0 }

// wants is whether name answers the question.  Names that share a
// command -- an alias and what it stands for -- are the same answer,
// as they are to filterAskAnswer.
func (q askEvalQuestion) wants(name string) bool {
	c, ok := commands[name]
	for _, w := range q.Want {
		if w == name {
			return true
		}
		if wc, wok := commands[w]; ok && wok && wc == c {
			return true
		}
	}
	return false
}

// loadAskEvalQuestions reads askEvalFile, refusing a line it cannot
// read rather than skipping it: a question silently dropped is a
// question the numbers no longer count.
func loadAskEvalQuestions(t testing.TB) []askEvalQuestion {
	t.Helper()
	f, err := os.Open(askEvalFile)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var qs []askEvalQuestion
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		n++
		line := sc.Text()
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) < 2 || len(cols) > 3 {
			t.Fatalf("%s:%d: %d columns; want question, commands and perhaps flags", askEvalFile, n, len(cols))
		}
		q := askEvalQuestion{Line: n, Question: strings.TrimSpace(cols[0])}
		if w := strings.Fields(cols[1]); !(len(w) == 1 && w[0] == "-") {
			q.Want = w
		}
		if len(cols) == 3 {
			q.Flags = strings.Fields(cols[2])
		}
		if q.Question == "" || (len(q.Want) == 0 && strings.TrimSpace(cols[1]) != "-") {
			t.Fatalf("%s:%d: a question and its commands, or -, are both needed", askEvalFile, n)
		}
		qs = append(qs, q)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return qs
}

// The file names only commands there are, and flags those commands
// take, so that a renamed command or flag fails here, by name, rather
// than as a slow drift in the numbers.
func TestAskEvalQuestionsAreWellFormed(t *testing.T) {
	qs := loadAskEvalQuestions(t)
	seen := map[string]int{}
	unanswerable := 0
	for _, q := range qs {
		if prev, ok := seen[q.Question]; ok {
			t.Errorf("%s:%d: asked already on line %d", askEvalFile, q.Line, prev)
		}
		seen[q.Question] = q.Line
		if !q.answerable() {
			unanswerable++
			if len(q.Flags) > 0 {
				t.Errorf("%s:%d: flags for a question nothing answers", askEvalFile, q.Line)
			}
			continue
		}
		for _, w := range q.Want {
			if _, ok := commands[w]; !ok {
				t.Errorf("%s:%d: %q is no command", askEvalFile, q.Line, w)
			}
		}
		for _, fl := range q.Flags {
			ok := false
			for _, w := range q.Want {
				if checkCommandLine(w+" "+fl+askEvalFlagArg(w, fl)) == nil {
					ok = true
				}
			}
			if !ok {
				t.Errorf("%s:%d: none of %v takes %s", askEvalFile, q.Line, q.Want, fl)
			}
		}
	}
	if len(qs) < 60 {
		t.Errorf("%d questions; the set is meant to have at least 60", len(qs))
	}
	if unanswerable*100 < len(qs)*15 {
		t.Errorf("only %d of %d questions have no answer; keep about a fifth", unanswerable, len(qs))
	}
}

// askEvalFlagArg is an argument for fl, when fl needs one, so that the
// well-formedness check above can parse "cmd flag" without a missing
// argument being mistaken for an unknown flag.  It tries the line bare
// first.
func askEvalFlagArg(cmd, fl string) string {
	if checkCommandLine(cmd+" "+fl) == nil {
		return ""
	}
	for _, arg := range []string{" 1", " x"} {
		if checkCommandLine(cmd+" "+fl+arg) == nil {
			return arg
		}
	}
	return ""
}

// askRecallFloor is what TestAskEvalRetrieval holds the index to, as
// fractions of the answerable questions whose command is in the first
// 1, 3 and 8 the index returns.  When this set was written the index
// scored 49, 57 and 63 of its 67 answerable questions (0.731, 0.851,
// 0.940); the floors are each a question below that, so that a change
// that loses one answer passes and a change that loses two fails.
// When the index gets better, raise them; the test logs the measured
// numbers.
var askRecallFloor = [3]float64{0.71, 0.83, 0.92}

// TestAskEvalRetrieval runs every answerable question through askSearch
// and measures how often the command that answers it comes back in the
// first 1, 3 and 8: 8 because that is how many ask shows a model.  The
// questions no command answers are not scored here -- retrieval always
// returns something, and whether that something is right is the model's
// question and the checks'.
func TestAskEvalRetrieval(t *testing.T) {
	qs := loadAskEvalQuestions(t)
	ks := [3]int{1, 3, 8}
	var hits [3]int
	n := 0
	for _, q := range qs {
		if !q.answerable() {
			continue
		}
		n++
		got, err := askSearch(q.Question)
		if err != nil {
			t.Fatal(err)
		}
		rank := -1
		var top []string
		for i, h := range got {
			if rank < 0 && q.wants(h.Command) {
				rank = i
			}
			if i < 8 {
				top = append(top, h.Command)
			}
		}
		for i, k := range ks {
			if rank >= 0 && rank < k {
				hits[i]++
			}
		}
		if rank < 0 || rank >= 3 {
			t.Logf("miss@3 %s:%d %q wants %s, rank %d; first eight %s",
				askEvalFile, q.Line, q.Question, strings.Join(q.Want, "|"), rank+1, strings.Join(top, " "))
		}
	}
	if n == 0 {
		t.Fatal("no answerable questions")
	}
	var parts []string
	for i, k := range ks {
		r := float64(hits[i]) / float64(n)
		parts = append(parts, fmt.Sprintf("recall@%d %.3f (%d/%d)", k, r, hits[i], n))
		if r < askRecallFloor[i] {
			t.Errorf("recall@%d is %.3f, below the floor of %.2f; the misses are logged above", k, r, askRecallFloor[i])
		}
	}
	t.Log(strings.Join(parts, ", "))
}
