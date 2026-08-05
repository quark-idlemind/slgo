package main

// The commands, which are what the prefix key leads to.
//
// Each takes the rest of the line as it was typed.  Anything that names
// a person accepts a uuid, part of a name, or the number from the last
// listing, because the three are what you have at different moments:
// the number right after a "who", the name when you know who you want,
// and the uuid when something printed one.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// command runs one command line.
func (a *App) command(ctx context.Context, line string) {
	name, rest, _ := strings.Cut(line, " ")
	rest = strings.TrimSpace(rest)

	switch strings.ToLower(name) {
	case "help", "?":
		a.help()
	case "who", "nearby":
		a.cmdNearby(ctx)
	case "lookup", "find", "search":
		a.cmdLookup(ctx, rest)
	case "friends":
		a.cmdFriends(ctx)
	case "im", "msg":
		a.cmdIM(ctx, rest)
	case "close", "part":
		a.cmdClose()
	case "sessions", "windows":
		a.cmdSessions()
	case "local":
		a.cmdLocal()
	case "offer", "befriend":
		a.cmdOffer(ctx, rest)
	case "accept":
		a.cmdAccept(ctx, rest)
	case "decline", "reject":
		a.cmdDecline(ctx, rest)
	case "offers":
		a.cmdOffers()
	case "where":
		a.cmdWhere(ctx)
	case "quit", "exit":
		a.Quit()
	default:
		a.notice("no command %q; %s help for the list", name, KeyName(a.cfg.Prefix))
	}
}

func (a *App) help() {
	key := KeyName(a.cfg.Prefix)
	a.page([]string{
		"commands (" + key + " first, then the command):",
		"  who                 who else is in the region, nearest first",
		"  friends             which friends are online",
		"  lookup <text>       search for people whose name contains <text>",
		"  im <who>            start or switch to an instant message session",
		"  close               close the current instant message session",
		"  sessions            list the sessions; tab cycles them",
		"  local               go back to open chat",
		"  offer <who> [text]  offer friendship",
		"  offers              friendship offers waiting for an answer",
		"  accept [who]        accept an offer",
		"  decline [who]       refuse one",
		"  where               where this avatar is standing",
		"  quit                leave slchat; the session stays up in slgod",
		"",
		"<who> is a name, part of one, a uuid, or the number from the last listing.",
		"tab on an empty line moves to the next session.",
		"a long listing pages: space shows more, q stops it.",
	})
}

// listed remembers the last listing, so that "im 2" means what it just
// showed.
func (a *App) setListed(ps []person) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.listed = append([]person(nil), ps...)
}

func (a *App) cmdNearby(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	ps, err := a.Nearby(ctx)
	if err != nil {
		a.notice("could not read who is nearby: %v", err)
		return
	}
	if len(ps) == 0 {
		a.notice("nobody else is in range")
		return
	}
	a.setListed(ps)
	lines := []string{fmt.Sprintf("%d nearby:", len(ps))}
	for i, p := range ps {
		lines = append(lines, fmt.Sprintf("  %2d  %-28s %6.1fm", i+1, p.Name, p.Distance))
	}
	a.page(lines)
}

func (a *App) cmdFriends(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	online, offline, err := a.Friends(ctx)
	if err != nil {
		a.notice("could not read the friend list: %v", err)
		return
	}
	if len(online) == 0 {
		a.notice("no friends online (%d offline)", len(offline))
		return
	}
	a.setListed(online)
	lines := []string{fmt.Sprintf("%d friends online (%d offline):", len(online), len(offline))}
	for i, p := range online {
		lines = append(lines, fmt.Sprintf("  %2d  %s", i+1, p.Name))
	}
	a.page(lines)
}

// cmdLookup searches for people by part of their name.
//
// What comes back is put in the roster and in the numbered list, so the
// answer is usable straight away: lookup, then "im 3".
func (a *App) cmdLookup(ctx context.Context, want string) {
	want = strings.TrimSpace(want)
	if want == "" {
		a.notice("lookup what? part of a name is enough")
		return
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	hits, err := a.Lookup(ctx, want)
	if err != nil {
		a.notice("could not search: %v", err)
		return
	}
	if len(hits) == 0 {
		a.notice("nobody found matching %q", want)
		return
	}

	ps := make([]person, 0, len(hits))
	for _, h := range hits {
		a.roster.Learn(h.ID, h.Name)
		ps = append(ps, person{ID: h.ID, Name: h.Name})
	}
	a.setListed(ps)

	lines := []string{fmt.Sprintf("%d matching %q:", len(hits), want)}
	for i, h := range hits {
		line := fmt.Sprintf("  %2d  %-28s", i+1, h.Name)
		// The display name is worth showing when it is not simply
		// the name again, since it is what they are called in
		// conversation.
		if h.Display != "" && !strings.EqualFold(h.Display, h.Name) {
			line += "  " + h.Display
		}
		if h.Username != "" && !strings.EqualFold(h.Username, strings.ReplaceAll(strings.ToLower(h.Name), " ", ".")) {
			line += "  (" + h.Username + ")"
		}
		lines = append(lines, strings.TrimRight(line, " "))
	}
	a.page(lines)
}

func (a *App) cmdIM(ctx context.Context, who string) {
	if who == "" {
		a.notice("im who?")
		return
	}
	id, name, err := a.resolve(ctx, who)
	if err != nil {
		a.notice("%v", err)
		return
	}
	if id == a.me {
		a.notice("that is you")
		return
	}
	s, made := a.sessions.Open(id, name)
	a.sessions.Switch(s)
	a.refreshPrompt()
	if made {
		a.notice("talking to %s -- tab to come back to open chat", s.Label())
	}
}

func (a *App) cmdClose() {
	s := a.sessions.Current()
	if s.Local() {
		a.notice("open chat is always there; nothing to close")
		return
	}
	name := s.Label()
	a.sessions.Close(s)
	a.refreshPrompt()
	a.notice("closed the session with %s", name)
}

func (a *App) cmdSessions() {
	list, cur := a.sessions.All()
	var b strings.Builder
	b.WriteString("sessions (tab cycles):")
	for i, s := range list {
		mark := " "
		if i == cur {
			mark = "*"
		}
		fmt.Fprintf(&b, "\n %s %2d  %s", mark, i+1, s.Label())
	}
	a.page(strings.Split(b.String(), "\n"))
}

func (a *App) cmdLocal() {
	list, _ := a.sessions.All()
	a.sessions.Switch(list[0])
	a.refreshPrompt()
}

func (a *App) cmdOffer(ctx context.Context, rest string) {
	if rest == "" {
		a.notice("offer friendship to whom?")
		return
	}
	who, text, _ := strings.Cut(rest, " ")
	id, name, err := a.resolve(ctx, who)
	if err != nil {
		a.notice("%v", err)
		return
	}
	if id == a.me {
		a.notice("that is you")
		return
	}
	if err := a.OfferFriendship(ctx, id, strings.TrimSpace(text)); err != nil {
		a.notice("could not offer: %v", err)
		return
	}
	a.notice("offered friendship to %s", name)
}

// pending is the offers waiting, oldest first.
func (a *App) pending() []offer {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]offer, 0, len(a.offers))
	for _, o := range a.offers {
		out = append(out, o)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].At.Before(out[j-1].At); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// takeOffer finds the offer somebody made and forgets it.  With nothing
// named it takes the only one, and refuses to guess between several.
func (a *App) takeOffer(who string) (offer, error) {
	list := a.pending()
	if len(list) == 0 {
		return offer{}, fmt.Errorf("no friendship offers are waiting")
	}
	var found offer
	switch {
	case who == "":
		if len(list) > 1 {
			return offer{}, fmt.Errorf("%d offers are waiting; name one of them", len(list))
		}
		found = list[0]
	default:
		want := strings.ToLower(who)
		var hits []offer
		for _, o := range list {
			if strings.Contains(strings.ToLower(o.Name), want) || o.From.String() == who {
				hits = append(hits, o)
			}
		}
		if len(hits) == 0 {
			return offer{}, fmt.Errorf("no offer from anybody matching %q", who)
		}
		if len(hits) > 1 {
			return offer{}, fmt.Errorf("%q matches %d offers", who, len(hits))
		}
		found = hits[0]
	}

	a.mu.Lock()
	delete(a.offers, found.From)
	a.mu.Unlock()
	return found, nil
}

func (a *App) cmdAccept(ctx context.Context, who string) {
	o, err := a.takeOffer(who)
	if err != nil {
		a.notice("%v", err)
		return
	}
	if err := a.AcceptFriendship(ctx, o); err != nil {
		a.notice("could not accept: %v", err)
		return
	}
	// Nothing will tell us this happened: the grid reports an
	// acceptance to whoever was offered it, and says nothing at all to
	// whoever did the accepting.  So say it ourselves, or the friend
	// list is wrong until the next login.
	if err := a.conn.NoteFriend(ctx, o.From, true); err != nil {
		a.notice("accepted, but could not record the friendship: %v", err)
	}
	a.notice("accepted %s's friendship offer", o.Name)
}

func (a *App) cmdDecline(ctx context.Context, who string) {
	o, err := a.takeOffer(who)
	if err != nil {
		a.notice("%v", err)
		return
	}
	if err := a.DeclineFriendship(ctx, o); err != nil {
		a.notice("could not decline: %v", err)
		return
	}
	a.notice("declined %s's friendship offer", o.Name)
}

func (a *App) cmdOffers() {
	list := a.pending()
	if len(list) == 0 {
		a.notice("no friendship offers are waiting")
		return
	}
	var b strings.Builder
	b.WriteString("friendship offers waiting:")
	for _, o := range list {
		fmt.Fprintf(&b, "\n  %-28s %s ago", o.Name, time.Since(o.At).Round(time.Second))
	}
	a.page(strings.Split(b.String(), "\n"))
}

func (a *App) cmdWhere(ctx context.Context) {
	p, err := a.conn.Presence(ctx, 0)
	if err != nil {
		a.notice("could not ask where we are: %v", err)
		return
	}
	region := p.Region
	if region == "" {
		region = "an unnamed region"
	}
	if p.Position != nil {
		a.notice("%s at %.0f, %.0f, %.0f", region, p.Position.X, p.Position.Y, p.Position.Z)
		return
	}
	a.notice("%s", region)
}

// resolve turns what was typed into somebody.
//
// The order is what a person means by it: a uuid is itself, a number is
// the last listing, and anything else is a name -- which is looked up
// among the people already known, and asked about only if that fails.
func (a *App) resolve(ctx context.Context, who string) (msg.UUID, string, error) {
	if id, err := msg.ParseUUID(who); err == nil {
		name := a.roster.Name(id)
		if name == "" {
			a.AskNames(ctx, []msg.UUID{id})
			a.waitNames(ctx, []msg.UUID{id}, 2*time.Second)
			name = a.roster.NameOr(id)
		}
		return id, name, nil
	}

	if n, err := strconv.Atoi(who); err == nil {
		a.mu.Lock()
		list := a.listed
		a.mu.Unlock()
		if n < 1 || n > len(list) {
			return msg.UUID{}, "", fmt.Errorf("there is no %d in the last listing", n)
		}
		p := list[n-1]
		return p.ID, p.Name, nil
	}

	hits := a.roster.Find(who)
	switch len(hits) {
	case 1:
		return hits[0], a.roster.NameOr(hits[0]), nil
	case 0:
		return msg.UUID{}, "", fmt.Errorf("nobody called %q is known; try who or friends first", who)
	default:
		names := make([]string, 0, len(hits))
		for _, id := range hits {
			names = append(names, a.roster.NameOr(id))
		}
		return msg.UUID{}, "", fmt.Errorf("%q could be any of: %s", who, strings.Join(names, ", "))
	}
}
