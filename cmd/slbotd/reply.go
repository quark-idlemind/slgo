package main

// Sending an answer back as instant messages.
//
// An instant message is not a terminal.  It carries about a kilobyte,
// it arrives in a conversation window somebody is reading, and a
// hundred of them in a row is a flood the grid will throttle and the
// person will not thank anybody for.  So an answer is composed in full
// first -- every command writes to an io.Writer for that reason -- and
// then cut into as few messages as it fits in, up to a limit, with the
// rest said to be missing rather than silently dropped.
//
// The cut is made at a line ending where there is one, at a space where
// there is not, and between runes in the worst case.  Never inside a
// rune: the message field is bytes and a half-written rune arrives as a
// replacement character in somebody's chat window.

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// IMBytes is how much of a message is sent.
//
// The simulator's own limit is 1023 bytes and what goes over it is cut
// off without a word.  The margin is for the name and the decoration a
// viewer puts around a message, which are not part of this count but
// are part of what somebody reads.
const IMBytes = 1000

// IMGap is the pause between two messages of one answer.
//
// Not politeness: the simulator throttles chat and instant messages per
// agent, and a burst sent as fast as the circuit takes them arrives
// with the tail missing.  Small enough that a four-part answer is still
// one thought.
const IMGap = 250 * time.Millisecond

// sendReply sends an answer back to whoever asked, in as many messages
// as it takes and no more than the limit allows.
//
// The limit is on messages rather than on characters because messages
// are what the far end counts: five hundred lines and five hundred
// characters are the same nuisance if they arrive as forty windows
// full.  What is cut off is said, with how much, so that the person can
// ask for less rather than wonder whether the answer ended.
func sendReply(ctx context.Context, s *sl.Session, to msg.UUID, text string, limit int) error {
	parts := splitReply(text, IMBytes)
	if len(parts) == 0 {
		return nil
	}
	if limit < 1 {
		limit = 1
	}
	cut := false
	if len(parts) > limit {
		parts = parts[:limit]
		cut = true
	}
	for i, p := range parts {
		if i > 0 {
			select {
			case <-time.After(IMGap):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if err := s.SendIM(ctx, to, p); err != nil {
			return err
		}
	}
	if cut {
		select {
		case <-time.After(IMGap):
		case <-ctx.Done():
			return ctx.Err()
		}
		return s.SendIM(ctx, to,
			fmt.Sprintf("...that is the first %d messages of the answer and there was more; "+
				"ask for less of it.", limit))
	}
	return nil
}

// splitReply cuts text into pieces of at most n bytes each.
//
// Trailing whitespace goes, and an answer that is nothing but
// whitespace comes back as no pieces at all rather than as an empty
// message: a command that succeeded and had nothing to say should say
// nothing, and "ok" is what the caller adds when silence would be
// ambiguous.
func splitReply(text string, n int) []string {
	text = strings.TrimRight(text, " \t\r\n")
	if text == "" {
		return nil
	}
	if n < 1 {
		n = 1
	}

	var out []string
	for len(text) > 0 {
		if len(text) <= n {
			out = append(out, text)
			break
		}
		cut := breakAt(text, n)
		piece := strings.TrimRight(text[:cut], " \t\r\n")
		if piece != "" {
			out = append(out, piece)
		}
		// Only the line endings come off the front of what is left.
		// Not the spaces: a listing is indented, and a piece that
		// began at a line but had its indent trimmed away starts with
		// one row out of line with the rest -- which is exactly what
		// this did, and it showed up the first time a help listing ran
		// to three messages.  A cut at a space has already stepped
		// over it, so there is nothing there to trim either.
		text = strings.TrimLeft(text[cut:], "\r\n")
	}
	return out
}

// breakAt is where to cut a string that is longer than n bytes.
//
// A newline is the best place and a space the next best, both looked
// for backwards from the limit so that the piece is as full as it can
// be.  Failing either -- one long line with no spaces in it, which is a
// uuid listing or a script -- it falls back to the last whole rune
// inside the limit, which is the one place the cut is allowed to be
// arbitrary and is never allowed to be inside a rune.
func breakAt(s string, n int) int {
	if i := strings.LastIndexByte(s[:n], '\n'); i >= 0 {
		return i + 1
	}
	if i := strings.LastIndexByte(s[:n], ' '); i > 0 {
		return i + 1
	}
	// Back up to a rune boundary.  n itself may be in the middle of
	// one, and so may every byte between it and the start of that rune.
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	if cut == 0 {
		// One rune longer than the whole limit.  Send it anyway rather
		// than loop forever sending nothing.
		_, size := utf8.DecodeRuneInString(s)
		return size
	}
	return cut
}
