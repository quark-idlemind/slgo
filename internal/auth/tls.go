package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"sync"
	"time"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"

	"context"
)

// TLS here is for CONFIDENTIALITY ONLY. Nobody checks the certificate.
//
// The server makes a fresh self-signed one each time it starts and the
// client does not verify it, so there is nothing to distribute, expire
// or rotate. That is not laxness: the certificate was never what proved
// anything. Identity comes from the Login handshake, and the handshake
// is tied to this exact TLS session by the channel binding below, which
// is what stops the obvious attack on an unverified channel.
//
// Without that binding, unverified TLS plus application authentication
// is a trap: an attacker terminating TLS on both sides relays every
// message of the handshake unchanged -- none of them mentions the
// connection, so all of them still verify -- and then reads and rewrites
// everything after it. With it, the attacker's two TLS sessions export
// different material, and the proof computed on one does not verify on
// the other.

// ServerTLS returns credentials with a freshly generated certificate.
func ServerTLS() (credentials.TransportCredentials, error) {
	cert, err := selfSigned()
	if err != nil {
		return nil, err
	}
	return credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{cert},
		// TLS 1.3 only. The exporter this depends on is well defined
		// there, and there is no reason to accept anything older between
		// two programs shipped together.
		MinVersion: tls.VersionTLS13,
	}), nil
}

// selfSigned makes a certificate for a server nobody will check.
func selfSigned() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "slrund"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

// BindingFromContext exports this connection's keying material, server
// side, from the peer information gRPC attaches to every call.
func BindingFromContext(ctx context.Context) ([]byte, error) {
	pr, ok := peer.FromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("no peer information")
	}
	info, ok := pr.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return nil, fmt.Errorf("the connection is not TLS, so it cannot be bound to")
	}
	return info.State.ExportKeyingMaterial(BindingLabel, nil, BindingLength)
}

// ClientTLS returns credentials that encrypt without verifying, and a
// function returning the channel binding once the handshake has run.
// The function fails rather than returning nothing.
//
// The binding has to be captured during the handshake because gRPC does
// not offer the TLS state to client code afterwards. Wrapping the
// credentials is the hook.
func ClientTLS() (credentials.TransportCredentials, func() ([]byte, error)) {
	c := &bindingCreds{
		got: &bindingSlot{},
		TransportCredentials: credentials.NewTLS(&tls.Config{
			// Deliberate. See the note at the top of this file: the
			// certificate proves nothing and is not meant to. What
			// proves the server is the Login handshake, bound to this
			// session.
			InsecureSkipVerify: true,
			MinVersion:         tls.VersionTLS13,
		}),
	}
	return c, c.Binding
}

type bindingCreds struct {
	credentials.TransportCredentials

	// Shared with every clone, deliberately. gRPC clones credentials per
	// connection, so a clone with its own storage would record the
	// binding where the caller cannot see it -- and the caller would then
	// compute its proof over NOTHING.
	//
	// That failure would not look like a failure. An empty binding on
	// both sides still verifies, because both sides hash the same
	// nothing, so the handshake would succeed with the channel binding
	// silently absent. Which is why Binding refuses to answer with an
	// empty value rather than returning one.
	got *bindingSlot
}

type bindingSlot struct {
	mu sync.Mutex
	b  []byte
}

func (s *bindingSlot) set(b []byte) {
	s.mu.Lock()
	s.b = b
	s.mu.Unlock()
}

func (s *bindingSlot) get() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b
}

func (c *bindingCreds) ClientHandshake(ctx context.Context, authority string, raw net.Conn) (net.Conn, credentials.AuthInfo, error) {
	conn, info, err := c.TransportCredentials.ClientHandshake(ctx, authority, raw)
	if err != nil {
		return conn, info, err
	}
	if tlsInfo, ok := info.(credentials.TLSInfo); ok {
		if b, e := tlsInfo.State.ExportKeyingMaterial(BindingLabel, nil, BindingLength); e == nil {
			c.got.set(b)
		}
	}
	return conn, info, err
}

// Clone keeps the wrapper in place. gRPC clones credentials per
// connection, and a clone that dropped back to the embedded value would
// silently stop recording the binding.
func (c *bindingCreds) Clone() credentials.TransportCredentials {
	return &bindingCreds{TransportCredentials: c.TransportCredentials.Clone(), got: c.got}
}

// Binding is the exported keying material for the connection that was
// made, or an error if there is none.
//
// An error rather than an empty slice on purpose. Proving knowledge of
// the secret over an empty binding succeeds at both ends and protects
// nothing, so the one thing this must never do is quietly hand back
// nothing at all.
func (c *bindingCreds) Binding() ([]byte, error) {
	b := c.got.get()
	if len(b) != BindingLength {
		return nil, fmt.Errorf(
			"no TLS channel binding was captured (%d bytes); refusing to authenticate without one", len(b))
	}
	return b, nil
}
