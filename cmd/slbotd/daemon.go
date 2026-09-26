package main

// The daemon: one attendant per avatar, and the connection to slgod
// they are brought up through.
//
// slbotd holds no credentials and performs no login.  It asks slgod to
// host a profile and then attaches to the session slgod is holding, so
// the passwords stay where they already were and an slbotd that is
// killed leaves every avatar logged in.  That division is the whole
// design: slgod owns the grid connection and supervises it, and this
// owns what is done with it.
//
// What is left for an attendant to do about staying connected is
// therefore small and worth being precise about.  slgod reconnects a
// session that drops; it does NOT reconnect one that was logged out on
// purpose, and it says so -- "it will not come back until asked for by
// name".  An attendant that forced such a session back up would be
// fighting whoever is using that avatar in a viewer, which is exactly
// what the deliberate logout exists to prevent.  So a refusal of that
// kind stops the attendant asking, until somebody says to force it.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/quark-idlemind/slgo/client"
	"github.com/quark-idlemind/slgo/msg"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
	"github.com/quark-idlemind/slgo/sl"
)

// Backoff bounds how often an attendant asks for a session it cannot
// get.  A login refused for a reason that will clear -- the grid is
// down, slgod is restarting -- is worth asking about again; asking
// every second is how a login server comes to throttle an account.
const (
	backoffFirst = 2 * time.Second
	backoffMax   = 2 * time.Minute

	// settled is how long a session has to last before the attendant
	// believes the trouble is over and starts from the first delay
	// again.  Shorter than this and a session that comes up and dies
	// immediately would reset the backoff on every attempt, which is
	// no backoff at all.
	settled = 2 * time.Minute

	// HeldRecheck is how often an attendant looks to see whether an
	// avatar somebody stopped on purpose has been started again.
	//
	// Half a minute is short enough that "slsh login example" is
	// followed by the daemon picking it up while you are still
	// watching, and long enough that an avatar left stopped for a
	// week costs one small call every thirty seconds and nothing
	// else.
	HeldRecheck = 30 * time.Second
)

// daemon is the whole of a running slbotd.
type daemon struct {
	cfg  Config
	addr string
	logf func(string, ...any)

	// quiet drops the lines that are only somebody talking.  An avatar
	// standing in a busy region hears a great deal, and a daemon meant
	// to run for weeks should not fill a disk with other people's small
	// talk.  Everything else -- commands, offers, and every word about
	// the connection -- is kept whatever this says.
	quiet bool

	// ctl is the connection slgod's own calls go over: hosting a
	// profile, and listing what it holds.  It is separate from the
	// sessions because it is needed before any session exists, and
	// because it must outlive one that ends.
	ctlMu sync.Mutex
	ctl   *client.Conn

	// chat is the conversation machinery, or nil when no model is
	// configured, and audience decides who an avatar will talk to.
	// Both live on the daemon rather than on an attendant because the
	// model has one set of slots however many avatars are sharing it.
	chat     *Chatter
	audience Audience

	// host and attach are how an attendant reaches slgod: ask for a
	// profile to be brought up, and attach to the session it is
	// holding.  Fields rather than plain calls because they are the
	// seam between talking to slgod and deciding what to do about the
	// answer, and the deciding is the part with rules in it -- when to
	// try again, how long to wait, and when to stop asking altogether.
	// Those rules are worth reading and testing without an slgod to
	// try them against.
	host   func(ctx context.Context, name string, force bool) (deliberate bool, err error)
	attach func(ctx context.Context, name string) (*sl.Session, error)
	agents func(ctx context.Context) ([]*pb.AgentInfo, error)

	mu   sync.Mutex
	bots map[string]*bot

	// order is the avatars as the configuration named them, for Bots
	// and so for everything that goes through them in turn.  A map has
	// no order to offer.
	order []string
}

func newDaemon(cfg Config, addr string, logf func(string, ...any)) *daemon {
	d := &daemon{
		cfg:  cfg,
		addr: addr,
		logf: logf,
		bots: map[string]*bot{},
	}
	d.host = d.hostThroughSlgod
	d.attach = d.attachThroughSlgod
	d.agents = d.agentsFromSlgod
	d.audience = silentAudience()
	for _, name := range cfg.Avatars {
		d.bots[name] = newBot(d, name)
		d.order = append(d.order, name)
	}
	return d
}

// Run brings every attendant up and returns when the context ends.
func (d *daemon) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, name := range d.order {
		b := d.bots[name]
		wg.Add(1)
		go func() {
			defer wg.Done()
			b.run(ctx)
		}()
	}
	wg.Wait()
	d.closeControl()
}

// Bot is the attendant for a profile, and whether there is one.
func (d *daemon) Bot(name string) (*bot, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for n, b := range d.bots {
		if strings.EqualFold(n, name) {
			return b, true
		}
	}
	return nil, false
}

// AvatarFor names the profile an agent id belongs to, when it is one
// this daemon is attending.
//
// By id and not by name, because this is asked about the sender of a
// message and a name is what somebody can call themselves.  An
// attendant with no session answers for nobody, which is right: it is
// not driving that avatar just now, so whoever is using it is a person.
func (d *daemon) AvatarFor(id msg.UUID) (string, bool) {
	if id.IsZero() {
		return "", false
	}
	for _, b := range d.Bots() {
		s := b.Session()
		if s != nil && s.Me() == id {
			return b.Name(), true
		}
	}
	return "", false
}

// Bots is every attendant, in the order the configuration named them.
func (d *daemon) Bots() []*bot {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]*bot, 0, len(d.order))
	for _, n := range d.order {
		out = append(out, d.bots[n])
	}
	return out
}

// control hands back the connection slgod's own calls go over, dialling
// one if there is none.
//
// Dialled lazily and dropped on failure, because slgod restarts: a
// connection held from startup would be dead after the first restart and
// every host would fail against it for ever.
func (d *daemon) control(ctx context.Context) (*client.Conn, error) {
	d.ctlMu.Lock()
	defer d.ctlMu.Unlock()
	if d.ctl != nil {
		select {
		case <-d.ctl.Done():
			d.ctl.Close()
			d.ctl = nil
		default:
			return d.ctl, nil
		}
	}
	dial, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	c, err := client.Dial(dial, d.addr)
	if err != nil {
		return nil, fmt.Errorf("cannot reach slgod at %s: %w", d.addr, err)
	}
	d.ctl = c
	return c, nil
}

// dropControl forgets a control connection that has failed, so the next
// call dials a fresh one.
func (d *daemon) dropControl(c *client.Conn) {
	d.ctlMu.Lock()
	defer d.ctlMu.Unlock()
	if d.ctl == c {
		d.ctl = nil
		c.Close()
	}
}

func (d *daemon) closeControl() {
	d.ctlMu.Lock()
	defer d.ctlMu.Unlock()
	if d.ctl != nil {
		d.ctl.Close()
		d.ctl = nil
	}
}

// Agents is what slgod says it holds, which is more than this daemon
// attends to: a profile slbotd was never told about is still listed,
// because "I have never heard of that avatar" and "that avatar is not
// mine to drive" are different answers.
func (d *daemon) Agents(ctx context.Context) ([]*pb.AgentInfo, error) { return d.agents(ctx) }

// agentsFromSlgod is Agents as it really is: one call over the control
// connection.
func (d *daemon) agentsFromSlgod(ctx context.Context) ([]*pb.AgentInfo, error) {
	c, err := d.control(ctx)
	if err != nil {
		return nil, err
	}
	as, err := c.ListAgents(ctx)
	if err != nil {
		d.dropControl(c)
		return nil, err
	}
	return as, nil
}

// stoppedAt slgod reports whether an avatar is still down on purpose.
//
// A read and not a request.  The difference is the whole point: asking
// to host a stopped avatar over and over would be the attendant
// arguing with the person who stopped it, where this only watches for
// somebody bringing it back.
//
// A slgod that cannot be reached is not an answer either way, so it is
// treated as still stopped: the attendant waits and asks again rather
// than concluding from a network error that an avatar is free.
func (d *daemon) stoppedAt(ctx context.Context, name string) bool {
	as, err := d.Agents(ctx)
	if err != nil {
		return true
	}
	for _, a := range as {
		if strings.EqualFold(a.GetName(), name) {
			return a.GetState() == pb.AgentInfo_STOPPED
		}
	}
	// Not listed at all.  slgod lists what it could start as well as
	// what it holds, so this is a profile it has never heard of, and
	// asking for it is the attendant's business rather than this one's.
	return false
}

// hostThroughSlgod asks slgod to bring a profile up, and says whether
// the refusal was the deliberate kind.
//
// The deliberate kind is the one that matters.  slgod refuses to
// restart a session somebody logged out on purpose -- the usual reason
// being that they are using that avatar in a viewer -- and an attendant
// that kept asking would be fighting them for it.
func (d *daemon) hostThroughSlgod(ctx context.Context, name string, force bool) (deliberate bool, err error) {
	c, err := d.control(ctx)
	if err != nil {
		return false, err
	}
	call, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if _, err := c.Host(call, name, force); err != nil {
		if status.Code(err) == codes.FailedPrecondition {
			return true, err
		}
		// Anything that is not slgod answering is slgod not being
		// there, and the connection goes with it.
		if status.Code(err) == codes.Unavailable {
			d.dropControl(c)
		}
		return false, err
	}
	return false, nil
}

// attachThroughSlgod attaches to the session slgod is holding.
func (d *daemon) attachThroughSlgod(ctx context.Context, name string) (*sl.Session, error) {
	dial, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	// Weak: this daemon ATTENDS the avatars rather than uses them, so
	// its being attached must not be the reason "slsh logout" needs
	// --force.  It is attached to every avatar all day; if it counted,
	// the flag would always be needed and would stop being read.
	return sl.DialWeak(dial, d.addr, name)
}

// ------------------------------------------------------------- attendant

// state is what an attendant is doing, for the listing that says so.
type state string

const (
	stateStarting state = "starting"
	stateHosting  state = "asking slgod to host it"
	stateAttached state = "attached"
	stateWaiting  state = "waiting to try again"
	stateHeldBack state = "stopped deliberately; not asking again"
	stateGone     state = "shutting down"
)

// bot is one avatar being attended to.
type bot struct {
	d    *daemon
	name string

	// chatJobs limits how many conversations this avatar answers at
	// once.  Its own budget rather than a share of jobs, because a
	// reply takes seconds and a command takes milliseconds: a busy
	// region would otherwise fill the avatar with small talk and leave
	// no room for anybody to drive it.
	chatJobs chan struct{}

	// jobs limits how many commands this avatar runs at once.  A
	// buffered channel rather than a counter, so that a command which
	// arrives when it is full is refused at once and told why, rather
	// than queued behind a benchmark nobody remembers starting.
	jobs chan struct{}

	mu     sync.Mutex
	s      *sl.Session
	st     state
	detail string
	since  time.Time

	// wake lets a command ask the run loop to try again now rather
	// than at the end of its backoff, and force says the next attempt
	// may overrule a deliberate logout.
	wake  chan struct{}
	force bool

	// trouble is what has gone wrong with this avatar lately, for
	// somebody who can only reach it by talking to it.  See trouble.go.
	trouble troubles

	// seen is when each person last said something to this avatar,
	// which is how a remark after a long silence is known to be one.
	//
	// Kept here rather than read from the conversation store because
	// an avatar with no model has no store and still has failures
	// worth reporting to whoever drives it.
	seenMu sync.Mutex
	seen   map[msg.UUID]time.Time
}

func newBot(d *daemon, name string) *bot {
	jobs := d.cfg.Jobs
	if jobs < 1 {
		jobs = 1
	}
	chats := d.cfg.ChatJobs
	if chats < 1 {
		chats = 1
	}
	return &bot{
		d:        d,
		name:     name,
		jobs:     make(chan struct{}, jobs),
		chatJobs: make(chan struct{}, chats),
		st:       stateStarting,
		since:    time.Now(),
		wake:     make(chan struct{}, 1),
		seen:     map[msg.UUID]time.Time{},
	}
}

// noteTrouble files a failure against the avatar it belongs to.
//
// An avatar this daemon does not hold is not an error here: the name
// came from a conversation, the log line has already been written, and
// there is simply nobody to tell.
func (d *daemon) noteTrouble(avatar, text string) {
	if b, ok := d.bots[avatar]; ok {
		b.trouble.add(text)
	}
}

// sinceSeen is how long it has been since this person last said
// anything to this avatar, and whether they have said anything since
// the daemon started.  It records now as the latest.
//
// So it is called only for a remark worth counting: a typing
// notification is not somebody speaking, and treating it as such would
// mean nobody was ever away.
func (b *bot) sinceSeen(who msg.UUID, now time.Time) (time.Duration, bool) {
	b.seenMu.Lock()
	defer b.seenMu.Unlock()
	last, ok := b.seen[who]
	b.seen[who] = now
	if !ok {
		return 0, false
	}
	return now.Sub(last), true
}

// Name is the profile this attendant holds.
func (b *bot) Name() string { return b.name }

// Session is the live session, or nil when there is none.
func (b *bot) Session() *sl.Session {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.s
}

// Need is Session, with the refusal a command gives when there is none.
func (b *bot) Need() (*sl.Session, error) {
	if s := b.Session(); s != nil {
		return s, nil
	}
	st, detail := b.State()
	if detail != "" {
		return nil, fmt.Errorf("%s is not connected (%s: %s)", b.name, st, detail)
	}
	return nil, fmt.Errorf("%s is not connected (%s)", b.name, st)
}

// State is what this attendant is doing and why.
func (b *bot) State() (state, string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.st, b.detail
}

// Since is when it entered that state.
func (b *bot) Since() time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.since
}

func (b *bot) setState(st state, detail string) {
	b.mu.Lock()
	if b.st != st || b.detail != detail {
		b.since = time.Now()
	}
	b.st, b.detail = st, detail
	b.mu.Unlock()
}

func (b *bot) setSession(s *sl.Session) {
	b.mu.Lock()
	b.s = s
	b.mu.Unlock()
}

// Wake asks the run loop to try again now.  With force, the attempt may
// overrule a session somebody logged out on purpose.
func (b *bot) Wake(force bool) {
	b.mu.Lock()
	if force {
		b.force = true
	}
	b.mu.Unlock()
	select {
	case b.wake <- struct{}{}:
	default: // one pending wake is as good as two
	}
}

func (b *bot) takeForce() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	f := b.force
	b.force = false
	return f
}

func (b *bot) logf(format string, args ...any) {
	b.d.logf(b.name+": "+format, args...)
}

// chatf logs a line that is only somebody talking, which -q drops.
//
// A method of its own rather than a rule about what the text looks
// like.  Deciding by matching the message would make every future log
// line a thing that might silently disappear, and it already had: a
// filter for "Somebody says:" was dropping "slgod says:" as well.
func (b *bot) chatf(format string, args ...any) {
	if b.d.quiet {
		return
	}
	b.logf(format, args...)
}

// run keeps this avatar attended to, for as long as the context lasts.
func (b *bot) run(ctx context.Context) {
	defer b.setState(stateGone, "")
	backoff := backoffFirst

	for ctx.Err() == nil {
		held, lasted := b.attend(ctx)
		if ctx.Err() != nil {
			return
		}
		if lasted > settled {
			backoff = backoffFirst
		}

		if held {
			// Somebody stopped this avatar on purpose.  Asking to host
			// it again would be arguing with them, so the attendant
			// stops asking -- and WATCHES, because the person who
			// stopped it is going to start it again and should not
			// have to tell this daemon so as well.
			b.setState(stateHeldBack, b.detailNow())
			b.awaitRelease(ctx)
			continue
		}

		wait := backoff
		{
			b.setState(stateWaiting, b.detailNow())
			if backoff < backoffMax {
				backoff *= 2
				if backoff > backoffMax {
					backoff = backoffMax
				}
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-b.wake:
		case <-time.After(wait):
		}
	}
}

func (b *bot) detailNow() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.detail
}

// attend is one attempt: get the session hosted, attach to it, and
// serve it until it ends.
//
// It reports whether the refusal was a deliberate logout, and how long
// the session lasted -- which is what tells a connection that keeps
// dropping from one that came up and stayed up.
func (b *bot) attend(ctx context.Context) (held bool, lasted time.Duration) {
	force := b.takeForce()

	b.setState(stateHosting, "")
	deliberate, err := b.d.host(ctx, b.name, force)
	if err != nil {
		if deliberate {
			b.setState(stateHeldBack, err.Error())
			b.logf("%v", err)
			return true, 0
		}
		b.setState(stateWaiting, err.Error())
		b.logf("cannot host: %v", err)
		return false, 0
	}

	s, err := b.d.attach(ctx, b.name)
	if err != nil {
		b.setState(stateWaiting, err.Error())
		b.logf("cannot attach: %v", err)
		return false, 0
	}

	started := time.Now()
	b.setSession(s)
	b.setState(stateAttached, s.Info().AvatarName)
	b.logf("attached as %s in %s", s.Info().AvatarName, s.Info().Region)

	b.serve(ctx, s)

	b.setSession(nil)
	s.Close()
	lasted = time.Since(started)
	why := "the session ended"
	if err := s.Err(); err != nil && !errors.Is(err, context.Canceled) {
		why = err.Error()
	}
	if ctx.Err() == nil {
		b.logf("detached after %s: %s", lasted.Round(time.Second), why)
	}
	b.setState(stateWaiting, why)
	return false, lasted
}

// ------------------------------------------------------------- listings

// agentLine is one row of the "agents" listing: what slgod says about a
// profile, and what this daemon is doing about it.
type agentLine struct {
	Name    string
	State   string
	Avatar  string
	Region  string
	Mine    bool
	Attends string
}

// agentLines puts slgod's list together with this daemon's own.
func (d *daemon) agentLines(ctx context.Context) ([]agentLine, error) {
	as, err := d.Agents(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []agentLine
	for _, a := range as {
		seen[a.GetName()] = true
		row := agentLine{
			Name:   a.GetName(),
			State:  strings.ToLower(a.GetState().String()),
			Avatar: a.GetAvatarName(),
			Region: a.GetRegion(),
		}
		if a.GetDetail() != "" {
			row.State += " (" + a.GetDetail() + ")"
		}
		if b, ok := d.Bot(a.GetName()); ok {
			row.Mine = true
			st, detail := b.State()
			row.Attends = string(st)
			if detail != "" && st != stateAttached {
				row.Attends += " (" + detail + ")"
			}
		}
		out = append(out, row)
	}
	// An avatar this daemon was told to hold that slgod has never heard
	// of is the interesting row, so it is not left out for being
	// missing from the other list.
	for _, b := range d.Bots() {
		if seen[b.Name()] {
			continue
		}
		st, detail := b.State()
		out = append(out, agentLine{
			Name:    b.Name(),
			State:   "slgod has not heard of it",
			Mine:    true,
			Attends: string(st) + " (" + detail + ")",
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		// This daemon's own first: they are what somebody asking is
		// asking about.
		if out[i].Mine != out[j].Mine {
			return out[i].Mine
		}
		return false
	})
	return out, nil
}

// awaitRelease waits until an avatar stopped on purpose is started
// again, or until the daemon is told to try anyway.
//
// It polls rather than being pushed to, and that is deliberate.  An
// attendant with no session has no stream to be told anything on --
// slgod's notices travel to the clients attached to an agent, and this
// one is attached to nothing -- so being "informed" would mean a new
// daemon-wide event channel.  A read every half minute achieves the
// same thing, costs one small call, and is self-healing in the two
// ways a stream is not: it works when this daemon started AFTER the
// logout, with no event to have missed, and it needs no reconnecting
// when slgod itself restarts.
//
// Waking early is still possible: ":host NAME" pokes it, so somebody
// who does not want to wait need not.
func (b *bot) awaitRelease(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-b.wake:
			return
		case <-time.After(HeldRecheck):
		}
		if !b.d.stoppedAt(ctx, b.name) {
			b.logf("started again by somebody else; picking it up")
			return
		}
	}
}
