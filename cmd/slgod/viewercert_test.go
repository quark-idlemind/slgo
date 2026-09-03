package main

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The certificate the viewer endpoint serves is kept rather than made
// fresh, and its shape is decided by what Firestorm's own store will
// accept.  Both of those are easy to break without any test noticing:
// a regenerated certificate still serves, and a certificate missing an
// extension still serves -- the viewer is the only thing that objects,
// in its own log, at the login screen.

// certDir points the config directory at a temporary one, so a test
// never reads or writes the operator's own.
func certDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SLGOD_CONFIG_DIR", dir)
	return dir
}

func readCert(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(b)
	if block == nil {
		t.Fatalf("%s does not parse as PEM", path)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

// TestAGeneratedCertificateIsTheShapeAViewerAccepts: each of these,
// got wrong, produces a viewer that returns to its login screen and
// says why only in its own log.
func TestAGeneratedCertificateIsTheShapeAViewerAccepts(t *testing.T) {
	certDir(t)
	certFile, _, err := viewerCertFiles("", "", "127.0.0.1", func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	cert := readCert(t, certFile)

	// Firestorm's _validateCert throws "Cert doesn't have a Subject Key
	// Id" on a certificate without one, and that is not the trust
	// dialog -- it is a refusal with no button on it.
	if len(cert.SubjectKeyId) == 0 {
		t.Error("no Subject Key Identifier, which Firestorm refuses outright")
	}
	if cert.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
		t.Error("no digitalSignature in the key usage")
	}
	var serverAuth bool
	for _, u := range cert.ExtKeyUsage {
		if u == x509.ExtKeyUsageServerAuth {
			serverAuth = true
		}
	}
	if !serverAuth {
		t.Error("no serverAuth in the extended key usage")
	}
	// The hostname is left to libcurl, so it is matched against the
	// subjectAltName and never against the common name.
	if err := cert.VerifyHostname("127.0.0.1"); err != nil {
		t.Errorf("the certificate does not cover 127.0.0.1: %v", err)
	}
	if err := cert.VerifyHostname("localhost"); err != nil {
		t.Errorf("the certificate does not cover localhost: %v", err)
	}
}

// TestTheCertificateIsKeptRatherThanRemade: a viewer trusts a
// certificate by keeping a copy.  One made fresh at every start would
// have to be re-trusted by hand at every start.
func TestTheCertificateIsKeptRatherThanRemade(t *testing.T) {
	certDir(t)
	certFile, keyFile, err := viewerCertFiles("", "", "127.0.0.1", func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	first := readCert(t, certFile)

	againCert, againKey, err := viewerCertFiles("", "", "127.0.0.1", func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	if againCert != certFile || againKey != keyFile {
		t.Fatalf("a second start named %s/%s, was %s/%s", againCert, againKey, certFile, keyFile)
	}
	if second := readCert(t, certFile); !second.Equal(first) {
		t.Error("the certificate was replaced on a second start, so every viewer must trust it again")
	}
}

// TestTheKeyIsNotReadableByAnybodyElse: it sits in a directory beside
// the profiles, and serving with it is proof of being this endpoint.
func TestTheKeyIsNotReadableByAnybodyElse(t *testing.T) {
	certDir(t)
	_, keyFile, err := viewerCertFiles("", "", "127.0.0.1", func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("the key is mode %04o, want nothing for group or other", mode)
	}
}

// TestACertificateThatDoesNotCoverTheAddressIsReplaced: a daemon moved
// from loopback to a tunnel address keeps a certificate that no longer
// names where it answers, and the failure would land on the viewer as a
// hostname mismatch with nothing on this side saying so.
func TestACertificateThatDoesNotCoverTheAddressIsReplaced(t *testing.T) {
	certDir(t)
	certFile, _, err := viewerCertFiles("", "", "127.0.0.1", func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	first := readCert(t, certFile)

	var said strings.Builder
	if _, _, err := viewerCertFiles("", "", "10.44.0.1",
		func(f string, a ...any) { said.WriteString(f) }); err != nil {
		t.Fatal(err)
	}
	second := readCert(t, certFile)
	if second.Equal(first) {
		t.Fatal("the certificate was kept for an address it does not cover")
	}
	if err := second.VerifyHostname("10.44.0.1"); err != nil {
		t.Errorf("the replacement does not cover the new address: %v", err)
	}
	// Still loopback too: a viewer on this machine is the ordinary case.
	if err := second.VerifyHostname("127.0.0.1"); err != nil {
		t.Errorf("the replacement dropped loopback: %v", err)
	}
	// And it said so, because until the viewer is told about the new
	// one it will refuse to connect and blame the daemon.
	if !strings.Contains(said.String(), "replacing") {
		t.Error("replacing a trusted certificate was not announced")
	}
}

// TestTheOperatorsOwnCertificateIsUsedAsGiven: it may well be from a
// real authority, and is not ours to second-guess or replace.
func TestTheOperatorsOwnCertificateIsUsedAsGiven(t *testing.T) {
	dir := certDir(t)
	mine := filepath.Join(dir, "mine.pem")
	minekey := filepath.Join(dir, "mine.key")

	gotCert, gotKey, err := viewerCertFiles(mine, minekey, "127.0.0.1", func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	if gotCert != mine || gotKey != minekey {
		t.Errorf("got %s/%s, want the pair that was named", gotCert, gotKey)
	}
	// And nothing was generated behind its back.
	if _, err := os.Stat(filepath.Join(dir, "viewer-cert.pem")); err == nil {
		t.Error("a certificate was generated even though one was named")
	}
}

// TestAWildcardAddressCoversLoopbackOnly: bound to every address, there
// is no one name a viewer would check, so the certificate covers what
// can be known and no more.
func TestAWildcardAddressCoversLoopbackOnly(t *testing.T) {
	certDir(t)
	for _, host := range []string{"", "0.0.0.0", "::"} {
		if got := certHost(host); got != "" {
			t.Errorf("certHost(%q) = %q, want the empty string", host, got)
		}
	}
	certFile, _, err := viewerCertFiles("", "", "0.0.0.0", func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	cert := readCert(t, certFile)
	if err := cert.VerifyHostname("127.0.0.1"); err != nil {
		t.Errorf("a wildcard bind produced a certificate that does not cover loopback: %v", err)
	}
	// And it is not replaced on the next start just because the bind
	// address cannot be checked against it.
	first := cert
	if _, _, err := viewerCertFiles("", "", "0.0.0.0", func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if !readCert(t, certFile).Equal(first) {
		t.Error("a wildcard bind replaced the certificate on every start")
	}
}
