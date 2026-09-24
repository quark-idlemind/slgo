//go:build askeval

package main

// The model half of ask's evaluation: every question in askEvalFile
// asked of a real model, through askRun, exactly as the command asks
// it.  It needs a server and takes minutes, so it is built only with
// the askeval tag and runs only when a server is named:
//
//	SLGO_ASK_EVAL_URL=http://127.0.0.1:11434 \
//	SLGO_ASK_EVAL_MODELS=qwen3:4b,gemma3:4b \
//	go test -tags askeval ./cmd/slsh -run TestAskEvalModels -v -timeout 2h
//
// SLGO_ASK_EVAL_URL     the OpenAI-compatible server (Ollama, llama-server)
// SLGO_ASK_EVAL_MODELS  comma-separated model names; each is evaluated
// SLGO_ASK_EVAL_SLOT    optional llama-server slot
// SLGO_ASK_EVAL_EXTRA   optional JSON object merged into every request
//                       body (llm.Options.Extra) -- for a server's own
//                       switches, such as one that turns thinking off
// SLGO_ASK_EVAL_TIMEOUT optional per-question timeout, as time.ParseDuration
//                       reads it (default 2m)
// SLGO_ASK_EVAL_OUT     optional file the table is also written to
//
// Nothing here asserts a number: the point is to compare models, and
// which model is good enough is a decision rather than a test.  What
// fails the test is a question that could not be asked at all.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/internal/llm"
)

// askEvalScore is one model's totals.
type askEvalScore struct {
	Model string

	Answerable   int // questions a command answers
	Right        int // ... whose first kept suggestion runs one of them
	RightFlag    int // ... and, where the question names flags, uses one
	FlagAsked    int // answerable questions that name flags
	Unanswerable int // questions nothing answers
	NotFound     int // ... for which nothing was kept

	Rejected  int // questions with any suggestion the checks refused
	WrongKept int // questions with a kept suggestion that runs a wrong command
	Retried   int
	Errors    int // askRun failed, or no model reply came back

	elapsed []time.Duration
	tokens  []int
}

func (s *askEvalScore) total() int { return s.Answerable + s.Unanswerable }

func TestAskEvalModels(t *testing.T) {
	url := os.Getenv("SLGO_ASK_EVAL_URL")
	models := strings.Split(os.Getenv("SLGO_ASK_EVAL_MODELS"), ",")
	if url == "" || strings.TrimSpace(models[0]) == "" {
		t.Skip("SLGO_ASK_EVAL_URL and SLGO_ASK_EVAL_MODELS name the server and the models to evaluate")
	}
	opts := llm.Options{URL: url, Temperature: 0, Timeout: 2 * time.Minute}
	if v := os.Getenv("SLGO_ASK_EVAL_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			t.Fatalf("SLGO_ASK_EVAL_TIMEOUT: %v", err)
		}
		opts.Timeout = d
	}
	if v := os.Getenv("SLGO_ASK_EVAL_SLOT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			t.Fatalf("SLGO_ASK_EVAL_SLOT: %v", err)
		}
		opts.Slot = &n
	}
	if v := os.Getenv("SLGO_ASK_EVAL_EXTRA"); v != "" {
		if err := json.Unmarshal([]byte(v), &opts.Extra); err != nil {
			t.Fatalf("SLGO_ASK_EVAL_EXTRA is not a JSON object: %v", err)
		}
	}

	qs := loadAskEvalQuestions(t)
	var scores []*askEvalScore
	for _, m := range models {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		o := opts
		o.Model = m
		scores = append(scores, askEvalModel(t, llm.New(o), m, qs, opts.Timeout))
	}

	table := askEvalTable(scores)
	t.Log("\n" + table)
	if out := os.Getenv("SLGO_ASK_EVAL_OUT"); out != "" {
		if err := os.WriteFile(out, []byte(table), 0o644); err != nil {
			t.Error(err)
		}
	}
}

// askEvalModel asks one model every question.  Each question's outcome
// is logged, so that -v shows what a model got wrong and not only how
// often.
func askEvalModel(t *testing.T, client *llm.Client, model string, qs []askEvalQuestion, timeout time.Duration) *askEvalScore {
	s := &askEvalScore{Model: model}
	for _, q := range qs {
		if q.answerable() {
			s.Answerable++
			if len(q.Flags) > 0 {
				s.FlagAsked++
			}
		} else {
			s.Unanswerable++
		}

		// The question's own deadline covers a retry too; the client's
		// timeout is per request.
		ctx, cancel := context.WithTimeout(context.Background(), 2*timeout+10*time.Second)
		// Asked the way the command asks it: "how" and the rest of the
		// line (howQuestion), so that what is measured is what somebody
		// typing the question would get.
		r, err := askRun(ctx, client, howQuestion(strings.Fields(q.Question)), nil)
		cancel()
		if err != nil || r == nil || r.Answer == nil {
			s.Errors++
			t.Logf("%s %s:%d %q: no answer: %v", model, askEvalFile, q.Line, q.Question, err)
			continue
		}
		s.elapsed = append(s.elapsed, r.Elapsed)
		if r.PromptTokens > 0 {
			s.tokens = append(s.tokens, r.PromptTokens)
		}
		if r.Retried {
			s.Retried++
		}
		if len(r.Rejected) > 0 {
			s.Rejected++
		}

		verdict := "right"
		wrong := false
		for _, k := range r.Kept {
			if !q.wants(askFirstWord(k.CommandLine)) {
				wrong = true
			}
		}
		if wrong {
			s.WrongKept++
		}
		switch {
		case !q.answerable() && len(r.Kept) == 0:
			s.NotFound++
		case !q.answerable():
			verdict = "wrong: suggested something for a question nothing answers"
		case len(r.Kept) == 0:
			verdict = "wrong: nothing kept"
		case !q.wants(askFirstWord(r.Kept[0].CommandLine)):
			verdict = "wrong: first kept runs " + askFirstWord(r.Kept[0].CommandLine)
		default:
			s.Right++
			if len(q.Flags) > 0 {
				if askEvalUsesFlag(r.Kept[0].CommandLine, q.Flags) {
					s.RightFlag++
				} else {
					verdict = "right command, none of the flags " + strings.Join(q.Flags, " ")
				}
			}
		}

		var kept []string
		for _, k := range r.Kept {
			kept = append(kept, k.CommandLine)
		}
		t.Logf("%s %s:%d %q: %s; kept [%s], %d rejected, retried %v, found %v, %v",
			model, askEvalFile, q.Line, q.Question, verdict,
			strings.Join(kept, " | "), len(r.Rejected), r.Retried, r.Answer.Found, r.Elapsed.Round(time.Millisecond))
		for _, rj := range r.Rejected {
			t.Logf("    rejected %q: %s", rj.Suggestion.CommandLine, strings.Join(rj.Reasons, "; "))
		}
	}
	if s.Errors == s.total() {
		t.Errorf("%s: not one question was answered; is the model served at the URL?", model)
	}
	return s
}

// askEvalUsesFlag is whether line uses any of flags.  A short flag may
// be bundled with others ("-lr") and a long one given with =VALUE.
func askEvalUsesFlag(line string, flags []string) bool {
	words := strings.Fields(line)
	if len(words) < 2 {
		return false
	}
	for _, w := range words[1:] {
		for _, f := range flags {
			switch {
			case w == f, strings.HasPrefix(w, f+"="):
				return true
			case len(f) == 2 && f[0] == '-' && f[1] != '-' &&
				len(w) > 2 && w[0] == '-' && w[1] != '-' && strings.ContainsRune(w[1:], rune(f[1])):
				return true
			}
		}
	}
	return false
}

// askEvalTable is the comparison, one row a model.  Rates are of the
// questions they are about: right and flag of the answerable, not-found
// of the unanswerable, and the rest of every question asked.
func askEvalTable(scores []*askEvalScore) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-24s %7s %7s %9s %8s %9s %7s %6s %8s %8s %7s\n",
		"model", "right", "flag", "notfound", "rejected", "wrongkept", "retry", "errors", "mean", "p90", "prompt")
	for _, s := range scores {
		n := s.total()
		fmt.Fprintf(&b, "%-24s %7s %7s %9s %8s %9s %7s %6d %8s %8s %7s\n",
			s.Model,
			askEvalPct(s.Right, s.Answerable),
			askEvalPct(s.RightFlag, s.FlagAsked),
			askEvalPct(s.NotFound, s.Unanswerable),
			askEvalPct(s.Rejected, n),
			askEvalPct(s.WrongKept, n),
			askEvalPct(s.Retried, n),
			s.Errors,
			askEvalMean(s.elapsed).Round(10*time.Millisecond),
			askEvalP90(s.elapsed).Round(10*time.Millisecond),
			askEvalMeanInt(s.tokens))
	}
	fmt.Fprintf(&b, "\nright: first kept suggestion runs an expected command (of answerable)\n"+
		"flag: ... and uses an expected flag (of answerable questions naming flags)\n"+
		"notfound: nothing kept (of unanswerable)\n"+
		"rejected: a suggestion the checks refused; wrongkept: a kept suggestion running a wrong command (of all)\n"+
		"mean/p90: askRun's elapsed time; prompt: mean prompt tokens as the server counted them\n")
	return b.String()
}

func askEvalPct(n, of int) string {
	if of == 0 {
		return "-"
	}
	return fmt.Sprintf("%.0f%%", 100*float64(n)/float64(of))
}

func askEvalMean(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	var sum time.Duration
	for _, d := range ds {
		sum += d
	}
	return sum / time.Duration(len(ds))
}

func askEvalP90(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[(len(s)*9+9)/10-1]
}

func askEvalMeanInt(ns []int) string {
	if len(ns) == 0 {
		return "-"
	}
	sum := 0
	for _, n := range ns {
		sum += n
	}
	return strconv.Itoa(sum / len(ns))
}
