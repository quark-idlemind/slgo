package server

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/internal/pay"
	"github.com/quark-idlemind/slgo/msg"
)

// A session logs from its first message.  The grid can answer a payment
// the moment the circuit is up, and the handler that logs it reads the
// session's Log then; a Log assigned after StartAgent returned raced
// that read, or missed the line.
// Why: doc/money.md#a-log-that-is-there-before-the-first-reply
func TestASessionLogsFromTheFirstMessageItHears(t *testing.T) {
	sim := newSim(t)
	defer sim.close()
	var logins atomic.Int64
	hs := loginServer(t, sim, &logins, nil)

	me := msg.MustParseUUID("876e7e57-7e57-c0de-8597-66b760a8cb5f")
	sim.whenArrived(func() {
		reply := &msg.MoneyBalanceReply{}
		reply.MoneyData.TransactionSuccess = true
		reply.MoneyData.MoneyBalance = 94
		reply.MoneyData.Description = []byte("You paid Example Resident L$5.\x00")
		reply.TransactionInfo.TransactionType = pay.TransGift
		reply.TransactionInfo.SourceID = me
		reply.TransactionInfo.DestID = payee
		reply.TransactionInfo.Amount = 5
		sim.send(reply, msg.FlagZerocoded)
	})

	log := &said{}
	srv := New()
	srv.SetLog(log.printf)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, err := srv.StartAgent(ctx, "example",
		agent.Login{First: "Example", Last: "Resident", Password: "x", URL: hs.URL},
		agent.Options{Timeout: 10 * time.Second, SkipCaps: true, Idle: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Agent().Close()
	if h.Log == nil {
		t.Fatal("a session has nowhere to log when StartAgent returns")
	}
	waitFor(t, 5*time.Second, "the grid's answer to be logged", func() bool {
		return strings.Contains(log.String(), `example: pay: the grid says "You paid Example Resident L$5."`)
	})
}
