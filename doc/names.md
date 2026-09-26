# Names in inventory and in objects

A name is how a person points at a folder or an item in inventory, or
at something inside a rezzed object. The grid does not promise that a
name points at one thing, and it does not treat two spellings that
differ only in case as one name. This page says what was measured, and
what `sl`, `slsh` and `slbotd` do about it.

## Measured

On Agni, on 2026-09-26:

- Scripts named `Script` and `script` were put into one prim. Its
  contents listed both: `Script` and `script`. Inside an object, names
  that differ only in case coexist.
- `Script` was put into the same prim a second time. The contents
  became `Script`, `Script 1` and `script`. The object renamed the
  newcomer, so inside an object an exact name is unique.
- In agent inventory, folders `Probe`, `probe` and `Probe` all
  coexisted. Agent inventory allows names that differ only in case,
  and exact duplicates.

Only scripts were put into the prim. That a notecard or a texture is
renamed the same way is inferred, not measured. Only folders were made
in agent inventory for this. That a folder holds several items of one
exact name was seen before this measurement, and is why `rm --newest`
and `--oldest` exist.

## What is done with a name

The rule is `sl.PickNamed` (`sl/pick.go`). `sl`'s walk down a path
uses it, as do `slsh`'s paths and its names inside an object, and
`slbotd`'s paths.

- **Case matters.** A name is matched exactly, in the case it has.
  Asked for `object` among `Object`, `object` and `objectT`, a rule that
  ignored case could take one that was not meant.
- **One name, one thing.** Several things called the name exactly are
  refused, and the refusal lists their ids; each id names one of them.
  Taking the first is not safe even for a command that only reads:
  `slsh -c "cat notes" > file` in a script would write whichever came
  first, and nothing would say so.
- **A miss names the near misses.** A name nothing is called is
  refused, and the refusal names what differs from it only in case, if
  anything does: `nothing called "object" here; did you mean "Object"?`

`sl.AllNamed` is the same match for the two commands that deal with
every one of a name: `slsh`'s `ls` of a path, which lists them, and
`rm`, which refuses them unless `--newest`, `--oldest` or
`--remove-all-copies` says which.

Inside an object an exact name is unique, so only the near miss can
happen there, and `rm --in` refuses `--remove-all-copies`: there is
never more than one to remove.

A person's name is different. The grid ignores case in one, and so do
`slsh` and `slbotd`.

## Not yet moved

Some names from inventory are still matched ignoring case: a landmark
in `landmark`, in `slsh` and in `slbotd`; a worn object, or its link in
the Current Outfit folder, in `detach`, in both; and an offer in
`slsh`'s `accept` and `decline` (`sl.InventoryOffersFor`). Each of them
refuses a name that means several things, so none picks between two;
each will take a case variant when it is the only thing that matches.
`TestNoInventoryNameIsMatchedIgnoringCase`, in `sl/pick_test.go`, lists
the ones it can find, and fails on a new one.
