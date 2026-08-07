package main

// Remembering paddings, so that the search is paid for once.
//
// # What is being remembered
//
// A padding is a property of the BASE script -- the harness with no
// copies of the code under test in it -- and of nothing else.  That is
// worth stating plainly, because it is what makes this worth having:
// the code being measured is not in the base script at all, so every
// benchmark of a given shape has the same base and wants the same
// answer.  Rendering the base for two different --code values gives
// byte-for-byte the same script.
//
// Finding it costs about a dozen live runs.  Recognising it costs a
// hash, and confirming it costs two.
//
// # What the key is
//
// The rendered base script, with one normalisation: the title is
// replaced by a run of one character of the same length.  A title is a
// string literal, so what it costs is its length rather than its text,
// and without this the title -- which differs for every benchmark --
// would be the one thing keeping otherwise identical bases apart.
//
// If that assumption is ever wrong the answer is confirmed before it is
// used, and a wrong one is thrown away and searched for again.  It
// costs two runs to find out, not a wrong measurement.
//
// # Why a file, and why staleness is not a worry
//
// It is a cache of a computation, not a record of anything in the
// world, so it belongs next to the other things this account keeps
// rather than in slgod.  What SL's compiler does can change under it --
// and the readings themselves are not perfectly repeatable, which is
// why there is a live test in this repository about exactly that -- so
// an entry is never trusted on sight.  It is confirmed on every use,
// which turns twelve runs into two and corrects itself when the answer
// has moved.

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/quark-idlemind/slgo/agent"
)

// padEntry is one remembered answer.
type padEntry struct {
	Padding int
	BaseMem int // what the base script measured, for a person reading the file
}

// baseKey identifies the base script this benchmark will use.
func baseKey() string {
	title := flags.Title
	flags.Title = strings.Repeat("t", len(title))
	src := buildScript(0, minpad)
	flags.Title = title

	sum := sha256.Sum256([]byte(src))
	return hex.EncodeToString(sum[:16])
}

func padCachePath() (string, error) {
	dir, err := agent.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "autobench-padding"), nil
}

// loadPadCache reads what has been remembered.  A missing or unreadable
// file is an empty cache and not an error: nothing here is worth
// failing a benchmark over.
func loadPadCache() map[string]padEntry {
	out := map[string]padEntry{}
	path, err := padCachePath()
	if err != nil {
		return out
	}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		pad, err := strconv.Atoi(f[1])
		if err != nil {
			continue
		}
		e := padEntry{Padding: pad}
		if len(f) > 2 {
			e.BaseMem, _ = strconv.Atoi(f[2])
		}
		out[f[0]] = e
	}
	return out
}

// savePadCache writes it back, through a temporary file so that an
// interrupted write cannot leave a half-written cache behind.
func savePadCache(m map[string]padEntry) {
	path, err := padCachePath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}

	var b strings.Builder
	b.WriteString("# autobench: base script hash, padding, base memory\n")
	for k, e := range m {
		fmt.Fprintf(&b, "%s %d %d\n", k, e.Padding, e.BaseMem)
	}

	tmp := path + ".new"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return
	}
	os.Rename(tmp, path)
}

func rememberPadding(key string, pad, baseMem int) {
	m := loadPadCache()
	m[key] = padEntry{Padding: pad, BaseMem: baseMem}
	savePadCache(m)
}

func forgetPadding(key string) {
	m := loadPadCache()
	if _, ok := m[key]; !ok {
		return
	}
	delete(m, key)
	savePadCache(m)
}
