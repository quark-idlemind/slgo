package msg

import "fmt"

// Raw is a message carried as its number and its undecoded body.
//
// It exists so that something can move a message it does not
// understand: a server relaying to a client, a capture being replayed,
// a message this build predates.  Encode hands back the bytes it was
// given, so nothing is lost in the middle.
type Raw struct {
	info Info
	Body []byte
}

// NewRaw wraps a message number and body.  The name comes from the
// template when the number is known, and is a placeholder when it is
// not -- an unknown message is still worth relaying.
func NewRaw(id ID, body []byte) *Raw {
	r := &Raw{Body: body}
	if info := Lookup(id); info != nil {
		r.info = *info
	} else {
		r.info = Info{Name: fmt.Sprintf("Unknown(%s %d)", id.Freq(), id.Number()), ID: id}
	}
	return r
}

func (r *Raw) MsgInfo() *Info { return &r.info }

// Encode returns the body unchanged.
func (r *Raw) Encode() ([]byte, error) { return r.Body, nil }

// Decode keeps a copy of the body.
func (r *Raw) Decode(b []byte) error {
	r.Body = append([]byte(nil), b...)
	return nil
}

// Known reports whether this message number is in the template.
func (r *Raw) Known() bool { return Lookup(r.info.ID) != nil }
