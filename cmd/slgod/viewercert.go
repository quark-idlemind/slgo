package main

// The certificate the viewer endpoint serves.
//
// This one is nothing like the gRPC side's.  There (auth/tls.go) the
// certificate proves nothing on purpose: it is made fresh every start,
// the client does not look at it, and what proves both ends is the
// Login handshake bound to the TLS session.  Here the certificate is
// the whole of what the viewer checks, because a viewer has no
// handshake of ours to run -- so it has to be verifiable, and it has to
// be the SAME one tomorrow.  A viewer trusts a certificate by putting a
// copy in its own store; one made fresh at every start would have to be
// re-trusted by hand at every start, which is a thing nobody does twice
// before turning verification off instead.
//
// So it is written once and kept, beside the machine identity and for
// the same reason: what matters is less what it is than that it does
// not change.
//
// Three things about the shape are not decoration.  Firestorm's
// certificate store refuses a certificate with no Subject Key
// Identifier outright (_validateCert, llsechandler_basic.cpp); it wants
// digitalSignature among any key usages and serverAuth among any
// extended ones; and it leaves the hostname to libcurl, so the address
// a viewer dials has to be in the subjectAltName -- a common name alone
// is not matched.  Each of those, got wrong, produces a viewer that
// returns to its login screen and says why only in its own log.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// viewerCertLife is how long a generated certificate is good for.
//
// Ten years, as the gRPC side's is.  Expiry protects against a leaked
// key being useful later, and this key is a file in the operator's own
// config directory on the machine the daemon runs on: anybody who can
// read it can read the profiles beside it, which are the accounts
// themselves.  Against that, an expiry short enough to matter would
// only mean the viewer's trust store went stale without warning, some
// morning, for no attacker's benefit.
const viewerCertLife = 10 * 365 * 24 * time.Hour

// viewerCertFiles resolves the certificate and key the viewer endpoint
// serves, generating a self-signed pair if the operator named none.
//
// host is the address -viewer was bound to, and is what the generated
// certificate has to cover.  An empty or wildcard host means the
// endpoint answers on addresses this cannot know, so the certificate
// covers loopback and the caller is told the rest is uncovered.
func viewerCertFiles(certFile, keyFile, host string, logf func(string, ...any)) (string, string, error) {
	if certFile != "" {
		// The operator's own, which may well be from a real authority
		// and is not ours to second-guess.
		return certFile, keyFile, nil
	}

	dir, err := machineConfigDir()
	if err != nil {
		return "", "", err
	}
	certFile = filepath.Join(dir, "viewer-cert.pem")
	keyFile = filepath.Join(dir, "viewer-key.pem")

	switch why := viewerCertUnusable(certFile, keyFile, host); why {
	case "":
		return certFile, keyFile, nil
	default:
		if _, err := os.Stat(certFile); err == nil {
			// Replacing one that is already trusted somewhere.  Said
			// loudly, because until the viewer is told about the new
			// one it will refuse to log in and blame the daemon.
			logf("viewer: replacing %s: %s", certFile, why)
			logf("viewer: the viewer must be told about the new certificate before it will connect")
		}
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", fmt.Errorf("viewer: %w", err)
	}
	if err := writeViewerCert(certFile, keyFile, host); err != nil {
		return "", "", err
	}
	logf("viewer: wrote a self-signed certificate to %s", certFile)
	logf("viewer: a viewer will refuse it until it is trusted -- on Firestorm, append it to " +
		"user_settings/CA.pem, or accept the certificate dialog once")
	return certFile, keyFile, nil
}

// viewerCertUnusable says why the stored pair cannot be served, or ""
// if it can.
//
// The host check is the one worth having.  A daemon moved from loopback
// to a tunnel address keeps a certificate that no longer names where it
// answers, and the failure lands on the viewer as a hostname mismatch
// with nothing on this side saying so.
func viewerCertUnusable(certFile, keyFile, host string) string {
	pemBytes, err := os.ReadFile(certFile)
	if err != nil {
		return "there is none yet"
	}
	if _, err := os.Stat(keyFile); err != nil {
		return "the key beside it is missing"
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return "it does not parse"
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "it does not parse"
	}
	if time.Now().After(cert.NotAfter) {
		return "it has expired"
	}
	if h := certHost(host); h != "" {
		if err := cert.VerifyHostname(h); err != nil {
			return fmt.Sprintf("it does not cover %s", h)
		}
	}
	return ""
}

// certHost is the name a viewer would check, or "" when the endpoint is
// bound to every address and there is nothing to check against.
func certHost(host string) string {
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		return ""
	}
	return host
}

// writeViewerCert makes the pair and writes it out, key first and
// private.
func writeViewerCert(certFile, keyFile, host string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("viewer: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return fmt.Errorf("viewer: %w", err)
	}
	// The Subject Key Identifier Firestorm insists on: the SHA-1 of the
	// public key, which is what every other generator uses too.
	pub, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return fmt.Errorf("viewer: %w", err)
	}
	skid := sha1.Sum(pub)

	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "slgod viewer endpoint"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(viewerCertLife),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		SubjectKeyId:          skid[:],
	}
	addCertHosts(tmpl, host)

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("viewer: %w", err)
	}
	// The key first and at 0600, so that there is never a moment when a
	// certificate names a key anybody can read.
	der8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return fmt.Errorf("viewer: %w", err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(
		&pem.Block{Type: "PRIVATE KEY", Bytes: der8}), 0o600); err != nil {
		return fmt.Errorf("viewer: writing the key: %w", err)
	}
	if err := os.WriteFile(certFile, pem.EncodeToMemory(
		&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return fmt.Errorf("viewer: writing the certificate: %w", err)
	}
	return nil
}

// addCertHosts puts the addresses a viewer might dial into the
// subjectAltName.
//
// Loopback always, because a viewer on this machine is the ordinary
// case and stays valid even for a daemon bound elsewhere; and the bound
// address as well when it is one this can name.
func addCertHosts(tmpl *x509.Certificate, host string) {
	tmpl.IPAddresses = []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}
	tmpl.DNSNames = []string{"localhost"}
	h := certHost(host)
	if h == "" {
		return
	}
	if ip := net.ParseIP(h); ip != nil {
		for _, have := range tmpl.IPAddresses {
			if have.Equal(ip) {
				return
			}
		}
		tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		return
	}
	if h != "localhost" {
		tmpl.DNSNames = append(tmpl.DNSNames, h)
	}
}
