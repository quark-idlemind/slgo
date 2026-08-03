package agent

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// DefaultLoginURL is the Agni (main grid) login endpoint.
const DefaultLoginURL = "https://login.agni.lindenlab.com/cgi-bin/login.cgi"

// Login is an XML-RPC login_to_simulator request.
type Login struct {
	First    string
	Last     string
	Password string // plain text, or an existing "$1$" + md5 hex digest

	// Start is "last", "home", or a location such as
	// "the test region/128/128/25".
	Start string

	// Channel and Version identify the client to Linden Lab.
	Channel string
	Version string

	MAC      string
	Platform string

	// URL defaults to DefaultLoginURL.
	URL string

	// Options are the extra blocks to ask for.  inventory-root is
	// requested by default.
	Options []string

	HTTP *http.Client
}

// Account is what the login server hands back.
type Account struct {
	AgentID         msg.UUID
	SessionID       msg.UUID
	SecureSessionID msg.UUID
	CircuitCode     uint32

	SimIP   net.IP
	SimPort int

	SeedCapability string
	InventoryRoot  msg.UUID

	FirstName string
	LastName  string

	// Message is the login server's message of the day, and Reason
	// is set when it refuses.
	Message string
	Reason  string

	RegionX uint32
	RegionY uint32

	// Raw is the whole decoded response, so nothing that is not
	// modelled above is lost.
	Raw map[string]any
}

// Name is the agent's full name, as it appears to other residents.
func (a *Account) Name() string { return a.FirstName + " " + a.LastName }

// SimAddr is the simulator's UDP address.
func (a *Account) SimAddr() *net.UDPAddr {
	return &net.UDPAddr{IP: a.SimIP, Port: a.SimPort}
}

// LoginError is a refusal from the login server.
type LoginError struct {
	Reason  string
	Message string
}

func (e *LoginError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("login refused (%s): %s", e.Reason, e.Message)
	}
	return "login refused: " + e.Message
}

// hashPassword returns the "$1$" + md5 form the login server wants,
// passing through a digest that is already in that form.
func hashPassword(p string) string {
	if len(p) == 35 && strings.HasPrefix(p, "$1$") {
		if _, err := hex.DecodeString(p[3:]); err == nil {
			return p
		}
	}
	sum := md5.Sum([]byte(p))
	return "$1$" + hex.EncodeToString(sum[:])
}

// startLocation turns a location into the form the login server wants.
// "last" and "home" pass through; anything else becomes
// uri:Region&x&y&z.  The ampersands are escaped when the body is built,
// not here.
func startLocation(s string) string {
	if s == "" {
		return "last"
	}
	if s == "last" || s == "home" {
		return s
	}
	parts := strings.Split(s, "/")
	x, y, z := 128, 128, 21
	if len(parts) > 1 {
		if n, err := strconv.Atoi(parts[1]); err == nil {
			x = n
		}
	}
	if len(parts) > 2 {
		if n, err := strconv.Atoi(parts[2]); err == nil {
			y = n
		}
	}
	if len(parts) > 3 {
		if n, err := strconv.Atoi(parts[3]); err == nil {
			z = n
		}
	}
	return fmt.Sprintf("uri:%s&%d&%d&%d", parts[0], x, y, z)
}

func (l Login) body() ([]byte, error) {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0"?>` + "\n")
	b.WriteString(`<methodCall><methodName>login_to_simulator</methodName>`)
	b.WriteString(`<params><param><value><struct>` + "\n")

	member := func(name, value string) error {
		b.WriteString("<member><name>")
		b.WriteString(name)
		b.WriteString("</name><value><string>")
		if err := xml.EscapeText(&b, []byte(value)); err != nil {
			return err
		}
		b.WriteString("</string></value></member>\n")
		return nil
	}

	version := l.Version
	if version == "" {
		version = "slgo 0.1"
	}
	channel := l.Channel
	if channel == "" {
		channel = "slgo"
	}
	mac := l.MAC
	if mac == "" {
		mac = "00:EA:7C:A7:DE:AD"
	}
	platform := l.Platform
	if platform == "" {
		platform = "lnx"
	}

	for _, kv := range [][2]string{
		{"first", l.First},
		{"last", l.Last},
		{"passwd", hashPassword(l.Password)},
		{"start", startLocation(l.Start)},
		{"version", version},
		{"channel", channel},
		{"mac", mac},
		{"platform", platform},
		{"agree_to_tos", "true"},
		{"read_critical", "true"},
	} {
		if err := member(kv[0], kv[1]); err != nil {
			return nil, err
		}
	}

	// The login server answers only what it is asked, and a field it was
	// not asked for comes back missing rather than empty -- which reads
	// exactly like a real "you have none". Group membership is NOT
	// available here at all, asked for or not; it arrives later, on the
	// event queue. See noteEvent.
	opts := append([]string{"inventory-root"}, l.Options...)
	b.WriteString("<member><name>options</name><value><array><data>\n")
	for _, o := range opts {
		b.WriteString("<value><string>")
		if err := xml.EscapeText(&b, []byte(o)); err != nil {
			return nil, err
		}
		b.WriteString("</string></value>\n")
	}
	b.WriteString("</data></array></value></member>\n")

	b.WriteString(`</struct></value></param></params></methodCall>`)
	return b.Bytes(), nil
}

// Do performs the login.
func (l Login) Do(ctx context.Context) (*Account, error) {
	if l.First == "" || l.Last == "" {
		return nil, fmt.Errorf("agent: login needs a first and last name")
	}
	url := l.URL
	if url == "" {
		url = DefaultLoginURL
	}
	hc := l.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}

	body, err := l.body()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "text/xml")
	req.Header.Set("Accept", "text/xml")

	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("agent: login request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("agent: login returned %s: %s", resp.Status, strings.TrimSpace(string(snippet)))
	}

	v, err := decodeResponse(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("agent: login response: %w", err)
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("agent: login response was %T, wanted a struct", v)
	}
	return accountFrom(m)
}

func accountFrom(m map[string]any) (*Account, error) {
	a := &Account{
		Raw:       m,
		Message:   getString(m, "message"),
		Reason:    getString(m, "reason"),
		FirstName: unquote(getString(m, "first_name")),
		LastName:  unquote(getString(m, "last_name")),
	}

	if getString(m, "login") != "true" {
		return a, &LoginError{Reason: a.Reason, Message: a.Message}
	}

	var err error
	if a.AgentID, err = uuidField(m, "agent_id"); err != nil {
		return a, err
	}
	if a.SessionID, err = uuidField(m, "session_id"); err != nil {
		return a, err
	}
	if a.SecureSessionID, err = uuidField(m, "secure_session_id"); err != nil {
		return a, err
	}

	code, ok := getInt(m, "circuit_code")
	if !ok {
		return a, fmt.Errorf("agent: login response has no usable circuit_code")
	}
	a.CircuitCode = uint32(code)

	ip := getString(m, "sim_ip")
	if a.SimIP = net.ParseIP(ip); a.SimIP == nil {
		return a, fmt.Errorf("agent: sim_ip %q is not an address", ip)
	}
	port, ok := getInt(m, "sim_port")
	if !ok || port <= 0 || port > 65535 {
		return a, fmt.Errorf("agent: sim_port %v is not a port", m["sim_port"])
	}
	a.SimPort = int(port)

	a.SeedCapability = getString(m, "seed_capability")
	if x, ok := getInt(m, "region_x"); ok {
		a.RegionX = uint32(x)
	}
	if y, ok := getInt(m, "region_y"); ok {
		a.RegionY = uint32(y)
	}

	// inventory-root is an array holding one struct with a
	// folder_id.
	if v, ok := lookup(m, "inventory-root"); ok {
		if arr, ok := v.([]any); ok && len(arr) > 0 {
			if e, ok := arr[0].(map[string]any); ok {
				if u, err := msg.ParseUUID(getString(e, "folder_id")); err == nil {
					a.InventoryRoot = u
				}
			}
		}
	}
	return a, nil
}

func uuidField(m map[string]any, key string) (msg.UUID, error) {
	s := getString(m, key)
	u, err := msg.ParseUUID(s)
	if err != nil {
		return msg.UUID{}, fmt.Errorf("agent: %s: %w", key, err)
	}
	return u, nil
}
