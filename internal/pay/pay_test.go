package pay

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

var (
	me      = msg.MustParseUUID("53d97e57-7e57-c0de-fdd9-764eb0e2a65d")
	example = msg.MustParseUUID("92f67e57-7e57-c0de-24de-53f27a898992")
	other   = msg.MustParseUUID("cd957e57-7e57-c0de-26e8-543033dda432")
	lamp    = msg.MustParseUUID("1b737e57-7e57-c0de-de35-3390805ca1b0")
)

// world answers from tables, and counts how often it was asked a name.
type world struct {
	owners  map[msg.UUID]msg.UUID
	names   map[string]msg.UUID
	asked   []string
	failing error

	// who names an avatar by its key, for a refusal to say.
	who map[msg.UUID]string

	// What a purchase is worth: objects by local id, parcels by local
	// id, and a group's fee, and an invitation's, by group.
	objects     map[uint32]sale
	parcels     map[int32]land
	groupFees   map[msg.UUID]int
	inviteFees  map[msg.UUID]int
	inviteAsked []msg.UUID
}

type sale struct {
	owner msg.UUID
	price int
}

type land struct {
	owner      msg.UUID
	sale, pass int
}

func (w *world) NameOf(_ context.Context, id msg.UUID) string { return w.who[id] }

func (w *world) ObjectSale(local uint32) (msg.UUID, int, bool) {
	o, ok := w.objects[local]
	return o.owner, o.price, ok
}

func (w *world) ParcelSale(local int32) (msg.UUID, int, int, bool) {
	p, ok := w.parcels[local]
	return p.owner, p.sale, p.pass, ok
}

func (w *world) GroupFee(g msg.UUID) (int, bool) {
	f, ok := w.groupFees[g]
	return f, ok
}

func (w *world) InvitationFee(g, txn msg.UUID) (int, bool) {
	w.inviteAsked = append(w.inviteAsked, txn)
	f, ok := w.inviteFees[g]
	return f, ok
}

func (w *world) ObjectOwner(id msg.UUID) (msg.UUID, bool) {
	o, ok := w.owners[id]
	return o, ok
}

func (w *world) FindAvatar(ctx context.Context, name string) (msg.UUID, error) {
	w.asked = append(w.asked, name)
	if w.failing != nil {
		return msg.UUID{}, w.failing
	}
	for n, id := range w.names {
		if strings.EqualFold(n, name) {
			return id, nil
		}
	}
	return msg.UUID{}, nil
}

func gift(to msg.UUID, amount int) *msg.MoneyTransferRequest {
	m := &msg.MoneyTransferRequest{}
	m.AgentData.AgentID = me
	m.MoneyData.SourceID = me
	m.MoneyData.DestID = to
	m.MoneyData.Amount = int32(amount)
	m.MoneyData.TransactionType = TransGift
	m.MoneyData.Description = []byte("thanks\x00")
	return m
}

func payObject(o msg.UUID, amount int) *msg.MoneyTransferRequest {
	m := gift(o, amount)
	m.MoneyData.TransactionType = TransPayObject
	return m
}

func rules(t *testing.T, lines ...string) Rules {
	t.Helper()
	var r Rules
	for _, l := range lines {
		k, v, _ := strings.Cut(l, "=")
		if err := r.Set(strings.TrimSpace(k), v); err != nil {
			t.Fatalf("%s: %v", l, err)
		}
	}
	return r
}

// clock is a gate's now, moved by hand.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func gateAt(r Rules, ledger string, c *clock) *Gate {
	g := NewGate(r, ledger)
	g.now = c.now
	return g
}

func TestTheProfileLinesAreRead(t *testing.T) {
	r := rules(t, "pay = on", "pay_max = 5", "pay_daily = L$20", "pay_to = Example Resident", "pay_to = "+other.String())
	if !r.On || r.Max != 5 || r.Daily != 20 || len(r.To) != 2 {
		t.Fatalf("read %+v", r)
	}
	var back Rules
	for _, kv := range r.Lines() {
		if err := back.Set(kv[0], kv[1]); err != nil {
			t.Fatal(err)
		}
	}
	if back.On != r.On || back.Max != r.Max || back.Daily != r.Daily || strings.Join(back.To, "|") != strings.Join(r.To, "|") {
		t.Errorf("written back and read again as %+v, from %+v", back, r)
	}
	if lines := (Rules{}).Lines(); lines != nil {
		t.Errorf("a profile that says nothing about paying is written back with %v", lines)
	}

	for _, bad := range []string{"pay = sometimes", "pay_max = 0", "pay_daily = -3", "pay_max = ten", "pay_to = "} {
		k, v, _ := strings.Cut(bad, "=")
		var r Rules
		if err := r.Set(strings.TrimSpace(k), v); err == nil {
			t.Errorf("%q was taken", bad)
		}
	}
}

func TestTheDefaults(t *testing.T) {
	c := &clock{t: time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)}
	w := &world{}
	g := gateAt(rules(t, "pay = on", "pay_to = *"), "", c)

	if d := g.Check(context.Background(), w, gift(example, 11)); !strings.Contains(d.Refused, "pay_max of L$10") {
		t.Errorf("L$11 with no pay_max: %q", d.Refused)
	}
	if d := g.Check(context.Background(), w, gift(example, 6)); d.Refused != "" {
		t.Fatalf("L$6: %s", d.Refused)
	}
	if d := g.Check(context.Background(), w, gift(example, 5)); !strings.Contains(d.Refused, "pay_daily of L$10") {
		t.Errorf("L$11 in a day with no pay_daily: %q", d.Refused)
	}
	if d := g.Check(context.Background(), w, gift(example, 4)); d.Refused != "" {
		t.Errorf("L$10 in a day with no pay_daily: %s", d.Refused)
	}
}

func TestEachRefusal(t *testing.T) {
	cases := []struct {
		name  string
		rules []string
		m     *msg.MoneyTransferRequest
		want  string
	}{
		{"off unless present", nil, gift(example, 1), "paying is off"},
		{"off when said", []string{"pay = off", "pay_to = *"}, gift(example, 1), "paying is off"},
		{"on alone pays nobody", []string{"pay = on"}, gift(example, 1), "add pay_to"},
		{"nothing", []string{"pay = on", "pay_to = *"}, gift(example, 0), "not an amount"},
		{"less than nothing", []string{"pay = on", "pay_to = *"}, gift(example, -5), "not an amount"},
		{"over pay_max", []string{"pay = on", "pay_max = 3", "pay_to = *"}, gift(example, 4), "pay_max of L$3"},
		{"over pay_daily", []string{"pay = on", "pay_max = 50", "pay_daily = 3", "pay_to = *"}, gift(example, 4), "pay_daily of L$3"},
		{"not in pay_to", []string{"pay = on", "pay_to = " + example.String()}, gift(other, 1), "not in this profile's pay_to"},
		{"an object nobody is known to own", []string{"pay = on", "pay_to = *"}, payObject(lamp, 1), "owner of object"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := NewGate(rules(t, c.rules...), "")
			d := g.Check(context.Background(), &world{}, c.m)
			if !strings.Contains(d.Refused, c.want) {
				t.Errorf("refused %q, want it to say %q", d.Refused, c.want)
			}
			if got, _ := g.read(); len(got) != 0 {
				t.Errorf("a refusal was recorded as paid: %v", got)
			}
		})
	}
}

func TestAnyoneIsAStar(t *testing.T) {
	w := &world{}
	g := NewGate(rules(t, "pay = on", "pay_to = Example Resident", "pay_to = *"), "")
	d := g.Check(context.Background(), w, gift(other, 1))
	if d.Refused != "" || d.AllowedBy != "*" {
		t.Errorf("pay_to = * refused somebody: %+v", d)
	}
	if len(w.asked) != 0 {
		t.Errorf("with * listed the grid was still asked who %v are", w.asked)
	}
}

func TestANameIsMatchedByTheKeyItResolvesTo(t *testing.T) {
	w := &world{names: map[string]msg.UUID{"Example Resident": example}}
	g := NewGate(rules(t, "pay = on", "pay_to = example"), "")

	if d := g.Check(context.Background(), w, gift(example, 1)); d.Refused != "" || d.AllowedBy != "example" {
		t.Fatalf("the avatar a pay_to name names: %+v", d)
	}
	if d := g.Check(context.Background(), w, gift(other, 1)); d.Refused == "" {
		t.Error("somebody the name does not resolve to was paid")
	}
	if d := g.Check(context.Background(), w, gift(example, 1)); d.Refused != "" {
		t.Errorf("paid again: %s", d.Refused)
	}
	if len(w.asked) != 1 || w.asked[0] != "example Resident" {
		t.Errorf("the grid was asked %q; want once, for the whole name", w.asked)
	}
}

func TestANameTheGridCannotBeAskedAboutIsARefusal(t *testing.T) {
	w := &world{failing: errors.New("no answer")}
	g := NewGate(rules(t, "pay = on", "pay_to = Example Resident"), "")
	if d := g.Check(context.Background(), w, gift(example, 1)); !strings.Contains(d.Refused, "could not be asked") {
		t.Errorf("refused %q", d.Refused)
	}
	w.failing = nil
	w.names = map[string]msg.UUID{"Example Resident": example}
	if d := g.Check(context.Background(), w, gift(example, 1)); d.Refused != "" {
		t.Errorf("a lookup that failed was remembered as an answer: %s", d.Refused)
	}
}

func TestAnObjectIsPaidOnlyWhenItsOwnerMayBe(t *testing.T) {
	w := &world{owners: map[msg.UUID]msg.UUID{lamp: example}}

	g := NewGate(rules(t, "pay = on", "pay_to = "+example.String()), "")
	d := g.Check(context.Background(), w, payObject(lamp, 2))
	if d.Refused != "" || d.Payee != example {
		t.Fatalf("an object whose owner may be paid: %+v", d)
	}
	if got, _ := g.read(); len(got) != 1 || got[0].To != example {
		t.Errorf("recorded %v, want the payment to the owner", got)
	}

	g = NewGate(rules(t, "pay = on", "pay_to = "+other.String()), "")
	if d := g.Check(context.Background(), w, payObject(lamp, 2)); !strings.Contains(d.Refused, "not in this profile's pay_to") {
		t.Errorf("an object whose owner may not be paid: %q", d.Refused)
	}
	// The object is not what pay_to lists.
	g = NewGate(rules(t, "pay = on", "pay_to = "+lamp.String()), "")
	if d := g.Check(context.Background(), w, payObject(lamp, 2)); d.Refused == "" {
		t.Error("an object listed by its own key was paid, though its owner is not listed")
	}
}

func TestTheDailyTotalSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	path := LedgerPath(filepath.Join(dir, "slgod"), "example")
	c := &clock{t: time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)}
	r := rules(t, "pay = on", "pay_to = *")
	w := &world{}

	if d := gateAt(r, path, c).Check(context.Background(), w, gift(example, 8)); d.Refused != "" {
		t.Fatal(d.Refused)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if m := fi.Mode().Perm(); m != 0o600 {
		t.Errorf("the record is mode %04o, want 0600", m)
	}

	c.t = c.t.Add(23 * time.Hour)
	again := gateAt(r, path, c)
	if d := again.Check(context.Background(), w, gift(example, 3)); !strings.Contains(d.Refused, "L$11 paid in the last 24 hours") {
		t.Errorf("after a restart, 8 then 3: %q", d.Refused)
	}
	c.t = c.t.Add(time.Hour)
	if d := again.Check(context.Background(), w, gift(example, 3)); d.Refused != "" {
		t.Errorf("a day after the first payment: %s", d.Refused)
	}
	es, err := readLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 1 || es[0].Amount != 3 {
		t.Errorf("the record holds %v; the day-old payment should have gone", es)
	}
}

func TestARecordThatCannotBeReadStopsPaying(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pay-example")
	if err := os.WriteFile(path, []byte("yesterday 5 somebody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	g := NewGate(rules(t, "pay = on", "pay_to = *"), path)
	if d := g.Check(context.Background(), &world{}, gift(example, 1)); !strings.Contains(d.Refused, "cannot be read") {
		t.Errorf("refused %q", d.Refused)
	}
}

func TestARefusalIsFilledInAsTheGridFillsOneIn(t *testing.T) {
	m := gift(example, 3)
	r := Refusal(m, 42, "paying is off")
	if r.MoneyData.TransactionSuccess {
		t.Error("a refusal says it succeeded")
	}
	ti := r.TransactionInfo
	if ti.TransactionType != TransGift || ti.SourceID != me || ti.DestID != example || ti.Amount != 3 ||
		string(ti.ItemDescription) != "thanks\x00" {
		t.Errorf("the transaction is %+v", ti)
	}
	if r.MoneyData.MoneyBalance != 42 || string(r.MoneyData.Description) != "paying is off\x00" {
		t.Errorf("the reply says %+v", r.MoneyData)
	}
	if _, err := r.Encode(); err != nil {
		t.Errorf("it does not encode: %v", err)
	}
	long := Refusal(m, 0, strings.Repeat("x", 400))
	if _, err := long.Encode(); err != nil {
		t.Errorf("a long reason does not encode: %v", err)
	}
}
