package xmlrpc

import (
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// DecodeCall reads a methodCall and returns the method name and its
// single struct parameter.
//
// This is the direction the login code never needed: slgo asked and read
// the answer.  slgod hosting a viewer is on the other side of that
// exchange -- the viewer says login_to_simulator at it and waits -- so
// the call has to be taken apart as well as composed.
//
// A call whose parameter is not a struct is an error rather than
// something to work around.  Every login_to_simulator is one struct of
// members, and a caller sending anything else has not made a request
// worth guessing at.
func DecodeCall(r io.Reader) (method string, params map[string]any, err error) {
	d := xml.NewDecoder(r)
	for {
		tok, err := d.Token()
		if err == io.EOF {
			return "", nil, fmt.Errorf("xmlrpc: no methodCall")
		}
		if err != nil {
			return "", nil, err
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if start.Name.Local != "methodCall" {
			continue
		}
		return decodeCallBody(d)
	}
}

func decodeCallBody(d *xml.Decoder) (string, map[string]any, error) {
	var method string
	var params map[string]any
	seen := false
	for {
		tok, err := d.Token()
		if err == io.EOF {
			return "", nil, fmt.Errorf("xmlrpc: methodCall ended early")
		}
		if err != nil {
			return "", nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "methodName":
				s, err := readText(d, "methodName")
				if err != nil {
					return "", nil, err
				}
				method = strings.TrimSpace(s)
			case "params":
				v, err := decodeParams(d)
				if err != nil {
					return "", nil, err
				}
				m, ok := v.(map[string]any)
				if !ok {
					return "", nil, fmt.Errorf(
						"xmlrpc: methodCall parameter is %T, want a struct", v)
				}
				params, seen = m, true
			default:
				if err := d.Skip(); err != nil {
					return "", nil, err
				}
			}
		case xml.EndElement:
			if t.Name.Local == "methodCall" {
				if method == "" {
					return "", nil, fmt.Errorf("xmlrpc: methodCall has no methodName")
				}
				if !seen {
					// A call with no parameters at all is
					// well formed; an empty map is a
					// truer answer than an error.
					params = map[string]any{}
				}
				return method, params, nil
			}
		}
	}
}

// EncodeResponse writes a methodResponse carrying one struct.
//
// The values are written back in the types they were decoded as, so a
// response read with DecodeResponse and written with this one says the
// same thing.  That round trip is the whole point: slgod answers a
// viewer's login by replaying the response the real login server gave
// it, with three fields swapped, and anything this encoder cannot carry
// would be quietly dropped from the viewer's idea of the session.
//
// Keys are written in sorted order so the same map produces the same
// bytes, which makes the result diffable against a capture.
func EncodeResponse(w io.Writer, params map[string]any) error {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?>`)
	b.WriteString("<methodResponse><params><param>")
	if err := encodeValue(&b, params); err != nil {
		return err
	}
	b.WriteString("</param></params></methodResponse>")
	_, err := io.WriteString(w, b.String())
	return err
}

// EncodeFault writes a methodResponse carrying a fault.
func EncodeFault(w io.Writer, code int64, message string) error {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?>`)
	b.WriteString("<methodResponse><fault>")
	if err := encodeValue(&b, map[string]any{
		"faultCode":   code,
		"faultString": message,
	}); err != nil {
		return err
	}
	b.WriteString("</fault></methodResponse>")
	_, err := io.WriteString(w, b.String())
	return err
}

func encodeValue(b *strings.Builder, v any) error {
	b.WriteString("<value>")
	defer b.WriteString("</value>")

	switch t := v.(type) {
	case nil:
		// The login response really does contain these -- a
		// grid's loginpage is <undef/> on more than one entry --
		// and dropping them would change the shape of the map.
		b.WriteString("<nil/>")
	case string:
		b.WriteString("<string>")
		xml.EscapeText(b, []byte(t))
		b.WriteString("</string>")
	case bool:
		b.WriteString("<boolean>")
		if t {
			b.WriteString("1")
		} else {
			b.WriteString("0")
		}
		b.WriteString("</boolean>")
	case int:
		b.WriteString("<int>" + strconv.Itoa(t) + "</int>")
	case int64:
		b.WriteString("<int>" + strconv.FormatInt(t, 10) + "</int>")
	case float64:
		b.WriteString("<double>" + strconv.FormatFloat(t, 'f', -1, 64) + "</double>")
	case []any:
		b.WriteString("<array><data>")
		for _, e := range t {
			if err := encodeValue(b, e); err != nil {
				return err
			}
		}
		b.WriteString("</data></array>")
	case map[string]any:
		b.WriteString("<struct>")
		for _, k := range sortedKeys(t) {
			b.WriteString("<member><name>")
			xml.EscapeText(b, []byte(k))
			b.WriteString("</name>")
			if err := encodeValue(b, t[k]); err != nil {
				return err
			}
			b.WriteString("</member>")
		}
		b.WriteString("</struct>")
	default:
		return fmt.Errorf("xmlrpc: cannot encode %T", v)
	}
	return nil
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
