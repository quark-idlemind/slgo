package slate

// The bridge and the probe, run in Go. fakeOps stands where the grid
// stands for the bring-up (gridOps) and plays the two LSL scripts the
// runner installs: what it is handed as source is read for its nonce,
// channels and keys, and what it says goes back through the fake grid as
// chat, in the wire format probe.go speaks. The parsing of a relay line
// and of a send command follows the listings in doc/slate-runner.md, a
// step at a time.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// Invented ids, made with tools/new-id.
var (
	idBridgeItem  = msg.MustParseUUID("24397e57-7e57-c0de-a414-c15fb250cae3")
	idBridgeItem2 = msg.MustParseUUID("26617e57-7e57-c0de-861e-f4c3cd3ae4fe")
	idBridgeWorn  = msg.MustParseUUID("307e7e57-7e57-c0de-0d57-c095ea1c3eff")
	idLid         = msg.MustParseUUID("5c477e57-7e57-c0de-ef35-fb929b7894c2")
	idSlot        = msg.MustParseUUID("6f487e57-7e57-c0de-bde5-82c0d7df0834")
	idForeign     = msg.MustParseUUID("82977e57-7e57-c0de-0545-c537ea25bbf7")
	idSpeaker     = msg.MustParseUUID("872d7e57-7e57-c0de-7221-47dc9b07060c")
	idKeyed       = msg.MustParseUUID("954a7e57-7e57-c0de-00b6-ef827a67b21f")
)

// simProbe is a probe script running in a prim.
type simProbe struct {
	prim    msg.UUID
	name    string
	link    int32
	command int32
	nonce   string
	tester  msg.UUID
	bridge  msg.UUID
	acked   bool
}

// simBridge is the bridge script running in the worn object.
type simBridge struct {
	nonce   string
	control int32
	listens map[int32]bool
}

// fakeOps is the grid as the bring-up sees it.
type fakeOps struct {
	f *fakeGrid

	mu  sync.Mutex
	log []string // what was asked, in order

	// What the test sets.
	items        int // inventory items named slate bridge; one unless set
	wornAlready  bool
	blocked      map[msg.UUID]string
	installErr   map[msg.UUID]error
	uncompiled   map[msg.UUID]bool
	removeErr    error // RemoveScripts
	takeOffErr   error
	silentBridge bool                  // never says ready
	mute         map[msg.UUID]bool     // a probe that never says hello
	helloKey     map[msg.UUID]msg.UUID // a hello that names another prim
	linkOf       map[msg.UUID]int32    // a probe that reports another link number
	extraSay     func(m msg.Message)   // what else the far end does with a send
	onInstall    func(name string)     // called after a script went in
	rejectAll    bool                  // every probe says bad to a send

	worn   *sl.Attached
	bridge *simBridge
	probes map[msg.UUID]*simProbe

	emitMu  sync.Mutex
	ticking bool

	// The simulator's goroutines end with the test: stop is closed, and
	// wg waited for, before the grid's channels are closed.
	stop chan struct{}
	wg   sync.WaitGroup
}

var _ gridOps = (*fakeOps)(nil)

func newOps(f *fakeGrid) *fakeOps {
	o := &fakeOps{f: f, stop: make(chan struct{}), items: 1, probes: map[msg.UUID]*simProbe{},
		blocked: map[msg.UUID]string{}, installErr: map[msg.UUID]error{}, uncompiled: map[msg.UUID]bool{},
		mute: map[msg.UUID]bool{}, helloKey: map[msg.UUID]msg.UUID{}, linkOf: map[msg.UUID]int32{}}
	f.replyTo(o.onSend)
	f.beforeClose = append(f.beforeClose, func() {
		close(o.stop)
		o.wg.Wait()
	})
	return o
}

// cfg is testCfg with this grid in it and the bring-up's waits short.
func (o *fakeOps) cfg() runCfg {
	c := testCfg()
	c.ops = o
	c.ready = 300 * time.Millisecond
	c.hello = 300 * time.Millisecond
	return c
}

func (o *fakeOps) note(format string, args ...any) {
	o.mu.Lock()
	o.log = append(o.log, fmt.Sprintf(format, args...))
	o.mu.Unlock()
}

// calls is what was asked of the grid, in order.
func (o *fakeOps) calls() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.log...)
}

// ------------------------------------------------------------- gridOps

func (o *fakeOps) ScriptsBlocked(_ context.Context, ob *sl.Object) string {
	o.note("blocked? %s", ob.Name)
	return o.blocked[ob.ID]
}

func (o *fakeOps) ObjectsFolder(context.Context) (msg.UUID, error) { return idObjectsFolder, nil }

func (o *fakeOps) FindItem(_ context.Context, folder msg.UUID, name string) (*sl.Item, error) {
	o.note("find %s", name)
	switch o.items {
	case 0:
		return nil, fmt.Errorf("sl: %w", &sl.NameError{Name: name, What: "item", Where: "in folder " + folder.String()})
	case 1:
		return &sl.Item{ID: idBridgeItem, Name: name}, nil
	}
	return nil, fmt.Errorf("sl: %w", &sl.NameError{Name: name, What: "item", Where: "in folder " + folder.String(),
		IDs: []msg.UUID{idBridgeItem, idBridgeItem2}})
}

func (o *fakeOps) WornFromItem(_ context.Context, item msg.UUID) (*sl.Attached, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.worn == nil && o.wornAlready {
		o.worn = &sl.Attached{Object: sl.Object{ID: idBridgeWorn, Local: 900, Name: "slate bridge"}, Item: item, Point: sl.HUDCenter2}
	}
	return o.worn, o.worn != nil
}

func (o *fakeOps) Wear(_ context.Context, it *sl.Item, point int, _ time.Duration) (*sl.Attached, error) {
	o.note("wear %d", point)
	o.mu.Lock()
	defer o.mu.Unlock()
	o.worn = &sl.Attached{Object: sl.Object{ID: idBridgeWorn, Local: 900, Name: "slate bridge"}, Item: it.ID, Point: point}
	return o.worn, nil
}

var (
	reString = func(name string) *regexp.Regexp {
		return regexp.MustCompile(`(?m)^(?:string|key)\s+` + name + `\s*=\s*"([^"]*)";`)
	}
	reInt = func(name string) *regexp.Regexp {
		return regexp.MustCompile(`(?m)^integer\s+` + name + `\s*=\s*(-?\d+);`)
	}
	reList   = regexp.MustCompile(`(?m)^list\s+LISTENS\s*=\s*\[([^\]]*)\];`)
	reNonce  = reString("NONCE")
	reTester = reString("TESTER")
	reBridge = reString("BRIDGE")
	reCmd    = reInt("COMMAND")
	reCtl    = reInt("CONTROL")
)

func group(re *regexp.Regexp, src string) string {
	m := re.FindStringSubmatch(src)
	if m == nil {
		panic("the installed source has no " + re.String())
	}
	return m[1]
}

func (o *fakeOps) InstallScript(ctx context.Context, ob *sl.Object, name, source string, running bool) (*sl.UploadResult, error) {
	o.note("install %s in %s", name, ob.Name)
	if err := o.installErr[ob.ID]; err != nil {
		return nil, err
	}
	if o.uncompiled[ob.ID] {
		return &sl.UploadResult{Compiled: false, Errors: []string{"(4, 10) : ERROR : Syntax error"}}, nil
	}
	switch name {
	case bridgeScript:
		ctl, _ := strconv.Atoi(group(reCtl, source))
		b := &simBridge{nonce: group(reNonce, source), control: int32(ctl), listens: map[int32]bool{}}
		for _, f := range strings.Split(group(reList, source), ",") {
			if f = strings.TrimSpace(f); f != "" {
				n, _ := strconv.Atoi(f)
				b.listens[int32(n)] = true
			}
		}
		o.mu.Lock()
		o.bridge = b
		o.mu.Unlock()
		if !o.silentBridge {
			o.emit(chatMsg("slate bridge", ob.ID, sl.ChatOwner, wireVersion+" "+b.nonce+" ready"))
		}
	case probeScript:
		cmd, _ := strconv.Atoi(group(reCmd, source))
		p := &simProbe{prim: ob.ID, name: ob.Name, command: int32(cmd), nonce: group(reNonce, source)}
		p.tester, _ = msg.ParseUUID(group(reTester, source))
		p.bridge, _ = msg.ParseUUID(group(reBridge, source))
		p.link = o.linkNumber(ctx, ob.ID)
		if n, ok := o.linkOf[ob.ID]; ok {
			p.link = n
		}
		o.mu.Lock()
		o.probes[ob.ID] = p
		start := !o.ticking
		o.ticking = true
		if start {
			o.wg.Add(1)
		}
		o.mu.Unlock()
		o.hello(p)
		if start {
			go o.helloTimer()
		}
	}
	if o.onInstall != nil {
		o.onInstall(name)
	}
	return &sl.UploadResult{Compiled: true}, nil
}

// linkNumber is what llGetLinkNumber says in a prim: 0 alone, 1 for the
// root, then the order of the children.
func (o *fakeOps) linkNumber(ctx context.Context, id msg.UUID) int32 {
	all, _ := o.f.Objects(ctx, "", "")
	var seen *sl.Seen
	for _, s := range all {
		if s.ID == id {
			seen = s
		}
	}
	members, err := linksetOf(all, seen)
	if err != nil || len(members) == 1 {
		return 0
	}
	for i, m := range members {
		if m.ID == id {
			return int32(i + 1)
		}
	}
	return 0
}

func (o *fakeOps) RemoveScripts(_ context.Context, ob *sl.Object, match func(string) bool) (int, error) {
	which := "probe"
	if match(bridgeScript) {
		which = "bridge"
	}
	o.note("remove %s from %s", which, ob.Name)
	if o.removeErr != nil {
		return 0, o.removeErr
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if which == "bridge" {
		o.bridge = nil
	} else {
		delete(o.probes, ob.ID)
	}
	return 1, nil
}

func (o *fakeOps) TakeOff(context.Context, msg.UUID) error {
	o.note("take off")
	if o.takeOffErr != nil {
		return o.takeOffErr
	}
	o.mu.Lock()
	o.worn = nil
	o.mu.Unlock()
	return nil
}

// ------------------------------------------------------------ the scripts

// emit puts a line in front of the session, one at a time.
func (o *fakeOps) emit(m msg.Message) {
	o.emitMu.Lock()
	defer o.emitMu.Unlock()
	o.f.relayUntil(m, o.stop)
}

// report is what a probe says to the tester.
func (o *fakeOps) report(p *simProbe, text string) {
	o.emit(chatMsg(p.name, p.prim, sl.ChatDirect, wireVersion+" "+p.nonce+" "+text))
}

func (o *fakeOps) hello(p *simProbe) {
	if o.mute[p.prim] {
		return
	}
	key := p.prim
	if k, ok := o.helloKey[p.prim]; ok {
		key = k
	}
	o.report(p, fmt.Sprintf("hello %d %s", p.link, key))
}

// helloTimer says hello each tick from every probe that has not heard its
// ack, until the test ends.
func (o *fakeOps) helloTimer() {
	defer o.wg.Done()
	t := time.NewTicker(10 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-o.stop:
			return
		case <-o.f.done:
			return
		case <-t.C:
		}
		o.mu.Lock()
		var due []*simProbe
		for _, p := range o.probes {
			if !p.acked {
				due = append(due, p)
			}
		}
		o.mu.Unlock()
		for _, p := range due {
			o.hello(p)
		}
	}
}

// onSend is what the far end does with a say on CONTROL: the bridge hears
// its owner only there, and relays what follows the channel.
func (o *fakeOps) onSend(m msg.Message) {
	if o.extraSay != nil {
		o.extraSay(m)
	}
	text, ch, ok := says(m)
	o.mu.Lock()
	b := o.bridge
	o.mu.Unlock()
	if !ok || b == nil || ch != b.control {
		return
	}
	head := wireVersion + " " + b.nonce + " relay "
	if !strings.HasPrefix(text, head) {
		return
	}
	rest := text[len(head):]
	target, rest, ok := strings.Cut(rest, " ")
	if !ok || len(target) != 36 {
		return
	}
	channel, rest, ok := strings.Cut(rest, " ")
	if !ok {
		return
	}
	n, err := strconv.Atoi(channel)
	if err != nil || strconv.Itoa(n) != channel {
		return
	}
	id, err := msg.ParseUUID(target)
	if err != nil {
		return
	}
	o.mu.Lock()
	p := o.probes[id]
	o.mu.Unlock()
	if p != nil && p.command == int32(n) {
		o.probeHears(p, rest)
	}
}

// probeHears is the probe's listen event, which hears only the bridge.
func (o *fakeOps) probeHears(p *simProbe, text string) {
	head := wireVersion + " " + p.nonce + " "
	if !strings.HasPrefix(text, head) {
		return
	}
	rest := text[len(head):]
	if v, ok := strings.CutPrefix(rest, "ack "); ok {
		if n, err := canonInt(v); err == nil && n == p.link {
			o.mu.Lock()
			p.acked = true
			o.mu.Unlock()
		}
		return
	}
	rest, ok := strings.CutPrefix(rest, "send ")
	if !ok {
		return
	}
	if o.rejectAll {
		if senderF, _, _ := strings.Cut(rest, " "); senderF == strconv.Itoa(int(p.link)) {
			o.report(p, "bad")
		}
		return
	}
	take := func() (string, bool) {
		f, r, ok := strings.Cut(rest, " ")
		if !ok || f == "" {
			return "", false
		}
		rest = r
		return f, true
	}
	senderF, ok := take()
	if !ok {
		return
	}
	if s, err := canonInt(senderF); err != nil || s != p.link {
		return
	}
	targetF, _ := take()
	numF, _ := take()
	keyF, ok := take()
	target, e1 := canonInt(targetF)
	num, e2 := canonInt(numF)
	if !ok || e1 != nil || e2 != nil {
		o.report(p, "bad")
		return
	}
	key, err := canonKey(keyF)
	if err != nil {
		o.report(p, "bad")
		return
	}
	str, err := unquote(rest)
	if err != nil {
		o.report(p, "bad")
		return
	}
	o.messageLinked(p, target, num, str, key)
}

// messageLinked delivers a link message to the probes the target
// addresses, in the sender's linkset, each of which reports it.
func (o *fakeOps) messageLinked(from *simProbe, target, num int32, str string, key msg.UUID) {
	all, _ := o.f.Objects(context.Background(), "", "")
	var seen *sl.Seen
	for _, s := range all {
		if s.ID == from.prim {
			seen = s
		}
	}
	members, _ := linksetOf(all, seen)
	in := map[msg.UUID]bool{}
	for _, m := range members {
		in[m.ID] = true
	}
	o.mu.Lock()
	var hear []*simProbe
	for _, p := range o.probes {
		if !in[p.prim] {
			continue
		}
		var ok bool
		switch {
		case target == -1:
			ok = true
		case target == -2:
			ok = p != from
		case target == -3:
			ok = p.link != 1 && p.link != 0
		case target == -4:
			ok = p == from
		case target == 1:
			ok = p.link == 1 || p.link == 0
		default:
			ok = p.link == target
		}
		if ok {
			hear = append(hear, p)
		}
	}
	o.mu.Unlock()
	sortProbes(hear)
	for _, p := range hear {
		line := fmt.Sprintf("link %d %d %d %s %s", p.link, from.link, num, key, quote(str))
		if !asciiText(str) || len(wireVersion+" "+p.nonce+" "+line) > 1023 {
			line = fmt.Sprintf("overflow %d %d %d %s %d", p.link, from.link, num, key, len(str))
		}
		o.report(p, line)
	}
}

func sortProbes(ps []*simProbe) {
	for i := 1; i < len(ps); i++ {
		for j := i; j > 0 && ps[j].link < ps[j-1].link; j-- {
			ps[j], ps[j-1] = ps[j-1], ps[j]
		}
	}
}

func asciiText(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c != 9 && c != 10 && (c < 32 || c > 126) {
			return false
		}
	}
	return true
}

// productSays is a product's say on a channel. The bridge hears it when
// it listens there, and owner-says what it heard.
func (o *fakeOps) productSays(ch int32, speaker msg.UUID, name, text string) {
	o.mu.Lock()
	b, w := o.bridge, o.worn
	o.mu.Unlock()
	if b == nil || w == nil || !b.listens[ch] {
		return
	}
	line := fmt.Sprintf("%s %s fwd %d %s %s %s", wireVersion, b.nonce, ch, speaker, quote(name), text)
	if len(line) > 1023 {
		line = fmt.Sprintf("%s %s fwd-overflow %d %s %d", wireVersion, b.nonce, ch, speaker, len(text))
	}
	o.emit(chatMsg("slate bridge", w.Object.ID, sl.ChatOwner, line))
}

// errNoSuch is an error a test makes up for a grid call.
var errNoSuch = errors.New("the grid said no")
