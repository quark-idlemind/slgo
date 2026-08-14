// Package agent is one avatar's connection to the grid: the XML-RPC
// exchange with the login server, then the UDP circuit to the simulator
// it hands you.
//
// An Agent is what the C client calls scommon_t -- the state shared by
// everything talking to one logged-in avatar.  It is deliberately not
// called a session: in this tree a session is an attached client, which
// is what package server serves.
package agent

import (
	"io"

	"github.com/quark-idlemind/slgo/internal/xmlrpc"
)

// The XML-RPC decoding lives in internal/xmlrpc, because slgod now has
// to write the protocol as well as read it: a viewer handed a running
// session speaks login at slgod and expects a methodResponse back.
//
// These are the names this package had before the move, kept so that
// the callers here read as they always did and so that the tests that
// came with the decoder still run against it.

// Fault is an XML-RPC fault returned instead of a result.
type Fault = xmlrpc.Fault

func decodeResponse(r io.Reader) (any, error) { return xmlrpc.DecodeResponse(r) }

func getString(m map[string]any, key string) string { return xmlrpc.String(m, key) }

func getInt(m map[string]any, key string) (int64, bool) { return xmlrpc.Int(m, key) }

func unquote(s string) string { return xmlrpc.Unquote(s) }

func lookup(m map[string]any, key string) (any, bool) { return xmlrpc.Lookup(m, key) }
