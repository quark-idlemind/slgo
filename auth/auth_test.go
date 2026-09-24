package auth

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const secret = "shared-secret"

// bind stands in for the TLS channel binding, which every proof is
// computed over.
var bind = []byte("a-tls-channel-binding-of-32-bytes")

func newServer(t *testing.T) *Server {
	t.Helper()
	s, err := New(secret)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func challenge(t *testing.T) []byte {
	t.Helper()
	b := make([]byte, ChallengeSize)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestHandshake(t *testing.T) {
	s := newServer(t)

	schal, err := s.Begin("test")
	if err != nil {
		t.Fatal(err)
	}
	if len(schal) != ChallengeSize {
		t.Fatalf("challenge is %d bytes, want %d", len(schal), ChallengeSize)
	}

	cchal := challenge(t)
	proof, who, err := s.Answer(cchal, ClientProof(secret, schal, bind), bind)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(proof, ServerProof(secret, cchal, bind)) {
		t.Error("the server's proof does not answer the client's challenge")
	}
	if who != "test" {
		t.Errorf("caller reported as %q, want test", who)
	}
}

// The reason the two tags exist.
//
// Without them both proofs are the same function of a 16 byte input, so
// an impostor with no secret could open a connection to the real server,
// pass the challenge it was given straight back as its own CCHAL, and
// receive exactly the value it needed to answer with. This checks the
// two directions produce different answers for the same input, which is
// what makes that useless.
func TestReflectionIsUseless(t *testing.T) {
	c := challenge(t)
	if bytes.Equal(ClientProof(secret, c, bind), ServerProof(secret, c, bind)) {
		t.Fatal("client and server proofs are identical, so a challenge can be reflected")
	}

	// Concretely: the server's answer to a challenge is not a valid
	// client proof for that same challenge.
	s := newServer(t)
	schal, _ := s.Begin("impostor")
	if _, _, err := s.Answer(challenge(t), ServerProof(secret, schal, bind), bind); err == nil {
		t.Fatal("a reflected server proof was accepted as a client proof")
	}
}

func TestWrongSecretRejected(t *testing.T) {
	s := newServer(t)
	schal, _ := s.Begin("test")
	if _, _, err := s.Answer(challenge(t), ClientProof("wrong", schal, bind), bind); err == nil {
		t.Fatal("a proof made with the wrong secret was accepted")
	}
}

// A client must be able to tell a real server from one that merely
// answers, which is the half the previous scheme did not have.
func TestClientCanDetectAWrongServer(t *testing.T) {
	cchal := challenge(t)
	honest := ServerProof(secret, cchal, bind)
	impostor := ServerProof("some-other-secret", cchal, bind)
	if bytes.Equal(honest, impostor) {
		t.Fatal("a server with the wrong secret produced the right proof")
	}
}

// A challenge is one attempt: a wrong guess must not leave it usable.
func TestChallengeIsSingleUse(t *testing.T) {
	s := newServer(t)
	schal, _ := s.Begin("test")
	good := ClientProof(secret, schal, bind)

	if _, _, err := s.Answer(challenge(t), good, bind); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Answer(challenge(t), good, bind); err == nil {
		t.Fatal("the same proof was accepted twice")
	}
}

func TestChallengeExpires(t *testing.T) {
	s := newServer(t)
	s.ChallengeTTL = time.Millisecond
	schal, _ := s.Begin("test")
	time.Sleep(10 * time.Millisecond)
	if _, _, err := s.Answer(challenge(t), ClientProof(secret, schal, bind), bind); err == nil {
		t.Fatal("an expired challenge was accepted")
	}
}

func TestConcurrentHandshakesDoNotCross(t *testing.T) {
	s := newServer(t)
	const n = 20

	challenges := make([][]byte, n)
	for i := range challenges {
		c, err := s.Begin("test")
		if err != nil {
			t.Fatal(err)
		}
		challenges[i] = c
	}
	// Answer them out of order.
	for i := n - 1; i >= 0; i-- {
		cchal := challenge(t)
		proof, _, err := s.Answer(cchal, ClientProof(secret, challenges[i], bind), bind)
		if err != nil {
			t.Fatalf("challenge %d: %v", i, err)
		}
		if !bytes.Equal(proof, ServerProof(secret, cchal, bind)) {
			t.Fatalf("challenge %d got the wrong server proof", i)
		}
	}
}

func TestMalformedIsRefused(t *testing.T) {
	s := newServer(t)
	schal, _ := s.Begin("test")
	for _, tc := range []struct {
		name          string
		cchal, cproof []byte
	}{
		{"no challenge", nil, ClientProof(secret, schal, bind)},
		{"short challenge", []byte{1, 2, 3}, ClientProof(secret, schal, bind)},
		{"no proof", challenge(t), nil},
		{"empty both", nil, nil},
	} {
		if _, _, err := s.Answer(tc.cchal, tc.cproof, bind); err == nil {
			t.Errorf("%s was accepted", tc.name)
		}
	}
}

func TestPendingChallengesAreCapped(t *testing.T) {
	s := newServer(t)
	for i := 0; i < maxPending*3; i++ {
		if _, err := s.Begin("flood"); err != nil {
			t.Fatal(err)
		}
	}
	s.mu.Lock()
	held := len(s.pendings)
	s.mu.Unlock()
	if held > maxPending {
		t.Errorf("holding %d challenges, cap is %d", held, maxPending)
	}

	// And a real caller arriving mid-flood still gets in.
	schal, _ := s.Begin("legitimate")
	if _, _, err := s.Answer(challenge(t), ClientProof(secret, schal, bind), bind); err != nil {
		t.Errorf("a legitimate login during a flood was refused: %v", err)
	}
}

func TestChallengesAreUnique(t *testing.T) {
	s := newServer(t)
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		c, err := s.Begin("test")
		if err != nil {
			t.Fatal(err)
		}
		if seen[string(c)] {
			t.Fatal("a challenge repeated")
		}
		seen[string(c)] = true
	}
}

func TestLoadSecretRefusesLooseMode(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "secret")
	if err := os.WriteFile(path, []byte("# a note\nhunter2\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := LoadSecret(path)
	if err != nil {
		t.Fatalf("mode 600 rejected: %v", err)
	}
	if got != "hunter2" {
		t.Errorf("secret = %q, want hunter2 (comments and blank lines skipped)", got)
	}

	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSecret(path); err == nil {
		t.Error("a group-readable secret was accepted")
	}

	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSecret(path); err == nil {
		t.Error("a secret in a world-readable directory was accepted")
	}
}

// One file for the whole lab, found the same way by every program that
// uses this package -- so where it is has to be settled here rather
// than passed in, and a caller that names nothing must land on it.
func TestTheDefaultSecretPathIsUnderTheHomeDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	want := filepath.Join(home, ".config", "slgod", "secret")
	if got := DefaultSecretPath(); got != want {
		t.Errorf("DefaultSecretPath = %q, want %q", got, want)
	}

	// And LoadSecret with no path reads that file, which is how both
	// slgod and every client find the same secret.
	dir := filepath.Dir(want)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, []byte("\t hunter2 \t\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSecret("")
	if err != nil {
		t.Fatal(err)
	}
	if got != "hunter2" {
		t.Errorf("secret = %q; surrounding whitespace should be trimmed", got)
	}
}

// A machine set up before the secret moved has it under slrun, and
// nothing under slgod.  That file is still the one to read: a daemon
// or client that suddenly could not find the secret it has used for
// months would be a broken lab for the sake of a tidier path.
func TestTheOldSecretPathIsUsedWhenTheNewOneIsMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	old := filepath.Join(home, ".config", "slrun", "secret")
	if err := os.MkdirAll(filepath.Dir(old), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte("from-the-old-place\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := DefaultSecretPath(); got != old {
		t.Errorf("DefaultSecretPath = %q, want the old file %q", got, old)
	}
	got, err := LoadSecret("")
	if err != nil {
		t.Fatal(err)
	}
	if got != "from-the-old-place" {
		t.Errorf("secret = %q, want the old file's", got)
	}

	// slgod's own directory existing, with no secret in it, changes
	// nothing: it holds slgod's machine config on any machine that has
	// run slgod, secret or not.
	if err := os.MkdirAll(filepath.Join(home, ".config", "slgod"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := DefaultSecretPath(); got != old {
		t.Errorf("with an empty slgod directory, DefaultSecretPath = %q, want %q", got, old)
	}
}

// With both there, the new one wins.  Somebody who has moved the file
// and left a copy behind has told us which one they mean.
func TestTheNewSecretPathWinsOverTheOld(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	for _, d := range []string{"slrun", "slgod"} {
		p := filepath.Join(home, ".config", d, "secret")
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("from-"+d+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := LoadSecret("")
	if err != nil {
		t.Fatal(err)
	}
	if got != "from-slgod" {
		t.Errorf("secret = %q, want the one under slgod", got)
	}
}

// With no home directory there is nowhere for the file to be, and
// saying so as an empty path is what turns into a plain "no such file"
// further up rather than a panic.
func TestNoHomeDirectoryMeansNoDefaultPath(t *testing.T) {
	t.Setenv("HOME", "")
	if got := DefaultSecretPath(); got != "" {
		t.Errorf("DefaultSecretPath = %q with no home directory, want empty", got)
	}
}

// The ways a secret file can be there and still be no use.  Each is a
// different thing for the operator to do, which is why they are
// distinct errors and not one.
func TestLoadSecretRefusesWhatItCannotUse(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Nothing there at all.
	if _, err := LoadSecret(filepath.Join(dir, "absent")); err == nil {
		t.Error("a secret that does not exist was accepted")
	}

	// A file holding only a note about itself is empty as far as this
	// is concerned, and an empty secret would authenticate anybody who
	// also had nothing.
	comments := filepath.Join(dir, "comments")
	if err := os.WriteFile(comments, []byte("# what this is for\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSecret(comments); err == nil {
		t.Error("a file with nothing but comments was accepted as a secret")
	} else if !strings.Contains(err.Error(), "empty") {
		t.Errorf("the refusal does not say it is empty: %v", err)
	}

	// The right mode on something that cannot be read as a file.  The
	// mode check passes and the read is what fails, which is a
	// different sentence for the operator than "chmod it".
	notAFile := filepath.Join(dir, "adirectory")
	if err := os.Mkdir(notAFile, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSecret(notAFile); err == nil {
		t.Error("a directory was read as a secret")
	}
}

func TestNewRequiresSecret(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Error("a server with no secret was created")
	}
}

// The binding is what ties a proof to one TLS session, so a proof made
// on one connection must be worthless on another. That is the whole
// defence against a man in the middle relaying an unverified channel.
func TestAProofDoesNotTravelBetweenConnections(t *testing.T) {
	s := newServer(t)
	other := []byte("a DIFFERENT tls channel binding...")

	schal, _ := s.Begin("test")
	stolen := ClientProof(secret, schal, bind)

	if _, _, err := s.Answer(challenge(t), stolen, other); err == nil {
		t.Fatal("a proof from one connection was accepted on another")
	}
}

// And the server's proof is likewise only good on the connection it was
// made for, so a relay cannot pass it on.
func TestServerProofIsPerConnection(t *testing.T) {
	c := challenge(t)
	other := []byte("a DIFFERENT tls channel binding...")
	if bytes.Equal(ServerProof(secret, c, bind), ServerProof(secret, c, other)) {
		t.Fatal("the server's proof is the same on two different connections")
	}
}
