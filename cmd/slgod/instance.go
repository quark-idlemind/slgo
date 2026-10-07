package main

import (
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"path/filepath"
)

// What only one slgod can hold, taken before anybody is logged in.
//
// A login from anywhere else ends the avatar's session on the grid
// ("logged you out because you are attempting to log in from another
// location"), and the session it ended does not come back on its own: the
// daemon that held it takes being thrown off as a decision.  So a second
// slgod started by mistake, which logged its avatars in and only then found
// its port taken, cost the running daemon every avatar it had for nothing.
//
// Everything a second instance would collide on is therefore taken here,
// first, and a refusal is the end of the run with no login made:
//
//   - the lock on the config directory, because a second daemon on another
//     port but the same profiles would log the same avatars in just the
//     same;
//   - the -listen address;
//   - the -viewer address.
//
// The listeners are handed to the server later, once there is something to
// serve.  A client that connects in between waits in the kernel's queue
// for the daemon to start accepting rather than being refused.
//
// Why: doc/guide.md#slgod

// lockName is the file in the config directory that an slgod holds for as
// long as it runs.
const lockName = "slgod.lock"

// taken is what one run holds from before its first login.
type taken struct {
	lock      *os.File     // held for the life of the process
	listen    net.Listener // -listen
	viewer    net.Listener // -viewer, nil without it
	viewerTLS *tls.Config  // the viewer endpoint's certificate, loaded
}

// close lets go of all of it, for a run that ends before serving.  The
// lock is released by the operating system when the process exits, so a
// crash leaves nothing stale, but a test that runs more than one in a
// process has to let go.
func (t *taken) close() {
	if t == nil {
		return
	}
	if t.listen != nil {
		t.listen.Close()
	}
	if t.viewer != nil {
		t.viewer.Close()
	}
	if t.lock != nil {
		t.lock.Close()
	}
}

// takeFirst takes the config directory's lock, then the -listen address,
// then the -viewer address, in that order, and returns at the first it
// cannot have.  Every error says nobody was logged in, because that is
// what somebody who has just started a second daemon by mistake wants to
// know before anything else: the first one is untouched.
//
// certs says which certificate and key the viewer endpoint serves, making
// them if need be; it is called after the lock is held, so that two
// daemons cannot both be writing the one pair, and not at all without
// -viewer.  They are loaded before the address is bound, so that an
// unreadable pair is an error naming the file.
func takeFirst(configDir, listen, viewerAt string, certs func() (cert, key string, err error)) (*taken, error) {
	t := &taken{}
	var err error

	lockPath := filepath.Join(configDir, lockName)
	if t.lock, err = lockConfigDir(lockPath); err != nil {
		return nil, err
	}

	if t.listen, err = net.Listen("tcp", listen); err != nil {
		t.close()
		return nil, fmt.Errorf("cannot listen on %s: %v; nobody has been logged in, "+
			"so whatever holds that address keeps its sessions", listen, err)
	}

	if viewerAt != "" {
		certFile, keyFile, err := certs()
		if err != nil {
			t.close()
			return nil, fmt.Errorf("-viewer: %v; nobody has been logged in", err)
		}
		if t.viewer, t.viewerTLS, err = bindViewer(viewerAt, certFile, keyFile); err != nil {
			t.close()
			return nil, fmt.Errorf("-viewer %s: %v; nobody has been logged in", viewerAt, err)
		}
	}
	return t, nil
}
