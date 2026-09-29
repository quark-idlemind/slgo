package pay

// The record of what a profile has paid, which is what the daily limit
// is counted from.  It is a file so that the total outlives the process
// counting it: slgod restarting, or one direct session after another.
//
// One line a payment: when, how much, and to whom.  A payment is dropped
// once it is a day old.  Written whole, through a temporary file and a
// rename, mode 600; read again before every payment, so that two
// processes paying for one profile each see what the other wrote.

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// LedgerPath is where a profile's record is kept in a directory.
func LedgerPath(dir, profile string) string {
	return filepath.Join(dir, "pay-"+profile)
}

// Entry is one payment in the record.
type Entry struct {
	At     time.Time
	Amount int
	To     msg.UUID
}

// readLedger is what the file holds.  One that is not there holds
// nothing; one that cannot be read is an error, since carrying on would
// count from nothing.
func readLedger(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []Entry
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 3 {
			return nil, fmt.Errorf("%s:%d: want a time, an amount and a key", path, n)
		}
		at, err := time.Parse(time.RFC3339, f[0])
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		amount, err := strconv.Atoi(f[1])
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		to, err := msg.ParseUUID(f[2])
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		out = append(out, Entry{At: at, Amount: amount, To: to})
	}
	return out, sc.Err()
}

// writeLedger replaces the file with these entries.
func writeLedger(path string, es []Entry) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("# What this profile has paid through slgo, written by slgod and by sl.\n")
	b.WriteString("# One payment a line; a payment is dropped a day after it was made.\n")
	for _, e := range es {
		fmt.Fprintf(&b, "%s %d %s\n", e.At.UTC().Format(time.RFC3339), e.Amount, e.To)
	}

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// recent is the entries inside the window before now.
func recent(es []Entry, now time.Time) []Entry {
	var out []Entry
	for _, e := range es {
		if now.Sub(e.At) < Window {
			out = append(out, e)
		}
	}
	return out
}

// total is what the entries come to.
func total(es []Entry) int {
	n := 0
	for _, e := range es {
		n += e.Amount
	}
	return n
}
