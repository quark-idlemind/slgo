// Package pay is what a profile lets a program pay, and the record of
// what it has paid.
//
// It is the one place the rules are read and checked.  slgod checks a
// client's MoneyTransferRequest against them before it forwards one, and
// sl checks its own for a session it holds itself, with no daemon in
// between; both do it through a Gate.  A viewer handed a session is not
// checked: that is a person using the viewer's own pay dialog, and these
// rules are about programs.
// Why: doc/money.md#the-rules
package pay

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// Transaction types, from the viewer's lltransactiontypes.h.  A payment
// to an avatar is a gift and one to an object is its own type; the
// viewer's pay dialog sends one or the other (llfloaterpay.cpp:622-648).
const (
	TransGift      = 5001
	TransPayObject = 5008
)

// The defaults for a profile that says pay = on and no more.
const (
	DefaultMax   = 10
	DefaultDaily = 10
)

// Window is how far back the daily total looks.
const Window = 24 * time.Hour

// MaxReason is the longest reason a payment may carry, in bytes: the
// viewer's pay dialog takes no more (floater_pay.xml:109).
const MaxReason = 127

// RefusedBy is what slgod signs a refusal with: the from_client of the
// MoneyBalanceReply it makes up for the client whose payment it refused.
// Why: doc/money.md#how-a-refusal-reaches-the-client
const RefusedBy = "slgod"

// Rules is what a profile says about paying, as it said it.  The zero
// Rules pays nobody.
type Rules struct {
	// On is pay = on.  Nothing is paid without it.
	On bool

	// Max is pay_max, the most one payment may be, and Daily is
	// pay_daily, the most all of them may come to in any 24 hours.
	// Zero is the default.
	Max   int
	Daily int

	// To is each pay_to line: a name, a uuid, or * for anyone.  An
	// empty list pays nobody.
	To []string
}

// Set reads one profile line.  pay_to adds to the list; the others
// replace what an earlier line said.
func (r *Rules) Set(key, value string) error {
	value = strings.TrimSpace(value)
	switch key {
	case "pay":
		switch strings.ToLower(value) {
		case "on", "yes", "y", "true", "t", "1":
			r.On = true
		case "off", "no", "n", "false", "f", "0":
			r.On = false
		default:
			return fmt.Errorf("want on or off, got %q", value)
		}
	case "pay_max", "pay_daily":
		n, err := strconv.Atoi(strings.TrimPrefix(value, "L$"))
		if err != nil || n < 1 {
			return fmt.Errorf("want a whole number of L$ from 1 up, got %q; pay = off stops paying", value)
		}
		if key == "pay_max" {
			r.Max = n
		} else {
			r.Daily = n
		}
	case "pay_to":
		if value == "" {
			return fmt.Errorf("want a name, a key or *")
		}
		r.To = append(r.To, value)
	default:
		return fmt.Errorf("%q is not a pay setting", key)
	}
	return nil
}

// Lines are the profile lines that say these rules, in the order a
// profile is written in, for writing one back.
func (r Rules) Lines() [][2]string {
	if !r.On && r.Max == 0 && r.Daily == 0 && len(r.To) == 0 {
		return nil
	}
	on := "off"
	if r.On {
		on = "on"
	}
	out := [][2]string{{"pay", on}}
	if r.Max > 0 {
		out = append(out, [2]string{"pay_max", strconv.Itoa(r.Max)})
	}
	if r.Daily > 0 {
		out = append(out, [2]string{"pay_daily", strconv.Itoa(r.Daily)})
	}
	for _, to := range r.To {
		out = append(out, [2]string{"pay_to", to})
	}
	return out
}

// MaxPayment and DailyLimit are the limits in force, defaults filled in.
func (r Rules) MaxPayment() int { return orDefault(r.Max, DefaultMax) }
func (r Rules) DailyLimit() int { return orDefault(r.Daily, DefaultDaily) }

func orDefault(n, def int) int {
	if n <= 0 {
		return def
	}
	return n
}

// anyone is whether pay_to = * is among the lines.
func (r Rules) anyone() bool {
	for _, to := range r.To {
		if to == "*" {
			return true
		}
	}
	return false
}

// WholeName is a pay_to name as the grid's whole-name search wants it:
// a dot read as the space it stands for in a username, and a name of one
// word given the last name every account without one has.
func WholeName(name string) string {
	words := strings.Fields(strings.ReplaceAll(name, ".", " "))
	if len(words) == 1 {
		words = append(words, "Resident")
	}
	return strings.Join(words, " ")
}

// CString is s as a message's Variable string field carries it, cut to
// fit a field of at most max bytes with its NUL.
func CString(s string, max int) []byte {
	if len(s) > max-1 {
		s = s[:max-1]
	}
	return append([]byte(s), 0)
}

// Transfer is what a MoneyTransferRequest asks for, read off the message.
type Transfer struct {
	Type      int
	Dest      msg.UUID
	DestGroup bool
	Amount    int
	Reason    string
}

// ReadTransfer is what a MoneyTransferRequest asks for.
func ReadTransfer(m *msg.MoneyTransferRequest) Transfer {
	d := m.MoneyData
	return Transfer{
		Type:      int(d.TransactionType),
		Dest:      d.DestID,
		DestGroup: d.Flags&FlagDestGroup != 0,
		Amount:    int(d.Amount),
		Reason:    trimNul(d.Description),
	}
}

// FlagDestGroup is the Flags bit saying the destination is a group
// (lltransactionflags.cpp:37).
const FlagDestGroup = 2

func trimNul(b []byte) string {
	if n := len(b); n > 0 && b[n-1] == 0 {
		b = b[:n-1]
	}
	return string(b)
}
