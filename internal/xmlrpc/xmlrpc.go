// Package xmlrpc is the login protocol's wire format, in both
// directions.
//
// It was written to read what a login server says, and the reason it is
// its own package is that slgod now has to SAY it as well: a viewer
// handed a running session speaks the ordinary login protocol at slgod,
// which means parsing a methodCall and composing a methodResponse.
// Duplicating a parser that took this much care to be forgiving would
// have been a shame.
//
// That forgiveness is the point.  The C client called err(1,
// "Unexpected value type got %s") on anything it did not recognise,
// which is how a login response that grew <int> fields took the whole
// program down.  Here an unfamiliar type is data to keep rather than a
// reason to stop.
package xmlrpc

import (
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// XML-RPC, decoded to plain Go values:
//
//	<string>          string
//	<int> <i4>        int64
//	<boolean>         bool
//	<double>          float64
//	<array>           []any
//	<struct>          map[string]any
//	<nil> <undef>     nil
//	anything else     string, holding whatever text was inside
//
// The last line is the important one.  The C client's parser called
// err(1, "Unexpected value type got %s") on anything it did not know,
// which is how a login response that grew <int> fields took the whole
// program down.  An unfamiliar type here is data we keep rather than a
// reason to stop.

// Fault is an XML-RPC fault returned instead of a result.
type Fault struct {
	Code    int64
	Message string
}

func (f *Fault) Error() string {
	return fmt.Sprintf("xmlrpc fault %d: %s", f.Code, f.Message)
}

// DecodeResponse reads a methodResponse and returns its single
// parameter.
func DecodeResponse(r io.Reader) (any, error) {
	d := xml.NewDecoder(r)
	for {
		tok, err := d.Token()
		if err == io.EOF {
			return nil, fmt.Errorf("xmlrpc: no methodResponse")
		}
		if err != nil {
			return nil, err
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch start.Name.Local {
		case "methodResponse":
			return decodeMethodResponse(d)
		default:
			return nil, fmt.Errorf("xmlrpc: expected methodResponse, got <%s>", start.Name.Local)
		}
	}
}

func decodeMethodResponse(d *xml.Decoder) (any, error) {
	for {
		tok, err := d.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "params":
				return decodeParams(d)
			case "fault":
				return nil, decodeFault(d)
			default:
				if err := d.Skip(); err != nil {
					return nil, err
				}
			}
		case xml.EndElement:
			if t.Name.Local == "methodResponse" {
				return nil, fmt.Errorf("xmlrpc: methodResponse held neither params nor fault")
			}
		}
	}
}

func decodeParams(d *xml.Decoder) (any, error) {
	var out any
	found := false
	for {
		tok, err := d.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local != "param" {
				if err := d.Skip(); err != nil {
					return nil, err
				}
				continue
			}
			v, err := decodeParam(d)
			if err != nil {
				return nil, err
			}
			if !found {
				out, found = v, true
			}
		case xml.EndElement:
			if t.Name.Local == "params" {
				if !found {
					return nil, fmt.Errorf("xmlrpc: params held no param")
				}
				return out, nil
			}
		}
	}
}

func decodeParam(d *xml.Decoder) (any, error) {
	var out any
	for {
		tok, err := d.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "value" {
				v, err := decodeValue(d)
				if err != nil {
					return nil, err
				}
				out = v
				continue
			}
			if err := d.Skip(); err != nil {
				return nil, err
			}
		case xml.EndElement:
			if t.Name.Local == "param" {
				return out, nil
			}
		}
	}
}

func decodeFault(d *xml.Decoder) error {
	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local != "value" {
				if err := d.Skip(); err != nil {
					return err
				}
				continue
			}
			v, err := decodeValue(d)
			if err != nil {
				return err
			}
			f := &Fault{Message: "unknown"}
			if m, ok := v.(map[string]any); ok {
				if c, ok := m["faultCode"].(int64); ok {
					f.Code = c
				}
				if s, ok := m["faultString"].(string); ok {
					f.Message = s
				}
			}
			return f
		case xml.EndElement:
			if t.Name.Local == "fault" {
				return &Fault{Message: "empty fault"}
			}
		}
	}
}

// decodeValue is called just after a <value> start element and returns
// once its matching end element has been consumed.
func decodeValue(d *xml.Decoder) (any, error) {
	var text strings.Builder
	var out any
	typed := false

	for {
		tok, err := d.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.CharData:
			if !typed {
				text.Write(t)
			}
		case xml.StartElement:
			v, err := decodeTyped(d, t)
			if err != nil {
				return nil, err
			}
			out, typed = v, true
		case xml.EndElement:
			if t.Name.Local == "value" {
				if typed {
					return out, nil
				}
				// A <value> with no type element is a
				// string, per the specification.
				return text.String(), nil
			}
		}
	}
}

// decodeTyped is called just after the start element naming a type and
// returns once that element has been consumed.
func decodeTyped(d *xml.Decoder, start xml.StartElement) (any, error) {
	name := start.Name.Local
	switch name {
	case "array":
		return decodeArray(d)
	case "struct":
		return decodeStruct(d)
	case "nil", "undef":
		// The absent value, which is not in the XML-RPC
		// specification but is in most implementations of it.  It
		// has to be told apart from the empty string, because this
		// package now writes responses as well as reading them, and
		// a value that came in absent has to go out absent rather
		// than as "".
		if err := d.Skip(); err != nil {
			return nil, err
		}
		return nil, nil
	}

	s, err := readText(d, name)
	if err != nil {
		return nil, err
	}
	switch name {
	case "int", "i4", "i8":
		n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil {
			// Keep the text rather than failing the login
			// over one malformed field.
			return s, nil
		}
		return n, nil
	case "boolean":
		return strings.TrimSpace(s) == "1" || strings.EqualFold(strings.TrimSpace(s), "true"), nil
	case "double":
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return s, nil
		}
		return f, nil
	default:
		// string, dateTime.iso8601, base64, and anything Linden
		// Lab adds next.
		return s, nil
	}
}

// readText accumulates the character data of an element, skipping any
// nested elements, and consumes the closing tag.
func readText(d *xml.Decoder, name string) (string, error) {
	var sb strings.Builder
	depth := 0
	for {
		tok, err := d.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.CharData:
			if depth == 0 {
				sb.Write(t)
			}
		case xml.StartElement:
			depth++
		case xml.EndElement:
			if depth == 0 && t.Name.Local == name {
				return sb.String(), nil
			}
			depth--
		}
	}
}

func decodeArray(d *xml.Decoder) (any, error) {
	out := []any{}
	for {
		tok, err := d.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "data":
				// fall through into the values
			case "value":
				v, err := decodeValue(d)
				if err != nil {
					return nil, err
				}
				out = append(out, v)
			default:
				if err := d.Skip(); err != nil {
					return nil, err
				}
			}
		case xml.EndElement:
			if t.Name.Local == "array" {
				return out, nil
			}
		}
	}
}

func decodeStruct(d *xml.Decoder) (any, error) {
	out := map[string]any{}
	for {
		tok, err := d.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local != "member" {
				if err := d.Skip(); err != nil {
					return nil, err
				}
				continue
			}
			k, v, err := decodeMember(d)
			if err != nil {
				return nil, err
			}
			if k != "" {
				out[k] = v
			}
		case xml.EndElement:
			if t.Name.Local == "struct" {
				return out, nil
			}
		}
	}
}

func decodeMember(d *xml.Decoder) (string, any, error) {
	var key string
	var val any
	for {
		tok, err := d.Token()
		if err != nil {
			return "", nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "name":
				s, err := readText(d, "name")
				if err != nil {
					return "", nil, err
				}
				key = strings.TrimSpace(s)
			case "value":
				v, err := decodeValue(d)
				if err != nil {
					return "", nil, err
				}
				val = v
			default:
				if err := d.Skip(); err != nil {
					return "", nil, err
				}
			}
		case xml.EndElement:
			if t.Name.Local == "member" {
				return key, val, nil
			}
		}
	}
}

// ---------------------------------------------------------------- access

// Lookup walks a decoded response by key, so callers do not repeat the
// type assertions.
func Lookup(m map[string]any, key string) (any, bool) {
	v, ok := m[key]
	return v, ok
}

func String(m map[string]any, key string) string {
	switch v := m[key].(type) {
	case string:
		return v
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64)
	case bool:
		if v {
			return "true"
		}
		return "false"
	}
	return ""
}

// Int accepts a number that arrived either as <int> or, as older
// login servers used to send them, as a <string>.
func Int(m map[string]any, key string) (int64, bool) {
	switch v := m[key].(type) {
	case int64:
		return v, true
	case float64:
		return int64(v), true
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil {
			return 0, false
		}
		return n, true
	}
	return 0, false
}

// Unquote strips the double quotes the login server wraps first_name
// in.  The name is used verbatim as the sender name on outgoing
// instant messages, so the quotes are not cosmetic.
func Unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}
