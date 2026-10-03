package slate

// The bridge and the probes: wearing the bridge, installing both scripts,
// the hello map, the protocol lines a run hears, and the cleanup that
// takes it all away again.
// Why: doc/slate-runner.md#probe-and-bridge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// gridOps is what setup and cleanup ask of the grid beyond the session's
// reads. *sl.Session is the production one; a test fakes it.
type gridOps interface {
	ScriptsBlocked(ctx context.Context, o *sl.Object) string
	InstallScript(ctx context.Context, o *sl.Object, name, source string, running bool) (*sl.UploadResult, error)
	RemoveScripts(ctx context.Context, o *sl.Object, match func(name string) bool) (int, error)
	ObjectsFolder(ctx context.Context) (msg.UUID, error)
	FindItem(ctx context.Context, folder msg.UUID, name string) (*sl.Item, error)
	WornFromItem(ctx context.Context, item msg.UUID) (*sl.Attached, bool)
	Wear(ctx context.Context, it *sl.Item, point int, timeout time.Duration) (*sl.Attached, error)
	TakeOff(ctx context.Context, item msg.UUID) error
}

var _ gridOps = (*sl.Session)(nil)

// The setup budgets of the bring-up, with the production value when a
// config leaves them zero.
// Why: doc/slate-runner.md#timeouts-and-setup-budgets
const (
	defaultReadyWait = 30 * time.Second // the bridge's ready line
	defaultHelloWait = 30 * time.Second // every probe's hello
	defaultWearWait  = 40 * time.Second // Session.Wear
)

func (c runCfg) readyFor() time.Duration { return orDefault(c.ready, defaultReadyWait) }
func (c runCfg) helloFor() time.Duration { return orDefault(c.hello, defaultHelloWait) }
func (c runCfg) wearFor() time.Duration  { return orDefault(c.wear, defaultWearWait) }

// evWire is a protocol line, which is never chat.
const evWire eventKind = 200

// probePrim is one prim a probe was installed in.
type probePrim struct {
	b       *binding
	seen    *sl.Seen
	link    int32
	helloed bool
}

// probeRun is the bridge and probes of one run. It is nil for a file
// that has no probe and no listen.
type probeRun struct {
	nonce   string
	control int32
	listens []int32
	command map[string]int32 // a probed binding's COMMAND channel

	worn     *sl.Attached // the bridge, once worn
	bridgeIn bool         // the bridge script went, or was tried, in
	ready    bool

	state     runState     // what classifies a line
	prims     []*probePrim // in install order
	installed []*probePrim // those a script went into, or was tried in
	byID      map[msg.UUID]*probePrim
	links     map[string]map[int32]*probePrim // by binding name, then link

	done bool // cleanup has run

	collecting bool   // hellos are being gathered
	conflict   string // two prims of one linkset hello as one link
}

// linkName is the name the runner reads and touches one link of a probed
// binding under. It has a space, so no script name is ever the same.
func linkName(name string, link int32) string { return fmt.Sprintf("%s link %d", name, link) }

// primAt is the prim at a link of a probed binding, or why there is none.
func (r *runner) primAt(b *binding, link int64) (*probePrim, error) {
	pr := r.pr
	if pr == nil || pr.links[b.name] == nil {
		return nil, fmt.Errorf("%s has no probe", b.name)
	}
	if pp := pr.links[b.name][int32(link)]; pp != nil && int64(int32(link)) == link {
		return pp, nil
	}
	var have []string
	for n := int32(0); n <= int32(len(pr.prims)); n++ {
		if pr.links[b.name][n] != nil {
			have = append(have, fmt.Sprint(n))
		}
	}
	return nil, fmt.Errorf("link %d is not one of the probed links of %s (%s)", link, b.name, strings.Join(have, " "))
}

// setupProbe is the bring-up, in the one order that works: the bridge is
// worn and opens its listens, and only then do the probes get its key.
// Why: doc/slate-runner.md#bring-up
func (r *runner) setupProbe(ctx context.Context) error {
	if len(r.s.Probes) == 0 && len(r.s.Listens) == 0 {
		return nil
	}
	pr := &probeRun{
		command: map[string]int32{},
		byID:    map[msg.UUID]*probePrim{},
		links:   map[string]map[int32]*probePrim{},
	}
	r.pr = pr
	var probed []*binding
	for _, p := range r.s.Probes {
		b := r.bind[p.Name.Text]
		if b == nil {
			return &setupError{fmt.Sprintf("%s is not an object", p.Name.Text)}
		}
		probed = append(probed, b)
		for _, m := range b.members {
			if m.PCode != pcodePrim {
				continue
			}
			pp := &probePrim{b: b, seen: m}
			pr.prims = append(pr.prims, pp)
			pr.byID[m.ID] = pp
		}
	}
	for _, pp := range pr.prims {
		if why := r.ops.ScriptsBlocked(ctx, &pp.seen.Object); why != "" {
			return &setupError{why}
		}
	}

	item, err := r.findBridge(ctx)
	if err != nil {
		return err
	}
	worn, ok := r.ops.WornFromItem(ctx, item.ID)
	if !ok {
		worn, err = r.ops.Wear(ctx, item, sl.HUDCenter2|sl.AttachAdd, r.cfg.wearFor())
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &setupError{fmt.Sprintf("wearing %q (%s): %v", bridgeScript, r.cfg.wearFor(), err)}
		}
	}
	pr.worn = worn

	var listens []int32
	for _, l := range r.s.Listens {
		listens = append(listens, int32(l.Channel.Value))
	}
	d := newDraws(r.cfg.draw, listens)
	if pr.nonce, err = d.Nonce(); err != nil {
		return &setupError{err.Error()}
	}
	if pr.control, err = d.Control(); err != nil {
		return &setupError{err.Error()}
	}
	for _, b := range probed {
		if pr.command[b.name], err = d.Command(); err != nil {
			return &setupError{err.Error()}
		}
	}
	pr.listens = listens
	pr.state = runState{Nonce: pr.nonce, Probes: map[msg.UUID]bool{}, Bridge: worn.Object.ID}

	src, err := bridgeSource(pr.nonce, pr.control, listens)
	if err != nil {
		return &setupError{err.Error()}
	}
	pr.bridgeIn = true
	if err := r.install(ctx, &worn.Object, bridgeScript, src, "the bridge"); err != nil {
		return err
	}
	ok, err = r.await(ctx, r.cfg.readyFor(), func() bool { return pr.ready })
	if err != nil {
		return err
	}
	if !ok {
		return &setupError{fmt.Sprintf("bridge did not say ready within %s", r.cfg.readyFor())}
	}

	if len(pr.prims) == 0 {
		return nil
	}
	r.printf("slate: setup: installing the probe in %s", prims(len(pr.prims)))
	pr.collecting = true
	for _, pp := range pr.prims {
		pr.state.Probes[pp.seen.ID] = true
		src, err := probeSource(pr.nonce, r.sess.Me(), worn.Object.ID, pr.command[pp.b.name])
		if err != nil {
			return &setupError{err.Error()}
		}
		pr.installed = append(pr.installed, pp)
		what := fmt.Sprintf("the probe in %q", pp.seen.Name)
		if err := r.install(ctx, &pp.seen.Object, probeScript, src, what); err != nil {
			return err
		}
	}
	ok, err = r.await(ctx, r.cfg.helloFor(), func() bool { return pr.conflict != "" || pr.hellos() })
	pr.collecting = false
	if err != nil {
		return err
	}
	if pr.conflict != "" {
		return &setupError{pr.conflict}
	}
	if !ok {
		for _, pp := range pr.prims {
			if !pp.helloed {
				return &setupError{fmt.Sprintf("probe in %q did not hello from %s within %s; ScriptsBlocked was empty and that is not proof the parcel will run the script (doc/ground.md)", pp.seen.Name, pp.seen.ID, r.cfg.helloFor())}
			}
		}
	}
	for _, pp := range pr.prims {
		if pr.links[pp.b.name] == nil {
			pr.links[pp.b.name] = map[int32]*probePrim{}
		}
		pr.links[pp.b.name][pp.link] = pp
		// Read and touched under a name of its own, so the rest of the
		// runner needs no notion of a link.
		r.bind[linkName(pp.b.name, pp.link)] = &binding{name: linkName(pp.b.name, pp.link), seen: pp.seen}
		r.printf("slate: probe %s link %d %s", pp.b.name, pp.link, pp.seen.ID)
	}
	for _, pp := range pr.prims {
		line, err := ackLine(pr.nonce, pp.seen.ID, pr.command[pp.b.name], pp.link)
		if err == nil {
			err = r.sess.Say(ctx, line, pr.control)
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &setupError{fmt.Sprintf("the ack to %q was not sent: %v", pp.seen.Name, err)}
		}
	}
	return nil
}

// findBridge is the one item named slate bridge in Objects.
func (r *runner) findBridge(ctx context.Context) (*sl.Item, error) {
	folder, err := r.ops.ObjectsFolder(ctx)
	if err == nil {
		var it *sl.Item
		if it, err = r.ops.FindItem(ctx, folder, bridgeScript); err == nil {
			return it, nil
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var ne *sl.NameError
	if errors.As(err, &ne) && len(ne.IDs) == 0 {
		return nil, &setupError{fmt.Sprintf("no %q in inventory; run slate -make-bridge once where the tester may build", bridgeScript)}
	}
	return nil, &setupError{err.Error()}
}

// install puts a script in an object. A compile failure and an error are
// both setup failures, and the compiler's lines are in the first.
func (r *runner) install(ctx context.Context, o *sl.Object, name, source, what string) error {
	up, err := r.ops.InstallScript(ctx, o, name, source, true)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &setupError{fmt.Sprintf("cannot install %s: %v", what, err)}
	}
	for _, w := range up.Warnings {
		r.printf("slate: setup: warning: %s", w)
	}
	if !up.Compiled {
		return &setupError{fmt.Sprintf("cannot install %s: the script did not compile: %s", what, strings.Join(up.Errors, "; "))}
	}
	return nil
}

// await waits, on the runner's own wake-ups, for done, and says whether it
// held before the budget ran out.
func (r *runner) await(ctx context.Context, budget time.Duration, done func() bool) (bool, error) {
	until := time.Now().Add(budget)
	for {
		if err := r.drain(); err != nil {
			return false, err
		}
		if done() {
			return true, nil
		}
		if !time.Now().Before(until) {
			return false, nil
		}
		if err := r.wait(ctx, until); err != nil {
			return false, err
		}
	}
}

// hellos says every prim has helloed.
func (pr *probeRun) hellos() bool {
	for _, pp := range pr.prims {
		if !pp.helloed {
			return false
		}
	}
	return true
}

// noteHello records a hello. The same prim again with the same link is
// its timer. Two prims of one linkset with one link number fail setup.
func (pr *probeRun) noteHello(pp *probePrim, link int32) {
	pp.link, pp.helloed = link, true
	for _, o := range pr.prims {
		if o != pp && o.b == pp.b && o.helloed && o.link == link && pr.conflict == "" {
			pr.conflict = fmt.Sprintf("two prims of %q hello as link %d: %s and %s", pp.b.name, link, o.seen.ID, pp.seen.ID)
		}
	}
}

// observeWire takes a protocol line into the log, and says it did. A line
// that is the probe's or the bridge's by source and type and does not
// parse is printed as a warning and ignored, so it is neither chat nor a
// stimulus; so is a hello whose key is not its source's.
func (r *runner) observeWire(l sl.Line) bool {
	pr := r.pr
	if pr == nil {
		return false
	}
	m, err := pr.state.Classify(l.Type, l.Source, l.Text)
	if err != nil {
		r.printf("slate: warning: ignored a protocol line from %s: %v", l.Source, err)
		return true
	}
	if m.Kind == wireNone {
		return false
	}
	ev := &event{kind: evWire, at: time.Now(), line: l, wire: m, prim: pr.byID[l.Source]}
	switch m.Kind {
	case wireReady:
		pr.ready = true
	case wireHello:
		if m.Prim != l.Source {
			r.printf("slate: warning: ignored a hello from %s that names %s", l.Source, m.Prim)
			return true
		}
		if pr.collecting {
			pr.noteHello(ev.prim, m.Link)
		}
	case wireLink:
		ev.text = fmt.Sprintf("probe link %s heard-by %d from %d num %d%s %q",
			r.probeName(ev.prim), m.HeardBy, m.Sender, m.Num, keyText(m.Key), m.Text)
	case wireOverflow:
		ev.text = fmt.Sprintf("probe overflow %s heard-by %d bytes %d", r.probeName(ev.prim), m.HeardBy, m.Length)
	case wireBad:
		ev.text = fmt.Sprintf("probe bad %s link %d", r.probeName(ev.prim), ev.prim.link)
	case wireFwd:
		ev.text = fmt.Sprintf("chat channel %d from %s: %q", m.Channel, r.fwdWho(m), m.Tail)
	case wireFwdOverflow:
		ev.text = fmt.Sprintf("probe fwd-overflow channel %d bytes %d", m.Channel, m.Length)
	}
	r.log.add(ev)
	r.printEvent(ev)
	return true
}

// keyText is a link key for the transcript, written only when it is not
// the null key.
func keyText(k msg.UUID) string {
	if k.IsZero() {
		return ""
	}
	return " key " + k.String()
}

// probeName is the script's name for the prim a probe is in.
func (r *runner) probeName(pp *probePrim) string {
	if pp == nil {
		return "?"
	}
	return pp.b.name
}

// fwdWho is who a forwarded line is from: the script's name for a bound
// prim, the tester, else the name the bridge heard, which is the displayed
// name of an avatar or any name at all for an object, and so labelled as an
// object's.
func (r *runner) fwdWho(m wireMessage) string {
	switch {
	case m.Speaker == r.sess.Me():
		return r.testerName()
	}
	if who := r.whoPrim(m.Speaker, nil); who != "" {
		return who
	}
	return sl.SenderObject.Label(m.Name)
}

// isProtocol says a line is the probe's or the bridge's own and no
// product's.
func (r *runner) isProtocol(l sl.Line) bool {
	if r.pr == nil {
		return false
	}
	m, err := r.pr.state.Classify(l.Type, l.Source, l.Text)
	return err != nil || m.Kind != wireNone
}

// cleanup undoes what setup did, once, on every exit after the bridge
// began: the probes, then the bridge script, then the bridge is taken
// off. Every failure is a warning. It runs after a cancel, so it does not
// take the run's own context.
// Why: doc/slate-runner.md#cleanup-and-what-a-failure-leaves-behind
func (r *runner) cleanup(ctx context.Context) {
	pr := r.pr
	if pr == nil || pr.done {
		return
	}
	pr.done = true
	ctx = context.WithoutCancel(ctx)
	warn := func(format string, args ...any) {
		r.printf("slate: cleanup: warning: "+format, args...)
	}
	if len(pr.installed) > 0 {
		r.printf("slate: cleanup: removed the probe from %s, settling 6s after each", prims(len(pr.installed)))
	}
	for _, pp := range pr.installed {
		if _, err := r.ops.RemoveScripts(ctx, &pp.seen.Object, func(n string) bool { return n == probeScript }); err != nil {
			warn("the probe in %q was not removed: %v", pp.seen.Name, err)
		}
	}
	if pr.worn == nil {
		return
	}
	if pr.bridgeIn {
		if _, err := r.ops.RemoveScripts(ctx, &pr.worn.Object, func(n string) bool { return n == bridgeScript }); err != nil {
			warn("the bridge script was not removed: %v", err)
		}
	}
	if err := r.ops.TakeOff(ctx, pr.worn.Item); err != nil {
		warn("the bridge was not taken off: %v", err)
	}
}

// prims is n and the word, one prim or several.
func prims(n int) string {
	if n == 1 {
		return "1 prim"
	}
	return fmt.Sprintf("%d prims", n)
}
