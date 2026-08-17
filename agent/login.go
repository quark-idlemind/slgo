package agent

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/xml"
	"errors"
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

	// ViewerPassword is what a viewer must type to be handed this
	// session, and it is deliberately not Password.
	//
	// Nothing on the grid has ever seen it.  It authenticates one
	// local thing to another -- a viewer on this machine, asking
	// slgod for a session slgod is already holding -- so making it
	// the account password would mean typing the account password
	// into a viewer's login box, where viewers remember it, for a
	// login that never leaves the loopback interface.
	//
	// Empty means this session cannot be handed to a viewer, which is
	// the right default: a daemon that was not set up for it should
	// not be one password guess away from giving a session out.
	//
	// Stored as its "$1$" digest, like Password.
	ViewerPassword string

	// Start is "last", "home", or a location such as
	// "the test region/128/128/25".
	Start string

	// Group is the group to act as, by name or uuid, and empty means
	// to work it out: the only one joined, or none.
	//
	// It is per account and not per invocation, which is why it lives
	// with the credentials.  A login starts with NO group active and a
	// parcel usually grants building to a group rather than to
	// individuals, so an avatar that builds happily through a viewer
	// cannot rez a thing here -- and the refusal blames the land,
	// which is the wrong place to look.
	//
	// Nothing in this package acts on it.  Group membership is not in
	// the login response at all; it arrives later on the event queue,
	// so whoever holds the session settles this after connecting.  See
	// cmd/slgod.
	Group string

	// Neighbours says whether this avatar holds a circuit to the
	// regions around it, so that it can walk over a border.
	//
	// Per account and not per invocation, like Group above and for the
	// same kind of reason: an avatar somebody drives through a text
	// viewer wants to be able to walk out of the region, and one that
	// runs benchmarks in a single region wants none of the sockets or
	// the traffic that costs.  One daemon holds both at once, so the
	// answer cannot be the process's.
	//
	// A pointer because absent and false are different answers: unset
	// leaves it to whoever starts the session -- slgod's -neighbours
	// flag -- and set wins over that either way.  Nothing in this
	// package acts on it; see cmd/slgod, and agent.Options.Neighbours
	// for what it becomes.
	Neighbours *bool

	// Channel and Version identify the client to Linden Lab.
	Channel string
	Version string

	// MAC and ID0 are the machine the login server is told this is.
	// A viewer reads them off the hardware -- the MAC address, and
	// the first disk's serial number -- and Linden Lab takes the
	// pair as the identity of the computer.  Headless there is no
	// hardware to read, so they are made up; what counts is that
	// they do not change from one login to the next.  slgod keeps a
	// pair for this; see cmd/slgod/machine.go.
	//
	// Neither is sent as it stands.  A viewer sends the md5 hex of
	// each, so MAC is hashed on the way out -- an address here, a
	// digest on the wire -- and ID0 is expected to be a digest
	// already, since the serial it is made from never leaves the
	// machine that has one.  ID0 is left out of the request when it
	// is empty rather than sent blank.
	MAC string
	ID0 string

	// Platform is "mac", "win" or "lnx", and the two that follow
	// describe the operating system: a dotted version, and the name
	// a human would recognise -- "15.7.7" and "macOS 15.7.7".  All
	// three default to the host this is running on, since claiming
	// one system in Platform and another in PlatformString is a
	// worse answer than claiming nothing.
	Platform        string
	PlatformVersion string
	PlatformString  string

	// AddressSize is the pointer width, 32 or 64, and defaults to
	// this build's.
	AddressSize int

	// URL defaults to DefaultLoginURL.
	URL string

	// Options are the extra blocks to ask for.  inventory-root and
	// buddy-list are requested by default.
	//
	// A session that may later hand itself to a viewer wants
	// ViewerOptions here, and wants it at login: the login server
	// answers once, and what was not asked for cannot be asked for
	// afterwards.
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

	// Buddies is the friend list, which arrives here and nowhere
	// else.  Online status is not part of it; that comes later, as
	// OnlineNotification.  See friends.go.
	Buddies []Buddy

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
//
// Reason is the coarse class -- "key" for a wrong password, "presence"
// for a session that has not finished ending, "tos", "critical",
// "update" -- and Message is a sentence written for a person.
//
// MessageID and MessageArgs are the same refusal in a form a program
// can act on, and arrive because the login asked for extended_errors:
// an identifier such as "LoginFailedAccountSuspended", and the values
// that would fill in the blanks of its sentence, such as TIME for a
// suspension or VERSION for a client too old to let in.  The login
// server does not name every refusal, so both may be empty even when
// Message is not.
type LoginError struct {
	Reason      string
	Message     string
	MessageID   string
	MessageArgs map[string]any
}

// RetryableLogin says whether a login that failed this way is worth
// trying again.
//
// Most failures are worth it.  A seed capability answering 404, a
// timeout, a refused connection, a region mid-restart -- none of those
// are about this account, and all of them clear on their own.  So does
// "presence", which is the login server saying the last session has not
// finished ending yet, and which is the single most common reason a
// login made moments after a logout is refused.
//
// What is not worth retrying is a refusal a human has to clear: a wrong
// password, terms of service to accept, a client too old.  Those will
// answer the same way for ever, and asking every couple of minutes for
// ever is how an account gets throttled.
//
// The default here is the opposite of Retryable's, and deliberately.  A
// kick is a statement about a session that WAS alive, so the safe
// reading of one nobody recognises is that somebody meant it.  A login
// failure is usually a failure to reach, so the safe reading of an
// unfamiliar one is that it may clear.
func RetryableLogin(err error) bool {
	var e *LoginError
	if !errors.As(err, &e) {
		return true
	}
	switch e.Reason {
	case "key", "tos", "update", "critical":
		return false
	}
	return true
}

func (e *LoginError) Error() string {
	// The identifier goes first when there is one: it is the part
	// worth matching on, and the sentence can be long.
	what := e.Reason
	if e.MessageID != "" {
		if what == "" {
			what = e.MessageID
		} else {
			what += ", " + e.MessageID
		}
	}
	if what != "" {
		return fmt.Sprintf("login refused (%s): %s", what, e.Message)
	}
	return "login refused: " + e.Message
}

// hashMAC returns the form the login server is actually sent, which is
// not an address: a viewer md5s the six bytes and sends the 32 hex
// digit digest, so the field named "mac" has never held one.  See
// llhasheduniqueid.cpp, and doc/login-parameters.md.
//
// A digest passes through, the way HashPassword passes through a "$1$"
// that is already a digest, so a value copied out of a viewer's log
// works.  Anything that is neither is hashed as it stands, so that
// whatever is configured, what goes over the wire has the shape a
// viewer's does.
func hashMAC(s string) string {
	if len(s) == 32 {
		if _, err := hex.DecodeString(s); err == nil {
			return strings.ToLower(s)
		}
	}
	if hw, err := net.ParseMAC(s); err == nil && len(hw) == 6 {
		sum := md5.Sum(hw)
		return hex.EncodeToString(sum[:])
	}
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// HashPassword returns the "$1$" + md5 form the login server wants,
// passing through a digest that is already in that form.
func HashPassword(p string) string {
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

	// A viewer sends numbers as <int>, and booleans as <int> too --
	// its LLSD layer turns true into 1 on the way out.
	intMember := func(name string, value int) {
		fmt.Fprintf(&b, "<member><name>%s</name><value><int>%d</int></value></member>\n", name, value)
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

	hostKey, hostVersion, hostName := hostPlatform()
	platform := l.Platform
	if platform == "" {
		platform = hostKey
	}
	platformVersion := l.PlatformVersion
	platformString := l.PlatformString
	// The two descriptions of the operating system travel together:
	// filling in one from the host while the other was set by hand
	// would describe two different computers.
	if platformVersion == "" && platformString == "" {
		platformVersion, platformString = hostVersion, hostName
	}
	size := l.AddressSize
	if size == 0 {
		size = addressSize()
	}

	fields := [][2]string{
		{"first", l.First},
		{"last", l.Last},
		{"passwd", HashPassword(l.Password)},
		{"start", startLocation(l.Start)},
		{"version", version},
		{"channel", channel},
		{"mac", hashMAC(mac)},
		{"platform", platform},
		{"agree_to_tos", "true"},
		{"read_critical", "true"},
	}
	// An empty id0 is not the same as no id0: it says the machine has
	// no identity, which no viewer ever reports.  The same goes for
	// an operating system nobody could name.
	if l.ID0 != "" {
		fields = append(fields, [2]string{"id0", l.ID0})
	}
	if platformVersion != "" {
		fields = append(fields, [2]string{"platform_version", platformVersion})
	}
	if platformString != "" {
		fields = append(fields, [2]string{"platform_string", platformString})
	}

	for _, kv := range fields {
		if err := member(kv[0], kv[1]); err != nil {
			return nil, err
		}
	}

	intMember("address_size", size)

	// extended_errors asks the login server to name a refusal --
	// message_id and message_args -- rather than only describe it in
	// a sentence meant for a human.  See LoginError.
	intMember("extended_errors", 1)

	// The login server answers only what it is asked, and a field it was
	// not asked for comes back missing rather than empty -- which reads
	// exactly like a real "you have none". Group membership is NOT
	// available here at all, asked for or not; it arrives later, on the
	// event queue. See noteEvent.
	//
	// buddy-list is asked for always, because this is the only place it
	// is ever offered: who your friends are is not on the circuit and
	// cannot be asked for later, so a login that does not take it here
	// can never know. It costs a few dozen uuids.
	// Deduplicated, because ViewerOptions names both of the defaults
	// and a login server asked twice for the same block is being asked
	// a question about this client rather than about the account.
	opts := dedupe(append([]string{"inventory-root", "buddy-list"}, l.Options...))
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
		e := &LoginError{
			Reason:    a.Reason,
			Message:   a.Message,
			MessageID: getString(m, "message_id"),
		}
		if v, ok := lookup(m, "message_args"); ok {
			e.MessageArgs, _ = v.(map[string]any)
		}
		return a, e
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

	a.Buddies = buddiesFrom(m)

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

// ViewerOptions are the option blocks a viewer asks the login server
// for, in the order Firestorm 7.2.4 sends them.
//
// They are here because of how a handover works.  slgod answers a
// viewer's login by replaying the response the real login server gave
// it, and a response holds only what was asked for -- so a block this
// session did not request is a block the viewer will never see, and
// there is no second chance: the login server answers once, at login,
// and the session then lives for hours.
//
// A viewer that does not get them does not fail cleanly.  Missing
// inventory-lib-root in particular trips an assertion inside the viewer
// with no useful message, which is the sort of fault that gets blamed on
// everything except the login response.
//
// The list is taken from a capture rather than from the documentation,
// which says twenty-three; agent/testdata/firestorm-login.xml is the
// capture, and a test here holds the two together so that a newer viewer
// asking for more is a test failure rather than a mystery.
var ViewerOptions = []string{
	"inventory-root",
	"inventory-skeleton",
	"inventory-lib-root",
	"inventory-lib-owner",
	"inventory-skel-lib",
	"initial-outfit",
	"gestures",
	"display_names",
	"event_categories",
	"event_notifications",
	"classified_categories",
	"adult_compliant",
	"buddy-list",
	"newuser-config",
	"ui-config",
	"advanced-mode",
	"max-agent-groups",
	"map-server-url",
	"voice-config",
	"tutorial_setting",
	"login-flags",
	"global-textures",
	"currency",
	"max_groups",
	"search",
	"destination_guide_url",
	"avatar_picker_url",
}

// dedupe keeps the first of each, so the order asked for is the order
// given.
func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := in[:0:0]
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
