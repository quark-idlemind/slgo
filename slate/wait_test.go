package slate

// wait: a step that sends nothing and hears everything. The transcript of
// a wait is what the run heard while it lasted.
// Why: doc/slate-language.md#wait

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// stamped is a live transcript that notes when each line was written.
type stamped struct {
	mu    sync.Mutex
	lines []string
	at    []time.Time
}

func (s *stamped) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = append(s.lines, strings.TrimRight(string(p), "\n"))
	s.at = append(s.at, time.Now())
	return len(p), nil
}

// when is when the first line containing want was written.
func (s *stamped) when(t *testing.T, want string) time.Time {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, l := range s.lines {
		if strings.Contains(l, want) {
			return s.at[i]
		}
	}
	t.Fatalf("no line written containing %q: %q", want, s.lines)
	return time.Time{}
}

// order fails unless each of want is a line of the transcript, in this order.
func order(t *testing.T, res *Result, want ...string) {
	t.Helper()
	ls := lines(res)
	at := 0
	for _, w := range want {
		found := false
		for ; at < len(ls); at++ {
			if strings.Contains(ls[at], w) {
				found = true
				at++
				break
			}
		}
		if !found {
			t.Errorf("no line containing %q after the one before it:\n%s", w, res.Transcript)
			return
		}
	}
}

func TestAWaitPrintsWhatIsSaidDuringItAsItIsSaid(t *testing.T) {
	f := newGrid(t)
	f.whenSaid("go", func() {
		go func() {
			for _, m := range []msg.Message{
				chatMsg("Example Sign", idSign, sl.ChatOwner, "owner one"),
				chatMsg("Example Sign", idSign, sl.ChatSay, "public two"),
				chatMsg("Example Tip Jar", idVendor, sl.ChatDebug, "debug three"),
				chatMsg("Example Tip Jar", idVendor, sl.ChatOwner, "owner four"),
				chatMsg("Example Stray Box", idStray, sl.ChatWhisper, "whisper five"),
				chatMsg("Example Stray Box", idStray, sl.ChatRegion, "region six"),
			} {
				time.Sleep(60 * time.Millisecond)
				f.relay(m)
			}
		}()
	})
	out := &stamped{}
	res := playWith(t, f, hdr+"say \"go\" on 0\nwait 800ms\n", Options{Out: out}, testCfg())
	wantExit(t, res, 0)
	order(t, res,
		`chat owner from sign: "owner one"`,
		`chat public from sign: "public two"`,
		`chat debug from vendor: "debug three"`,
		`chat owner from vendor: "owner four"`,
		`chat public from [Object] Example Stray Box: "whisper five"`,
		`chat region from [Object] Example Stray Box: "region six"`)
	// They were written when they came, well before the wait ended.
	first := out.when(t, `"owner one"`)
	end := out.when(t, "slate: pass step 2")
	if end.Sub(first) < 300*time.Millisecond {
		t.Errorf("the first line was written %s before the wait ended; a wait of 800ms prints as it hears", end.Sub(first))
	}
}

func TestAWaitDoesNotPrintAProtocolLineAsChat(t *testing.T) {
	f, o := world(t)
	f.whenSaid("go", func() {
		go func() {
			time.Sleep(80 * time.Millisecond)
			o.mu.Lock()
			b, w := o.bridge, o.worn
			o.mu.Unlock()
			// The bridge saying it is ready again is protocol, not chat.
			o.emit(chatMsg("slate bridge", w.Object.ID, sl.ChatOwner, wireVersion+" "+b.nonce+" ready"))
			time.Sleep(80 * time.Millisecond)
			o.productSays(7, idSign, "Example Sign", "heard on seven")
			time.Sleep(80 * time.Millisecond)
			f.relay(signSays("plain"))
		}()
	})
	res := o.play(t, probeHdr+"say \"go\" on 0\nwait 600ms\n")
	wantExit(t, res, 0)
	order(t, res, `chat channel 7 from sign: "heard on seven"`, `chat public from sign: "plain"`)
	mustNotHave(t, res, "slate bridge")
	mustNotHave(t, res, "ready")
}
