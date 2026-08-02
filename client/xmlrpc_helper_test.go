package client

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
)

// decodeMethodCallForTest reads a methodCall of the shape Login.body
// produces and returns its members as strings, so a test can check the
// request we send rather than just that it is well formed.
func decodeMethodCallForTest(b []byte) (map[string]string, error) {
	d := xml.NewDecoder(bytes.NewReader(b))
	out := map[string]string{}
	for {
		tok, err := d.Token()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "member" {
			continue
		}
		k, v, err := decodeMember(d)
		if err != nil {
			return nil, err
		}
		switch t := v.(type) {
		case string:
			out[k] = t
		case []any:
			for i, e := range t {
				out[fmt.Sprintf("%s[%d]", k, i)] = fmt.Sprint(e)
			}
		}
	}
}
