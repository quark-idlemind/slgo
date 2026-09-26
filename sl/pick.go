package sl

import (
	"fmt"
	"slices"
	"strings"

	"github.com/quark-idlemind/slgo/msg"
)

// Named is what PickNamed and AllNamed choose among: an inventory
// Entry, or a TaskItem inside an object.  Only this package's types
// satisfy it; anything else is chosen among by PickNamedFunc.
type Named interface {
	nameAndID() (string, msg.UUID)
}

func (e Entry) nameAndID() (string, msg.UUID)     { return e.Name, e.ID }
func (it TaskItem) nameAndID() (string, msg.UUID) { return it.Name, it.ID }

// PickNamed is the one of items called name.
//
// The name is matched exactly, in the case it has, and it has to pick
// out one thing:
//
//   - one called name is the answer;
//   - several called name are refused, listing their ids, each of which
//     names one of them;
//   - none called name is refused, naming any that differ from it only
//     in case, as a hint.
//
// Case matters because the grid keeps "Script" and "script" as two
// names, and several are refused rather than the first taken because
// agent inventory lets a folder hold any number of one exact name.  An
// object renames an exact duplicate, so inside one only the near miss
// can happen.  Why: doc/names.md#measured
//
// what is the noun a refusal uses for what was looked among, "folder"
// or "script", and "" for anything.  where says where it looked, as a
// phrase that can follow the noun: "here", "in Objects".  The refusal
// is a *NameError.
func PickNamed[T Named](items []T, name, what, where string) (T, error) {
	return PickNamedFunc(items, name, what, where, T.nameAndID)
}

// AllNamed is everything in items called name exactly, in the order
// given, for a command that acts on every one of a name -- a listing,
// or a delete told to take them all.  None is refused as PickNamed
// refuses it.
func AllNamed[T Named](items []T, name, what, where string) ([]T, error) {
	return AllNamedFunc(items, name, what, where, T.nameAndID)
}

// PickNamedFunc is PickNamed for things whose name and id are read by
// key: a worn attachment, a link in the Current Outfit folder, an
// offer.  The rule and the refusals are PickNamed's.
func PickNamedFunc[T any](items []T, name, what, where string, key func(T) (string, msg.UUID)) (T, error) {
	var zero T
	found, err := AllNamedFunc(items, name, what, where, key)
	if err != nil {
		return zero, err
	}
	if len(found) > 1 {
		e := &NameError{Name: name, What: what, Where: where}
		for _, x := range found {
			_, id := key(x)
			e.IDs = append(e.IDs, id)
		}
		return zero, e
	}
	return found[0], nil
}

// AllNamedFunc is AllNamed for things whose name and id are read by
// key.
func AllNamedFunc[T any](items []T, name, what, where string, key func(T) (string, msg.UUID)) ([]T, error) {
	var found []T
	var near []string
	for _, x := range items {
		n, _ := key(x)
		switch {
		case n == name:
			found = append(found, x)
		case strings.EqualFold(n, name) && !slices.Contains(near, n):
			near = append(near, n)
		}
	}
	if len(found) == 0 {
		return nil, &NameError{Name: name, What: what, Where: where, Near: near}
	}
	return found, nil
}

// A NameError is a name that does not pick out one thing: nothing is
// called it, or several things are.
type NameError struct {
	Name  string // the name asked for
	What  string // what was looked among, "folder" or "script"; "" is anything
	Where string // where, as a phrase: "here", "in Objects"

	IDs  []msg.UUID // everything called Name, when that is several
	Near []string   // the names that differ from Name only in case, when none is Name
}

func (e *NameError) Error() string {
	var b strings.Builder
	if len(e.IDs) > 1 {
		what := "things"
		if e.What != "" {
			what = e.What + "s"
		}
		fmt.Fprintf(&b, "%d %s", len(e.IDs), what)
		if e.Where != "" {
			b.WriteString(" " + e.Where)
		}
		fmt.Fprintf(&b, " are called %q:", e.Name)
		for _, id := range e.IDs {
			fmt.Fprintf(&b, "\n  %s", id)
		}
		return b.String()
	}
	if e.What != "" {
		fmt.Fprintf(&b, "no %s %q", e.What, e.Name)
	} else {
		fmt.Fprintf(&b, "nothing called %q", e.Name)
	}
	if e.Where != "" {
		b.WriteString(" " + e.Where)
	}
	if len(e.Near) > 0 {
		quoted := make([]string, len(e.Near))
		for i, n := range e.Near {
			quoted[i] = fmt.Sprintf("%q", n)
		}
		last := len(quoted) - 1
		either := quoted[last]
		if last > 0 {
			either = strings.Join(quoted[:last], ", ") + " or " + quoted[last]
		}
		fmt.Fprintf(&b, "; did you mean %s?", either)
	}
	return b.String()
}
