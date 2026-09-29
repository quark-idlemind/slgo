package main

// L$: the balance, paying an avatar, and money received.
//
// pay asks before it pays when somebody is at the prompt to answer, and
// otherwise will not pay without --yes: a script, a one-shot run, a
// session down a pipe and a file of commands have nobody to ask, and the
// next line of one is not an answer.
// Why: doc/money.md#slsh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

var moneyCommands = map[string]*command{
	"balance": {
		flags:    func() any { return new(helpOnly) },
		brief:    "this avatar's L$ balance, as the grid says it now",
		keywords: "money linden dollars l$ balance funds how much have account",
		man:      "balance",
		run:      cmdBalance,
	},
	"pay": {
		params:   "WHO AMOUNT [REASON]",
		flags:    func() any { return new(payOptions) },
		brief:    "pay an avatar L$, asking first; --yes where nobody can be asked",
		keywords: "money linden dollars l$ pay give send tip gift avatar person somebody",
		man:      "pay",
		run:      cmdPay,
	},
}

type payOptions struct {
	Yes  bool `getopt:"--yes -y    pay without asking; a script or a one-shot run needs it"`
	Help bool `getopt:"--help -h   show what this command takes"`
}

func cmdBalance(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o helpOnly
	rest, done, err := subOptions("balance", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(rest) != 0 {
		return usageError("balance", "balance takes nothing")
	}
	b, err := sh.s.Balance(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "L$%d\n", b)
	return nil
}

func cmdPay(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o payOptions
	rest, done, err := subOptions("pay", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(rest) < 2 {
		return usageError("pay")
	}
	who, name, rest, err := sh.whoAndRest(ctx, out, rest)
	if err != nil {
		return err
	}
	// A key names an avatar only if the grid has a name for it.
	if name == who.String() {
		return fmt.Errorf("the grid names nobody with the key %s; pay pays avatars", who)
	}
	if len(rest) == 0 {
		return usageError("pay", "how much, after who")
	}
	amount, err := strconv.Atoi(strings.TrimPrefix(rest[0], "L$"))
	if err != nil || amount < 1 {
		return usageError("pay", fmt.Sprintf("%q is not an amount; an amount is a whole number of L$ from 1 up", rest[0]))
	}
	reason := strings.Join(rest[1:], " ")

	if o.Yes {
		return sh.payNow(ctx, out, who, name, amount, reason)
	}
	if !sh.canAsk(ctx) {
		return fmt.Errorf("nothing was paid: nobody is at a prompt to be asked, so paying %s needs --yes", name)
	}
	sh.ask(&question{
		prompt: fmt.Sprintf("pay %s L$%d? [y/N] ", name, amount),
		yes: func(ctx context.Context) {
			if err := sh.payNow(ctx, sh.stdout(), who, name, amount, reason); err != nil {
				sh.errorf("pay: %v", err)
			}
		},
		no: "nothing was paid",
	})
	return nil
}

// payNow pays, and says what became of it.  A payment the grid's answer
// never came for is reported from the balance, and is a success when
// the balance says it was paid.
func (sh *Shell) payNow(ctx context.Context, out io.Writer, who msg.UUID, name string, amount int, reason string) error {
	tx, balance, err := sh.s.Pay(ctx, who, amount, reason)
	var lost *sl.PayUnconfirmed
	switch {
	case err == nil:
		fmt.Fprintf(out, "paid %s L$%d; L$%d left (transaction %s)\n", name, amount, balance, tx)
		return nil
	case errors.As(err, &lost) && lost.Outcome == sl.PaidUnconfirmed:
		fmt.Fprintf(out, "paid %s L$%d; L$%d left -- the grid's answer was lost, and the balance says it went\n",
			name, amount, balance)
		return nil
	}
	var refused *sl.PayRefused
	if errors.As(err, &refused) {
		return fmt.Errorf("%s refused: %s", refused.By, refused.Reason)
	}
	return err
}

// heardMoney prints a payment made to this avatar, with the payer named
// through Sender.Label like everybody else who sends anything.  The name
// is asked for off the watch loop, so a slow answer holds nothing up.
func (sh *Shell) heardMoney(ctx context.Context, m *sl.Money) {
	if m.Kind != sl.MoneyReceived || !m.Success || m.RefusedBy != "" {
		return
	}
	go func() {
		name := m.From.String()
		switch m.Sender() {
		case sl.SenderGroup:
			if n, ok := sh.groups.name(m.From); ok && n != "" {
				name = n
			}
		case sl.SenderPerson:
			if n := sh.s.Names(ctx, []msg.UUID{m.From}, 3*time.Second)[m.From]; n != "" {
				name = n
			}
		}
		why := ""
		// The grid's word for no reason, from the viewer's source.
		if m.Description != "" && m.Description != "Payment" {
			why = fmt.Sprintf(" %q", m.Description)
		}
		sh.noticef("%s paid you L$%d%s; L$%d now", m.Sender().Label(name), m.Amount, why, m.Balance)
	}()
}

// ---------------------------------------------------------- a question

// atPromptKey marks a command typed at the prompt, which is the one
// kind somebody is there to answer a question about.
type atPromptKey struct{}

// canAsk is whether a question can be put to somebody: the command was
// typed at the prompt of a terminal, and not read from a file.
func (sh *Shell) canAsk(ctx context.Context) bool {
	typed, _ := ctx.Value(atPromptKey{}).(bool)
	return typed && sh.depth == 0 && !sh.term.Plain()
}

// question is a yes or no put at the prompt.
type question struct {
	prompt string
	yes    func(context.Context)
	no     string
}

// ask puts a question.  The next line typed answers it: y or yes is yes,
// and anything else, Ctrl-C and Ctrl-D included, no.
func (sh *Shell) ask(q *question) {
	sh.mu.Lock()
	sh.question = q
	sh.mu.Unlock()
	sh.setMode(modeAsk)
}

// asking reports whether a question is waiting for its answer.
func (sh *Shell) asking() bool {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	return sh.mode == modeAsk
}

// answered takes the answer to the question being asked.
func (sh *Shell) answered(ctx context.Context, line string) {
	sh.mu.Lock()
	q := sh.question
	sh.question = nil
	sh.mu.Unlock()
	sh.setMode(modeCommand)
	if q == nil {
		return
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		q.yes(ctx)
	default:
		fmt.Fprintln(sh.stdout(), q.no)
	}
}
