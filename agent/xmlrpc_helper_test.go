package agent

import (
	"bytes"
	"fmt"
	"strconv"

	"github.com/quark-idlemind/slgo/internal/xmlrpc"
)

// decodeMethodCallForTest reads a methodCall of the shape Login.body
// produces and returns its members as strings, so a test can check the
// request we send rather than just that it is well formed.
//
// It goes through xmlrpc.DecodeCall, which is the same code slgod uses
// to read a viewer's login: what the tests here assert about the request
// slgo sends is therefore also a check that the reader on the other side
// understands it.
func decodeMethodCallForTest(b []byte) (map[string]string, error) {
	_, params, err := xmlrpc.DecodeCall(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for k, v := range params {
		switch t := v.(type) {
		case string:
			out[k] = t
		case int64:
			// <int> members, such as address_size, are compared
			// as the digits they were sent as.
			out[k] = strconv.FormatInt(t, 10)
		case []any:
			for i, e := range t {
				out[fmt.Sprintf("%s[%d]", k, i)] = fmt.Sprint(e)
			}
		}
	}
	return out, nil
}
