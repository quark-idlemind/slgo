package slate

import (
	"bytes"
	"testing"
)

// Options.Out gets every transcript line, and Transcript still holds them all.
func TestOutStreamsTheTranscript(t *testing.T) {
	f := newGrid(t)
	f.on("go", signSays("hello"))
	var out bytes.Buffer
	res := playWith(t, f, hdr+"say \"go\" on 0\nexpect say \"hello\" on public from object sign within 500ms\n", Options{Out: &out}, testCfg())
	if res.Transcript == "" {
		t.Fatal("no transcript to compare")
	}
	if out.String() != res.Transcript {
		t.Errorf("Out:\n%q\nTranscript:\n%q", out.String(), res.Transcript)
	}
}
