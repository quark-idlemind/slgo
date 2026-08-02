package llsd

import (
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Package llsd decodes and encodes LLSD in its XML form,, decoded to plain Go values:
//
//	<map>              map[string]any
//	<array>            []any
//	<string>           string
//	<uuid> <uri>       string
//	<date>             string
//	<integer>          int64
//	<real>             float64
//	<boolean>          bool
//	<binary>           []byte, base64 decoded
//	<undef/>           nil
//	anything else      string, holding whatever text was inside
//
// As with the XML-RPC decoder, the last line is the point.  This is
// what the capability and inventory servers speak, and they gain types
// and fields over time.

// Decode reads one LLSD document.
func Decode(r io.Reader) (any, error) {
	d := xml.NewDecoder(r)
	for {
		tok, err := d.Token()
		if err == io.EOF {
			return nil, fmt.Errorf("llsd: no <llsd> element")
		}
		if err != nil {
			return nil, err
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if start.Name.Local != "llsd" {
			return nil, fmt.Errorf("llsd: expected <llsd>, got <%s>", start.Name.Local)
		}
		return decodeBody(d)
	}
}

func decodeBody(d *xml.Decoder) (any, error) {
	for {
		tok, err := d.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			return decodeValue(d, t)
		case xml.EndElement:
			if t.Name.Local == "llsd" {
				return nil, nil
			}
		}
	}
}

// decodeValue is called just after a value's start element and
// returns once that element has been consumed.
func decodeValue(d *xml.Decoder, start xml.StartElement) (any, error) {
	switch start.Name.Local {
	case "map":
		return decodeMap(d)
	case "array":
		return decodeArray(d)
	}

	s, err := readText(d, start.Name.Local)
	if err != nil {
		return nil, err
	}
	s = strings.TrimSpace(s)

	switch start.Name.Local {
	case "undef":
		return nil, nil
	case "boolean":
		return s == "1" || strings.EqualFold(s, "true"), nil
	case "integer":
		if s == "" {
			return int64(0), nil
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return s, nil
		}
		return n, nil
	case "real":
		if s == "" {
			return float64(0), nil
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return s, nil
		}
		return f, nil
	case "binary":
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return s, nil
		}
		return b, nil
	default:
		// string, uuid, uri, date, and whatever comes next.
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

func decodeMap(d *xml.Decoder) (any, error) {
	out := map[string]any{}
	var key string
	haveKey := false
	for {
		tok, err := d.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "key" {
				s, err := readText(d, "key")
				if err != nil {
					return nil, err
				}
				key, haveKey = strings.TrimSpace(s), true
				continue
			}
			v, err := decodeValue(d, t)
			if err != nil {
				return nil, err
			}
			if haveKey {
				out[key] = v
				haveKey = false
			}
		case xml.EndElement:
			if t.Name.Local == "map" {
				return out, nil
			}
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
			v, err := decodeValue(d, t)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		case xml.EndElement:
			if t.Name.Local == "array" {
				return out, nil
			}
		}
	}
}

// Encode writes v as an LLSD document.
func Encode(v any) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("<llsd>")
	if err := encodeValue(&b, v); err != nil {
		return nil, err
	}
	b.WriteString("</llsd>")
	return b.Bytes(), nil
}

func encodeValue(b *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case nil:
		b.WriteString("<undef/>")
	case string:
		b.WriteString("<string>")
		if err := xml.EscapeText(b, []byte(t)); err != nil {
			return err
		}
		b.WriteString("</string>")
	case bool:
		if t {
			b.WriteString("<boolean>1</boolean>")
		} else {
			b.WriteString("<boolean>0</boolean>")
		}
	case int:
		fmt.Fprintf(b, "<integer>%d</integer>", t)
	case int64:
		fmt.Fprintf(b, "<integer>%d</integer>", t)
	case float64:
		fmt.Fprintf(b, "<real>%g</real>", t)
	case []byte:
		fmt.Fprintf(b, "<binary>%s</binary>", base64.StdEncoding.EncodeToString(t))
	case []string:
		b.WriteString("<array>")
		for _, e := range t {
			if err := encodeValue(b, e); err != nil {
				return err
			}
		}
		b.WriteString("</array>")
	case []any:
		b.WriteString("<array>")
		for _, e := range t {
			if err := encodeValue(b, e); err != nil {
				return err
			}
		}
		b.WriteString("</array>")
	case map[string]any:
		b.WriteString("<map>")
		for k, e := range t {
			b.WriteString("<key>")
			if err := xml.EscapeText(b, []byte(k)); err != nil {
				return err
			}
			b.WriteString("</key>")
			if err := encodeValue(b, e); err != nil {
				return err
			}
		}
		b.WriteString("</map>")
	default:
		return fmt.Errorf("llsd: cannot encode %T", v)
	}
	return nil
}

// ------------------------------------------------------------- accessors

// Map returns v as a map, or nil.
func Map(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

// String reads a string field, converting numbers and booleans.
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

// Int reads an integer field.
//
// It accepts a number that arrived as text, and one that arrived as
// <binary>, which is how the event queue sends packed fields such as
// ParcelFlags -- big endian, as LLSD binary always is.  Returning zero
// for those would be worse than useless: a flags word of zero reads as
// a definite "everything is off" rather than as "I could not tell".
func Int(m map[string]any, key string) int64 {
	switch v := m[key].(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	case bool:
		if v {
			return 1
		}
		return 0
	case []byte:
		if n, ok := binInt(v); ok {
			return n
		}
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err == nil {
			return n
		}
	}
	return 0
}

// binInt reads a big endian unsigned integer of 1, 2, 4 or 8 bytes.
func binInt(b []byte) (int64, bool) {
	switch len(b) {
	case 1, 2, 4, 8:
	default:
		return 0, false
	}
	var n uint64
	for _, c := range b {
		n = n<<8 | uint64(c)
	}
	return int64(n), true
}

// Bytes reads a binary field, or nil.
func Bytes(m map[string]any, key string) []byte {
	b, _ := m[key].([]byte)
	return b
}

// Bool reads a boolean field, accepting the integer and text forms.
func Bool(m map[string]any, key string) bool {
	switch v := m[key].(type) {
	case bool:
		return v
	case int64:
		return v != 0
	case []byte:
		n, ok := binInt(v)
		return ok && n != 0
	case string:
		return v == "1" || strings.EqualFold(v, "true")
	}
	return false
}
