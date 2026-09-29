package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

var (
	examplePayee = msg.MustParseUUID("92f67e57-7e57-c0de-24de-53f27a898992")
	payTx        = msg.MustParseUUID("c14e7e57-7e57-c0de-824e-a88b9b0cd5f8")
	payingGroup  = msg.MustParseUUID("2b2a7e57-7e57-c0de-cd89-3a790594f197")
)

func moneyReply(tx msg.UUID, ok bool, balance int, said string, typ int, from, to msg.UUID, amount int, echo string) *msg.MoneyBalanceReply {
	m := &msg.MoneyBalanceReply{}
	m.MoneyData.TransactionID = tx
	m.MoneyData.TransactionSuccess = ok
	m.MoneyData.MoneyBalance = int32(balance)
	m.MoneyData.Description = append([]byte(said), 0)
	m.TransactionInfo.TransactionType = int32(typ)
	m.TransactionInfo.SourceID = from
	m.TransactionInfo.DestID = to
	m.TransactionInfo.Amount = int32(amount)
	m.TransactionInfo.ItemDescription = append([]byte(echo), 0)
	return m
}

// grid answers a balance request with balance, and a payment as the grid
// does -- or with the refusal given, when there is one.
func answersMoney(t *testing.T, x *testShell, balance int, refusal string) {
	t.Helper()
	x.grid.mu.Lock()
	defer x.grid.mu.Unlock()
	x.grid.onSend = func(m msg.Message) {
		switch q := m.(type) {
		case *msg.MoneyBalanceRequest:
			x.grid.Relay(t, moneyReply(msg.UUID{}, true, balance, "", 0, msg.UUID{}, msg.UUID{}, -1, ""))
		case *msg.MoneyTransferRequest:
			d := q.MoneyData
			echo := strings.TrimSuffix(string(d.Description), "\x00")
			if refusal != "" {
				x.grid.Relay(t, moneyReply(payTx, false, balance, refusal, int(d.TransactionType), d.SourceID, d.DestID, int(d.Amount), echo))
				return
			}
			x.grid.Relay(t, moneyReply(payTx, true, balance-int(d.Amount), "You paid Example Resident L$5.",
				int(d.TransactionType), d.SourceID, d.DestID, int(d.Amount), echo))
		}
	}
}

func payments(x *testShell) []*msg.MoneyTransferRequest {
	var out []*msg.MoneyTransferRequest
	for _, m := range x.grid.Sent() {
		if p, ok := m.(*msg.MoneyTransferRequest); ok {
			out = append(out, p)
		}
	}
	return out
}

func TestBalanceSaysWhatTheGridSays(t *testing.T) {
	x := newTestShell(t)
	answersMoney(t, x, 120, "")
	if got := x.do(t, "balance"); !strings.Contains(got, "L$120") {
		t.Errorf("balance said %q", got)
	}
}

// Nobody is at a prompt to answer a one-shot run, a script or a pipe,
// and the next line of one is not an answer.
func TestPayNeedsYesWhereNobodyCanBeAsked(t *testing.T) {
	x := newTestShell(t)
	knows(t, x, map[msg.UUID]string{examplePayee: "Example Resident"})
	answersMoney(t, x, 100, "")
	got := x.do(t, "pay Example Resident 5")
	if !strings.Contains(got, "needs --yes") {
		t.Errorf("pay with nobody to ask said %q", got)
	}
	if n := len(payments(x)); n != 0 {
		t.Errorf("%d payments went out", n)
	}

	got = x.do(t, "pay --yes Example Resident L$5 for the lamp")
	if !strings.Contains(got, "paid Example Resident L$5; L$95 left") {
		t.Fatalf("pay --yes said %q", got)
	}
	ps := payments(x)
	if len(ps) != 1 || ps[0].MoneyData.DestID != examplePayee || ps[0].MoneyData.Amount != 5 ||
		string(ps[0].MoneyData.Description) != "for the lamp\x00" {
		t.Errorf("sent %+v", ps)
	}
}

// atATerminal makes x a shell somebody is sitting at, which is the only
// kind that asks.
func atATerminal(x *testShell) { x.term.plain = false }

// typeLine types a line at the prompt and presses Enter.
func typeLine(x *testShell, line string) {
	x.term.SetLine(line)
	x.key(context.Background(), '\r')
}

func TestPayAsksAtThePrompt(t *testing.T) {
	x := newTestShell(t)
	atATerminal(x)
	knows(t, x, map[msg.UUID]string{examplePayee: "Example Resident"})
	answersMoney(t, x, 100, "")

	typeLine(x, "pay Example Resident 5")
	if p := x.promptText(); p != "pay Example Resident L$5? [y/N] " {
		t.Fatalf("the prompt is %q", p)
	}
	if n := len(payments(x)); n != 0 {
		t.Fatalf("paid before being answered")
	}
	x.out.Reset()
	typeLine(x, "n")
	if !strings.Contains(x.out.String(), "nothing was paid") || len(payments(x)) != 0 {
		t.Errorf("answered no: %q, %d payments", x.out.String(), len(payments(x)))
	}
	if x.asking() {
		t.Error("still asking after an answer")
	}

	typeLine(x, "pay Example Resident 5")
	x.key(context.Background(), 3) // Ctrl-C
	if x.asking() || len(payments(x)) != 0 {
		t.Error("Ctrl-C paid, or left the question up")
	}

	typeLine(x, "pay Example Resident 5")
	x.out.Reset()
	typeLine(x, "y")
	if !strings.Contains(x.out.String(), "paid Example Resident L$5") || len(payments(x)) != 1 {
		t.Errorf("answered yes: %q, %d payments", x.out.String(), len(payments(x)))
	}
}

// A file of commands is a script even when a person typed the line that
// runs it.
func TestAFileOfCommandsIsNotAsked(t *testing.T) {
	x := newTestShell(t)
	atATerminal(x)
	knows(t, x, map[msg.UUID]string{examplePayee: "Example Resident"})
	answersMoney(t, x, 100, "")
	path := filepath.Join(t.TempDir(), "pays")
	if err := os.WriteFile(path, []byte("pay Example Resident 5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	x.out.Reset()
	typeLine(x, ". "+path)
	if x.asking() || !strings.Contains(x.out.String(), "needs --yes") || len(payments(x)) != 0 {
		t.Errorf("a sourced pay: asking %v, said %q, %d payments", x.asking(), x.out.String(), len(payments(x)))
	}
}

func TestARefusalPrintsTheReason(t *testing.T) {
	x := newTestShell(t)
	knows(t, x, map[msg.UUID]string{examplePayee: "Example Resident"})
	answersMoney(t, x, 100, "Insufficient funds.")
	if got := x.do(t, "pay --yes Example Resident 50"); !strings.Contains(got, "the grid refused: Insufficient funds.") {
		t.Errorf("a refused payment said %q", got)
	}
}

func TestAKeyNobodyHasIsNotPaid(t *testing.T) {
	x := newTestShell(t)
	answersMoney(t, x, 100, "")
	got := x.do(t, "pay --yes "+examplePayee.String()+" 5")
	if !strings.Contains(got, "pay pays avatars") || len(payments(x)) != 0 {
		t.Errorf("paying a key with no name said %q", got)
	}
}

// Money received is a line, with the payer labelled as anybody who sends
// anything is.
func TestMoneyReceivedIsALine(t *testing.T) {
	x := newTestShell(t)
	watching(t, x)
	knows(t, x, map[msg.UUID]string{examplePayee: "Example Resident"})

	x.grid.Relay(t, moneyReply(payTx, true, 105, "Example Resident paid you L$5.", 5001, examplePayee, testMe, 5, "for the lamp"))
	waits(t, x, `Example Resident paid you L$5 "for the lamp"; L$105 now`)

	x.groups.mu.Lock()
	x.groups.names = map[msg.UUID]string{payingGroup: "Example Group"}
	x.groups.mu.Unlock()
	group := moneyReply(payTx, true, 107, "", 6004, payingGroup, testMe, 2, "")
	group.TransactionInfo.IsSourceGroup = true
	x.grid.Relay(t, group)
	waits(t, x, "[Group] Example Group paid you L$2; L$107 now")

	// What this avatar pays is its own command's to report, and a
	// plain balance is nothing to say.
	x.out.Reset()
	x.grid.Relay(t, moneyReply(payTx, true, 102, "You paid Example Resident L$5.", 5001, testMe, examplePayee, 5, ""))
	x.grid.Relay(t, moneyReply(msg.UUID{}, true, 102, "", 0, msg.UUID{}, msg.UUID{}, -1, ""))
	time.Sleep(200 * time.Millisecond)
	if got := x.out.String(); strings.Contains(got, "paid") {
		t.Errorf("printed %q", got)
	}
}
