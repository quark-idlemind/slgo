package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/internal/askindex"
	"github.com/quark-idlemind/slgo/internal/llm"
)

// A suggestion the model itself says is no answer is not printed, even
// though it passes every check: found is the model's decision, and the
// suggestions it wrote beside it are not an answer.
func TestAskRunHonoursFoundFalse(t *testing.T) {
	f := &fakeModel{replies: []string{askJSON(t, false, askSetHome)}}
	res, err := askRun(context.Background(), newAskClient(askConfig{URL: f.serve(t).URL}, nil), askHomeQuestion, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Kept) != 0 || len(res.Rejected) != 0 || res.Retried {
		t.Errorf("kept %+v, rejected %+v, retried %v", res.Kept, res.Rejected, res.Retried)
	}
	if len(f.bodies) != 1 {
		t.Errorf("%d requests; a not-found answer needs no second question", len(f.bodies))
	}
}

// A line that leaves the command out is given the command the
// suggestion names, and nothing else is changed.
func TestAskMendLine(t *testing.T) {
	for _, tc := range []struct{ command, line, want string }{
		{"landmark", "--home", "landmark --home"},
		{"landmark", "`--go PLACE`", "landmark --go PLACE"},
		{"find", "slsh find lamp", "find lamp"},
		{"landmark", "landmark --home", "landmark --home"},
		{"landmark", "tp home", "tp home"},    // a different command is the checker's to refuse
		{"", "--home", "--home"},              // no command named, nothing to add
		{"nosuchcommand", "--home", "--home"}, // nor a command that is not one
		{"landmark", "NAME", "NAME"},          // only a line that starts at its flags
	} {
		got := askMendLine(askSuggestion{Command: tc.command, CommandLine: tc.line})
		if got != tc.want {
			t.Errorf("%q %q: got %q, want %q", tc.command, tc.line, got, tc.want)
		}
	}

	// And askRun keeps what the mended line is.
	s := askSetHome
	s.CommandLine = "--set-home"
	f := &fakeModel{replies: []string{askJSON(t, true, s)}}
	res, err := askRun(context.Background(), newAskClient(askConfig{URL: f.serve(t).URL}, nil), askHomeQuestion, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Kept) != 1 || res.Kept[0].CommandLine != "landmark --set-home" {
		t.Errorf("kept %+v, rejected %+v", res.Kept, res.Rejected)
	}
}

// how's own page is never a candidate, whatever the question.
func TestAskRunNeverOffersHowItself(t *testing.T) {
	if !askSkip(howName) || askSkip("landmark") {
		t.Errorf("askSkip(%q) = %v, askSkip(landmark) = %v", howName, askSkip(howName), askSkip("landmark"))
	}
	for _, q := range []string{"how do I ask which command does something", howQuestion([]string{"ask", "a", "question"})} {
		res, err := askRun(context.Background(), nil, q, nil)
		if err != nil {
			t.Fatal(err)
		}
		if askHasCandidate(res.Candidates, howName) {
			t.Errorf("%q: %s was a candidate", q, howName)
		}
	}
}

// A hint may name a command by its other name, and raises the command
// under the name the index knows it by.
func TestAskHintsTakeASecondName(t *testing.T) {
	if commands["exit"] != commands["quit"] || commands["unsit"] != commands["stand"] {
		t.Fatal("the test's pairs of names are no longer one command each")
	}
	path := filepath.Join(t.TempDir(), "hints.tsv")
	os.WriteFile(path, []byte("go away\texit quit\nget up\tunsit\n"), 0o644)
	h, unknown, err := readAskHints(path)
	if err != nil || len(unknown) != 0 {
		t.Fatal(err, unknown)
	}
	quit, _ := askIndexName("quit")
	stand, _ := askIndexName("stand")
	if len(h) != 2 || strings.Join(h[0].Commands, " ") != quit || strings.Join(h[1].Commands, " ") != stand {
		t.Errorf("hints %+v, want [%s] and [%s]", h, quit, stand)
	}

	// And it works: the hint's words bring the command up.
	res, err := askRun(context.Background(), nil, "zqxv", []askindex.Hint{{When: []string{"zqxv"}, Commands: h[1].Commands}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Candidates) == 0 || commands[res.Candidates[0].Name] != commands["unsit"] {
		t.Errorf("candidates %+v", res.Candidates)
	}
}

// The timeout is for the whole question: an answer and a retry that
// each fit it alone do not, together, get twice as long.
func TestAskRunTimeoutCoversTheRetry(t *testing.T) {
	bad := askSetHome
	bad.CommandLine = "landmark --make-home"
	f := &fakeModel{replies: []string{askJSON(t, true, bad), askJSON(t, true, askSetHome)}}
	inner := f.serve(t)
	const each = 300 * time.Millisecond
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(each):
		case <-r.Context().Done():
			return
		}
		resp, err := http.Post(inner.URL+r.URL.Path, "application/json", r.Body)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		buf := make([]byte, 1<<16)
		for {
			n, err := resp.Body.Read(buf)
			w.Write(buf[:n])
			if err != nil {
				return
			}
		}
	}))
	t.Cleanup(slow.Close)

	client := newAskClient(askConfig{URL: slow.URL, Timeout: each + each/2}, nil)
	if client.Timeout() != each+each/2 {
		t.Fatalf("timeout %v", client.Timeout())
	}
	start := time.Now()
	res, err := askRun(context.Background(), client, askHomeQuestion, nil)
	if !errors.Is(err, llm.ErrTimeout) {
		t.Errorf("err %v, want the retry cut off by the question's timeout", err)
	}
	if res == nil || !res.Retried {
		t.Errorf("res %+v: the first answer should have come back and been retried", res)
	}
	if d := time.Since(start); d > 2*each {
		t.Errorf("took %v; the timeout was %v", d, each+each/2)
	}
}
