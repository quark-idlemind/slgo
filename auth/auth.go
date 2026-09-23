// Package auth authenticates gRPC callers, in both directions, before
// they may run anything.
//
// The exchange is two calls of one RPC:
//
//	client -> server   (empty)
//	server -> client   SCHAL, 16 random bytes
//	client -> server   CCHAL, HMAC(secret, SCHAL || "CLIENT")
//	server -> client   HMAC(secret, CCHAL || "SERVER"), and a session token
//
// The secret never crosses the wire in either direction, and each
// challenge is random, single-use and short-lived, so a captured proof
// is worth nothing afterwards.
//
// MUTUAL is the point. The previous scheme proved the client knew the
// secret and said nothing about the server, so a client would happily
// authenticate itself to anything listening on the port -- and then send
// it scripts. Now neither side proceeds until the other has shown it
// knows the secret too.
//
// The "CLIENT" and "SERVER" tags are what make it mutual rather than
// merely two-sided. Without them both proofs are the same function of a
// 16-byte input, so an impostor could take the real server's challenge,
// offer it back as its own CCHAL, and have the answer it needed computed
// for it. The tags make one side's proof useless as the other's.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ChallengeSize is how many random bytes each side offers.
const ChallengeSize = 16

// ProofSize is how long a proof is: one HMAC-SHA256.
//
// Named so that a proof of any other length can be refused on sight.
// It arrives before the caller has proved anything, and the only
// length that could ever verify is this one.
const ProofSize = sha256.Size

// Tags separate the two directions. They are part of the protocol; both
// ends must use the same words.
const (
	tagClient = "CLIENT"
	tagServer = "SERVER"
)

// ErrNoSecret reports a server with authentication enabled but nothing
// to authenticate against.
var ErrNoSecret = errors.New("no shared secret configured")

// ErrDenied is what a caller is told when anything about the handshake
// is wrong.
//
// One error for every failure on purpose. Which half failed, and whether
// a challenge was unknown or merely expired, are all facts an attacker
// would like and a legitimate client does not need: it either has the
// secret or it does not.
var ErrDenied = errors.New("authentication failed")

// Proof is the value one side sends to show it knows the secret, on this
// connection.
//
// The binding is keying material exported from the TLS session, and it
// is what makes the exchange mean anything over a channel nobody has
// verified. Without it, an attacker terminating TLS on both sides can
// relay every message of the handshake untouched -- the proofs still
// verify, because none of them mentions the connection -- and then read
// and rewrite everything afterwards. Both ends would conclude they had
// authenticated each other, and they would be right, just not to each
// other.
//
// With it, the two halves of a relayed connection are different TLS
// sessions with different exported material, so the proof computed for
// one does not verify on the other.
func Proof(secret string, challenge []byte, tag string, binding []byte) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(challenge)
	mac.Write([]byte(tag))
	mac.Write(binding)
	return mac.Sum(nil)
}

// ClientProof answers the server's challenge.
func ClientProof(secret string, serverChallenge, binding []byte) []byte {
	return Proof(secret, serverChallenge, tagClient, binding)
}

// ServerProof answers the client's challenge.
func ServerProof(secret string, clientChallenge, binding []byte) []byte {
	return Proof(secret, clientChallenge, tagServer, binding)
}

// BindingLabel is the exporter label both ends must use. It is part of
// the protocol.
const BindingLabel = "EXPERIMENTAL slrund channel binding"

// BindingLength is how many bytes of exported keying material to mix in.
const BindingLength = 32

type pending struct {
	challenge []byte
	client    string
	expires   time.Time
}

// Server issues challenges, answers them, and tracks sessions.
type Server struct {
	secret string

	// ChallengeTTL bounds how long a challenge may go unanswered. Short
	// on purpose: it is the window in which a captured proof could be
	// replayed.
	ChallengeTTL time.Duration

	mu       sync.Mutex
	pendings []*pending
}

// New returns a Server keyed on the shared secret.
func New(secret string) (*Server, error) {
	if secret == "" {
		return nil, ErrNoSecret
	}
	return &Server{secret: secret, ChallengeTTL: time.Minute}, nil
}

// maxPending caps outstanding challenges.
//
// Begin is the only unauthenticated entry point, so it is where an
// attacker could otherwise make the server hold arbitrarily much -- and
// where Answer's search would otherwise become arbitrarily long.
const maxPending = 256

// Begin opens a handshake and returns the server's challenge.
//
// The client's name is kept with the challenge until it is answered,
// and is cut to NameSize first.  The wire cannot carry more (see
// PackName), so this is for any other caller: whatever is kept for
// somebody who has proved nothing must have a size this end chose.
func (s *Server) Begin(client string) ([]byte, error) {
	client = clipName(client)
	challenge := make([]byte, ChallengeSize)
	if _, err := rand.Read(challenge); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked()
	if len(s.pendings) >= maxPending {
		// Drop the oldest rather than refuse: a flood must not lock out
		// the legitimate caller arriving in the middle of it.
		s.pendings = s.pendings[1:]
	}
	s.pendings = append(s.pendings, &pending{
		challenge: challenge, client: client,
		expires: time.Now().Add(s.ChallengeTTL),
	})
	return challenge, nil
}

// Answer checks the client's proof and, if it holds, returns the
// server's own proof and a session token.
//
// The matching challenge is found by trying each outstanding one. The
// wire carries no identifier -- the protocol is the client's proof and
// nothing else -- and there are never many outstanding, so searching is
// both simplest and what keeps the exchange to two messages.
//
// Whichever challenge matches is consumed. A challenge is one attempt,
// so a wrong guess cannot be retried against the same one.
func (s *Server) Answer(clientChallenge, clientProof, binding []byte) (serverProof []byte, client string, err error) {
	if len(clientChallenge) != ChallengeSize || len(clientProof) != ProofSize {
		return nil, "", ErrDenied
	}

	s.mu.Lock()
	s.expireLocked()
	found := -1
	for i, p := range s.pendings {
		want := ClientProof(s.secret, p.challenge, binding)
		if subtle.ConstantTimeCompare(want, clientProof) == 1 {
			found = i
			break
		}
	}
	if found < 0 {
		s.mu.Unlock()
		return nil, "", ErrDenied
	}
	who := s.pendings[found].client
	s.pendings = append(s.pendings[:found], s.pendings[found+1:]...)
	s.mu.Unlock()

	return ServerProof(s.secret, clientChallenge, binding), who, nil
}

// expireLocked drops what has timed out.
func (s *Server) expireLocked() {
	now := time.Now()
	keep := s.pendings[:0]
	for _, p := range s.pendings {
		if now.Before(p.expires) {
			keep = append(keep, p)
		}
	}
	s.pendings = keep
}

// DefaultSecretPath is where the shared secret lives.
//
// ONE file for the whole lab, shared by every service that uses this
// package. Two secrets would be two things to keep in step for no gain:
// anyone holding one already reaches the machine the other runs on.
func DefaultSecretPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "slrun", "secret")
}

// LoadSecret reads the shared secret, refusing a file others can read.
// It is a password: a mode that lets anyone else on the machine take it
// is worth failing over, the same way ssh refuses a group-readable
// private key.
func LoadSecret(path string) (string, error) {
	if path == "" {
		path = DefaultSecretPath()
	}
	dir := filepath.Dir(path)
	if err := checkMode(dir, 0o700); err != nil {
		return "", err
	}
	if err := checkMode(path, 0o600); err != nil {
		return "", err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	secret := trimSecret(string(b))
	if secret == "" {
		return "", fmt.Errorf("%s is empty", path)
	}
	warnIfShort(path, secret)
	return secret, nil
}

// MinSecretLength is the shortest secret LoadSecret takes without
// complaint.
//
// The handshake never shows the secret to anybody, but a wrong proof
// costs a guesser nothing but a round trip and a secret short enough to
// type is short enough to be found that way.  Made as the guide says,
// with openssl rand -hex 32, it is 64 characters and 256 bits, and no
// rate of guessing reaches it.
const MinSecretLength = 16

// Warnf is where a warning about the secret goes.  A variable so that a
// test can hear it.
var Warnf = log.Printf

var (
	warnedMu sync.Mutex
	warned   = map[string]bool{}
)

// warnIfShort says, once per file per process, that a secret is short
// enough to guess.
//
// A warning rather than a refusal on purpose.  Refusing would stop a
// daemon that has run for months on the secret it has from starting at
// all the next time it restarts, which is a worse thing to do to
// somebody than to tell them.  Once per file, because a client that
// reconnects reads the file again every time and would otherwise say
// the same thing on every reconnection.
func warnIfShort(path, secret string) {
	if len(secret) >= MinSecretLength {
		return
	}
	warnedMu.Lock()
	seen := warned[path]
	warned[path] = true
	warnedMu.Unlock()
	if seen {
		return
	}
	Warnf("WARNING: the shared secret in %s is %d bytes, which is short enough to guess; "+
		"at least %d is wanted.  Make a new one with:  "+
		"(umask 077; openssl rand -hex 32 > %s)  and restart slgod and its clients",
		path, len(secret), MinSecretLength, path)
}

func checkMode(path string, want os.FileMode) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if got := fi.Mode().Perm(); got&^want != 0 {
		return fmt.Errorf("%s has mode %04o, must be %04o -- it holds the shared secret; chmod %04o %s",
			path, got, want, want, path)
	}
	return nil
}

// trimSecret drops surrounding whitespace and ignores comment lines, so
// the file can carry a note about what it is for.
func trimSecret(s string) string {
	for _, line := range splitLines(s) {
		line = trimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		return line
	}
	return ""
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	return append(lines, s[start:])
}

func trimSpace(s string) string {
	i, j := 0, len(s)
	for i < j && (s[i] == ' ' || s[i] == '\t' || s[i] == '\r') {
		i++
	}
	for j > i && (s[j-1] == ' ' || s[j-1] == '\t' || s[j-1] == '\r') {
		j--
	}
	return s[i:j]
}
