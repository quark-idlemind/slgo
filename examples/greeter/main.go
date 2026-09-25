// Command greeter is the bot the handbook builds, one step at a time,
// on its page "Your own bot".  It stands in a shop and:
//
//   - greets each new visitor by name in local chat, once;
//   - remembers who it has greeted, in a file, across restarts;
//   - answers "info", said nearby or sent by IM, with what the land
//     says about itself;
//   - offers each new visitor a gift from its inventory.
//
// It drives an avatar slgod is holding, so the avatar stays logged in
// when the program stops:
//
//	go run ./examples/greeter -agent greeter -gift "Welcome Kit"
//
// -h lists the flags.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// options are the flags.
type options struct {
	addr   string
	agent  string
	radius float64
	every  time.Duration
	again  time.Duration
	memory string
	folder string
	gift   string
}

func main() {
	var o options
	flag.StringVar(&o.addr, "addr", "localhost:7807", "where slgod is")
	flag.StringVar(&o.agent, "agent", "", "which of slgod's avatars to use (empty: $SLGO_AGENT, or the one slgod has held longest)")
	flag.Float64Var(&o.radius, "radius", 20, "greet people who come within this many metres")
	flag.DurationVar(&o.every, "every", 5*time.Second, "how often to look around")
	flag.DurationVar(&o.again, "again", 0, "welcome somebody back after this long away (0: greet each person once)")
	flag.StringVar(&o.memory, "memory", "greeted.json", "the file that remembers who has been greeted")
	flag.StringVar(&o.folder, "folder", "Greeter", "the top-level inventory folder the gift is in")
	flag.StringVar(&o.gift, "gift", "", "the item to offer each new visitor (empty: no gift)")
	flag.Parse()

	// Ctrl-C and SIGTERM end the program cleanly.  The avatar stays
	// logged in: slgod holds it, not this program.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, o); err != nil {
		log.Fatal(err)
	}
}

// greeter is the bot's state while it runs.
type greeter struct {
	s    *sl.Session
	o    options
	mem  *memory
	gift *sl.Item

	// here is who was within reach at the last look, so that somebody
	// arriving can be told from somebody who has been there all along.
	here map[msg.UUID]bool

	// When each person was last given the information nearby, given
	// it by IM, and told how to ask for it.  They are what stop anybody,
	// and in particular another bot, from keeping this one talking for
	// ever.  Nearby and by IM are counted apart, so that somebody who
	// asks in both places is answered in both.
	answeredNearby map[msg.UUID]time.Time
	answeredIM     map[msg.UUID]time.Time
	pointed        map[msg.UUID]time.Time
}

// run attaches to the avatar and listens until told to stop.
func run(ctx context.Context, o options) error {
	// Step 2: attach to an avatar slgod is holding.  DialWeak rather
	// than Dial says this program attends the avatar rather than uses
	// it, so "slsh logout" does not refuse on its account.
	dial, cancel := context.WithTimeout(ctx, 30*time.Second)
	s, err := sl.DialWeak(dial, o.addr, o.agent)
	cancel()
	if err != nil {
		return err
	}
	defer s.Close()

	where, err := s.Where(ctx)
	if err != nil {
		return err
	}
	log.Printf("attached to %s, in %s at %.0f, %.0f, %.0f", s.Info().AvatarName,
		where.Region, where.Position.X, where.Position.Y, where.Position.Z)

	g := &greeter{
		s: s, o: o,
		here:           map[msg.UUID]bool{},
		answeredNearby: map[msg.UUID]time.Time{},
		answeredIM:     map[msg.UUID]time.Time{},
		pointed:        map[msg.UUID]time.Time{},
	}
	if g.mem, err = loadMemory(o.memory); err != nil {
		return err
	}
	log.Printf("%d people greeted before, in %s", len(g.mem.Visitors), o.memory)

	if o.gift != "" {
		if g.gift, err = findGift(ctx, s, o.folder, o.gift); err != nil {
			return err
		}
		log.Printf("the gift is %s", g.gift.Name)
	}
	return g.listen(ctx)
}

// listen is the event loop.  Everything the bot does starts here, and
// runs one thing at a time.
func (g *greeter) listen(ctx context.Context) error {
	// Step 3: subscribe before doing anything else, so that nothing
	// said in between is missed.  Local chat from avatars only, at the
	// three volumes a person speaks at; every kind of IM.
	chat := g.s.Chat(sl.ChatFilter{
		SourceTypes: []uint8{sl.SourceAgent},
		Types:       []uint8{sl.ChatWhisper, sl.ChatSay, sl.ChatShout},
	}, 0)
	defer g.s.StopChat(chat)
	ims := g.s.IMs(0)
	defer g.s.StopIMs(ims)

	// Step 4: nothing announces an arrival, so look around on a timer.
	tick := time.NewTicker(g.o.every)
	defer tick.Stop()
	g.look(ctx)

	for {
		select {
		case <-ctx.Done():
			log.Printf("stopping; the avatar is still logged in")
			return nil
		case <-g.s.Done():
			return fmt.Errorf("the connection to slgod ended: %v", g.s.Err())
		case l, ok := <-chat:
			if !ok {
				return errors.New("the chat subscription ended")
			}
			g.heard(ctx, l)
		case im, ok := <-ims:
			if !ok {
				return errors.New("the IM subscription ended")
			}
			g.told(ctx, im)
		case <-tick.C:
			g.look(ctx)
		}
	}
}

// look finds who is within reach, and deals with whoever has just
// arrived.
func (g *greeter) look(ctx context.Context) {
	people, err := g.s.Nearby(ctx)
	if err != nil {
		log.Printf("cannot see who is here: %v", err)
		return
	}
	now := map[msg.UUID]bool{}
	for _, p := range people {
		// Distance is -1 for somebody seated on something the region
		// has not described, who is here but cannot be placed.
		if p.Distance < 0 || float64(p.Distance) > g.o.radius {
			continue
		}
		// Nearby puts a shortened id where a name has not arrived
		// yet.  Wait for the name rather than greet a number: it is
		// usually there by the next look.
		name := g.s.Name(p.ID)
		if name == "" {
			continue
		}
		now[p.ID] = true
		if !g.here[p.ID] {
			g.arrived(ctx, p, name)
		}
	}
	g.here = now
}

// arrived decides what to say to somebody who has just come within
// reach: a greeting if they are new, a welcome back if they have been
// away long enough, and nothing otherwise.
func (g *greeter) arrived(ctx context.Context, p sl.Person, name string) {
	// Step 5: remember people by uuid.  A name is what is shown; the
	// uuid is who they are.
	id := p.ID.String()
	v := g.mem.Visitors[id]
	switch {
	case v == nil:
		g.greet(ctx, p, name, false)
		v = &visitor{Name: name, First: time.Now()}
		g.mem.Visitors[id] = v
	case g.o.again > 0 && time.Since(v.Last) >= g.o.again:
		g.greet(ctx, p, name, true)
	default:
		log.Printf("%s is here again; greeted on %s", name, v.First.Format(time.DateOnly))
	}
	v.Last = time.Now()
	if err := g.mem.save(); err != nil {
		log.Printf("cannot save %s: %v", g.o.memory, err)
	}
}

// greet says hello in local chat, and offers somebody new the gift.
func (g *greeter) greet(ctx context.Context, p sl.Person, name string, back bool) {
	// Step 6: act.  Turn to face them first.  It does not matter if
	// that fails, so it is only logged.
	if _, err := g.s.Face(ctx, p.Position); err != nil {
		log.Printf("cannot face %s: %v", name, err)
	}

	first := strings.Fields(name)[0]
	line := fmt.Sprintf("Welcome back, %s!", first)
	if !back {
		place, _ := g.land(ctx)
		line = fmt.Sprintf("Hello %s, welcome to %s!", first, place)
		// Step 8: the gift.  It is an offer: they have to accept it.
		if g.gift != nil {
			err := g.s.GiveToAvatar(ctx, p.ID, g.gift.ID, g.gift.Name, int8(g.gift.Type))
			if err != nil {
				log.Printf("cannot offer %s to %s: %v", g.gift.Name, name, err)
			} else {
				log.Printf("offered %s to %s", g.gift.Name, name)
				line += " I have sent you a " + g.gift.Name + "."
			}
		}
	}
	line += " Say info, or IM me info, to hear about this place."
	if err := g.s.Say(ctx, line, 0); err != nil {
		log.Printf("cannot greet %s: %v", name, err)
		return
	}
	log.Printf("greeted %s", name)
}

// heard is one line of local chat from an avatar.
func (g *greeter) heard(ctx context.Context, l sl.Line) {
	// Step 7: answer.  Skip anything this avatar said itself.
	if l.Source == g.s.Me() || l.Mine {
		return
	}
	if !asksForInfo(l.Text) || tooSoon(g.answeredNearby, l.Source) {
		return
	}
	if err := g.s.Say(ctx, g.info(ctx), 0); err != nil {
		log.Printf("cannot answer %s: %v", l.From, err)
		return
	}
	log.Printf("told %s about this place, in local chat", l.From)
}

// told is one instant message.
func (g *greeter) told(ctx context.Context, im *sl.IM) {
	// An IM can be a dozen kinds of thing.  Conversation is true only
	// for somebody writing to this avatar: not a typing notice, a
	// group message, an offer, or this avatar's own words from another
	// client.
	if !im.Conversation() {
		return
	}
	var reply string
	switch {
	case asksForInfo(im.Text):
		if tooSoon(g.answeredIM, im.From) {
			return
		}
		reply = g.info(ctx)
	case time.Since(g.pointed[im.From]) > time.Hour:
		// Anything else gets one pointer an hour, and then silence.
		// Answering every IM is how two bots talk to each other
		// until somebody notices.
		g.pointed[im.From] = time.Now()
		reply = "I am this shop's greeter, and a bot. IM me info to hear about this place."
	default:
		return
	}
	if err := g.s.SendIM(ctx, im.From, reply); err != nil {
		log.Printf("cannot answer %s: %v", im.FromName, err)
		return
	}
	log.Printf("answered an IM from %s", im.FromName)
}

// tooSoon is whether this person was given the information in the last
// half minute, and notes that they are being given it now if not.
func tooSoon(last map[msg.UUID]time.Time, who msg.UUID) bool {
	if time.Since(last[who]) < 30*time.Second {
		return true
	}
	last[who] = time.Now()
	return false
}

// asksForInfo is whether a line asks for the information: the word
// info on its own, in any case, with or without a question mark.
func asksForInfo(text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	return strings.TrimRight(t, "?!. ") == "info"
}

// info is the answer to "info": where this is, in the land's own words.
func (g *greeter) info(ctx context.Context) string {
	name, desc := g.land(ctx)
	text := "This is " + name
	if where, err := g.s.Where(ctx); err == nil {
		text += ", in " + where.Region
	}
	text += "."
	if desc != "" {
		text += " " + desc
	}
	return text
}

// land is the parcel's name and description, which its owner sets in a
// viewer under About Land.
func (g *greeter) land(ctx context.Context) (name, desc string) {
	p, err := g.s.Parcel(ctx, 10*time.Second)
	if err != nil {
		log.Printf("cannot read the parcel: %v", err)
		return "the shop", ""
	}
	return p.Name, p.Desc
}

// findGift finds the gift in a folder at the top of the bot's inventory,
// and checks that it can be given away more than once.
func findGift(ctx context.Context, s *sl.Session, folder, name string) (*sl.Item, error) {
	f, err := s.Folder(ctx, folder)
	if err != nil {
		return nil, err
	}
	it, err := s.FindItem(ctx, f, name)
	if err != nil {
		return nil, err
	}
	// Without transfer it cannot be given at all.  Without copy it can
	// be given once: the first visitor to accept takes the only one.
	if it.OwnerMask&sl.PermTransfer == 0 {
		return nil, fmt.Errorf("%s is no-transfer, so it cannot be given", name)
	}
	if it.OwnerMask&sl.PermCopy == 0 {
		return nil, fmt.Errorf("%s is no-copy, so only the first visitor would get one", name)
	}
	return it, nil
}

// visitor is one person the greeter has greeted.
type visitor struct {
	Name  string    `json:"name"`
	First time.Time `json:"first"` // when they were first greeted
	Last  time.Time `json:"last"`  // when they last came within reach
}

// memory is everyone greeted, by uuid, kept in a file so that a restart
// does not greet the whole shop again.
type memory struct {
	path     string
	Visitors map[string]*visitor `json:"visitors"`
}

// loadMemory reads the file, or starts an empty memory if there is none.
func loadMemory(path string) (*memory, error) {
	m := &memory{path: path}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// Nobody greeted yet.
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(b, m); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	if m.Visitors == nil {
		m.Visitors = map[string]*visitor{}
	}
	return m, nil
}

// save writes the whole file under a new name and then renames it over
// the old one, so that a crash part way leaves the last good copy
// rather than half of one.  Mode 600, because it names everybody who
// came in.
func (m *memory) save() error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.path + ".new"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.path)
}
