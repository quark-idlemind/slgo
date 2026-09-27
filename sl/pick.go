package sl

import (
	"github.com/quark-idlemind/slgo/internal/pick"
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
	return pick.One(items, name, what, where, key)
}

// AllNamedFunc is AllNamed for things whose name and id are read by
// key.
func AllNamedFunc[T any](items []T, name, what, where string, key func(T) (string, msg.UUID)) ([]T, error) {
	return pick.All(items, name, what, where, key)
}

// A NameError is a name that does not pick out one thing: nothing is
// called it, or several things are.  Name is what was asked for, What
// what was looked among ("folder", "script", or "" for anything), and
// Where where, as a phrase ("here", "in Objects").  IDs is everything
// called Name when that is several, and Near the names that differ from
// Name only in case when none is Name.
//
// It is internal/pick's, which agent's lookups share.
type NameError = pick.NameError
