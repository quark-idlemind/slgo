// Package pick is the rule sl.PickNamed documents: an inventory name is
// matched exactly, in the case it has, and has to pick out one thing.
// It is here rather than in sl so that agent, which sl imports, keeps to
// the same rule; sl.PickNamedFunc, sl.AllNamedFunc and sl.NameError are
// this package's One, All and NameError.
// Why: doc/names.md#measured
package pick

import (
	"fmt"
	"slices"
	"strings"

	"github.com/quark-idlemind/slgo/msg"
)

// One is the one of items called name, its name and id read by key.
// Several called name are refused with their ids, and none with the
// names that differ from name only in case.
func One[T any](items []T, name, what, where string, key func(T) (string, msg.UUID)) (T, error) {
	var zero T
	found, err := All(items, name, what, where, key)
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

// All is everything in items called name exactly, in the order given.
// None is refused as One refuses it.
func All[T any](items []T, name, what, where string, key func(T) (string, msg.UUID)) ([]T, error) {
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
