# Identifiers and names: every form, and what sees each

This repository is public and most of it was measured on a live grid, so
nearly everything captured there names somebody. `CLAUDE.md` has the rule:
nothing that identifies a real resident, their land, their objects or their
groups belongs in a file, a tag or a commit message. This page is the
catalogue behind it: every shape an identifier or a name has been seen to
take here, or can take, and which check, if any, would notice it. A check
that is not listed against a form does not exist, and the form is left to
reading what came back before committing it.

Two kinds of thing are kept out, and they are kept out differently.

An **identifier** is a value with a shape: a uuid, a password digest, a
login's `mac` and `id0`, a grid square. A rule can recognise the shape, so
the rule is that an invented one carries a signature and a real one is
refused. That needs no list of what is real, and it runs for everybody.

A **name** is words, and no rule can tell a real name from an invented
one. Names are matched against a list of the real ones, kept off the tree,
and an allowlist of the names that may appear is coming to replace it.
What people said -- chat, instant messages, notices, profiles -- cannot be
matched at all and is always replaced.

## The signature

An invented uuid is

    xxxx7e57-7e57-c0de-xxxx-xxxxxxxxxxxx

four random hex digits and `7e57`, then `7e57-c0de`, then random, as in
`ab3f7e57-7e57-c0de-9d41-6c2e08f1b7a5`. Without hyphens, hex digits five
to sixteen are `7e577e57c0de`. Any other 128-bit value -- a `$1$` password
digest, a `mac`, an `id0` -- is the same thing without hyphens,
`ab3f7e577e57c0de9d416c2e08f1b7a5`. An id elided in documentation is its
first group, `ab3f7e57-...`, and since that group ends in `7e57` the
elision shows it was invented.

`tools/new-id` makes one, and `tools/new-id -n 5` makes five, sorted, which
is how a family of ids that must sort as a family is made. It refuses a
first group that any id in the tree already uses, so an elided id names one
id. `tools/new-id --hash` prints the 32-digit form.

There is no other way in. Linden Lab's own published constants -- the
built-in animations, the default texture, the null key -- are listed with
where Linden publishes each in `tools/known-uuids`; the one digest a test
has to work out for real from a string it names is in
`tools/known-hashes`. Everything else carries the signature, a placeholder
included: a repeated character and bytes in order were an exception once,
and were removed because an exception is a place to hide a real id. A
digest or id that is all zeros is signed too, except the null key, which
is written with its hyphens.

Nothing is kept from the id that an invented one replaces. An earlier rule
kept its first four characters so that things sorting by id still sorted;
that left a fragment of the real id in the tree. Now a conversion hands the
fresh ids out in the order of the old ones, which keeps every comparison
without keeping any of the characters.

## Uuids, as written

`tools/scan-ids` finds all of these, normalises each to 32 lowercase hex
digits, and `tools/check-identities` refuses any that is not signed and
not in `tools/known-uuids`. The examples are one invented id.

- **Hyphenated**, in either case, in braces, in quotes, or inside a URL:
  `{AB3F7E57-7E57-C0DE-9D41-6C2E08F1B7A5}`, a capability or an asset or
  texture URL, `secondlife:///app/agent/<id>/about`, LLSD notation
  `u<id>`, JSON, XML `<uuid>`.
- **Thirty-two hex digits with no hyphens**: `ab3f7e577e57c0de9d416c2e08f1b7a5`.
  The same shape is a digest, a `mac` or an `id0`, and is checked the same
  way against the signature, `tools/known-hashes` and `tools/known-uuids`.
  The all-zero form is refused, because without hyphens it is a blank
  digest and not the null key.
- **A hexdump**: four groups of eight, `ab3f7e57 7e57c0de 9d416c2e 08f1b7a5`
  (`sl/texture_test.go`); byte pairs, `ab 3f 7e 57 ...`, or groups of four
  or sixteen, separated by a space, colon, comma, dot or underscore, and
  wrapped across lines with or without a comment marker between. A run
  longer than an id is read from its start, so a trailing byte after the
  four groups does not hide them.
- **A Go byte literal**, over as many lines as it likes, with comments
  among the bytes: `msg.UUID{0xab, 0x3f, 0x7e, 0x57, 0x7e, 0x57, 0xc0,
  0xde, 0x9d, 0x41, 0x6c, 0x2e, 0x08, 0xf1, 0xb7, 0xa5}`, a
  `[16]byte{...}` in hex or decimal, a `[]byte{...}` of sixteen when
  every one is written `0x..` (a slice with a decimal in it is a packet or
  a table, and sixteen of anything is not an id). A `msg.UUID{...}` of four
  bytes or more that is not sixteen is the start of an id, with the rest
  zero, and is read as an elided id: its third and fourth bytes must be
  `0x7e, 0x57`. That kind of literal makes an id the test cannot print as
  signed, and where one was found the test now names a whole signed id.
- **String escapes**: `"\xab\x3f\x7e\x57\x7e\x57\xc0\xde\x9d\x41\x6c\x2e\x08\xf1\xb7\xa5"`,
  exactly sixteen in a row.
- **Base64**: `qz9+V35XwN6dQWwuCPG3pQ==`. A run of twenty base64 characters
  or more is decoded. It is an id when it decodes to exactly sixteen
  bytes (22 characters, or 24 with padding, and not a word made only of
  letters). Longer bytes that are LLSD binary -- they open with the
  `<? LLSD/Binary ?>` header, or sit in an XML `<binary>` and parse whole
  as a binary value -- are parsed (undef, booleans, `i`, `r`, `d`, `u`,
  `b`, `s`, `l`, arrays and maps, nested as deep as they go), and every
  `u` is checked: signed or known, or refused. Nothing is assumed from an
  id's shape, since it is not certain that Second Life's ids are version 4
  uuids. A document with the header that does not parse to its end is
  reported as unparseable LLSD binary, so a capture that is not valid
  LLSD gets a person's look. Longer bytes in a `<binary>` with no header
  that do not parse are a packed field, which can be any bytes, and are
  not looked at; so is base64 anywhere else that is longer than an id.
- **A format string that makes ids**: `"ab3f7e57-7e57-c0de-9d41-%012d"`.
  Each directive stands for the digits it is wide, and every digit the
  string writes in the signature's place must agree with it. A test that
  counts makes ids this way, and the ids it makes are the ones its other
  literals write out, so the fixed part is converted with them.
- **Elided to the first group**: `ab3f7e57-...`, `ab3f7e57-…`, with or
  without the hyphen, or to the first few groups. Its first group must end
  in `7e57`, or be the first group of a known uuid. A first group quoted
  bare, with nothing after it, cannot be told from any other eight hex
  digits and is not found; one was, where a test printed an id's first
  eight characters, and it was converted by hand.
- **In a file name or a tag name**, and in a tag's message: the checker
  reads each path it is given as text, and `--all` reads the tags.
- **In a commit message**: `tools/check-identities --message FILE`, which
  `--install-hook` runs from a `commit-msg` hook. A pull request
  description, a release note and an issue title are not files and are
  not checked by anything.
- **Derived from another id**: combined with it, XORed, hashed with MD5.
  A literal derived from a real id lacks the signature and is caught. One
  computed at test time from a signed id is fine, and nothing is written
  down for it.
- **As two uint64s or a big integer**: only review catches this.

What an id names makes no difference to the rule: agents, sessions, secure
sessions, regions, parcels, objects, items, assets, groups, transactions,
notices and folders are all uuids, and one rule covers all of them.

## Grid squares

A region's grid square is one map lookup from its name, so it is as much
the region as the name is. An invented square has `0xAA` as the high byte
of both coordinates (43520 to 43775), and a placeholder has both below 16;
Linden's sandboxes are in `tools/known-squares`. `tools/check-identities`
reads a square out of `RegionHandle(x, y)`, the handle in decimal, in hex
as a constant or as spaced or unspaced bytes or a Go byte list, and as
LLSD's base64. A bare `(x, y)` is not looked for -- too many pairs of
numbers are something else -- and is left to the list of real names. A
square is also written as a map tile URL, as global metres, and inside a
SLURL or a `maps.secondlife.com` link, and none of those is looked for.

## Names

None of these is enforced by a rule. They are described so that a change
that adds one looks for it in every guise, and so that the allowlist that is
coming knows what to match.

- **Avatars**: a legacy `First Last`, `First Resident`, the username
  `first.last` or `first` in lower case, a display name, a first name alone
  in prose, an slgod profile handle, an agent SLURL, the name in a chat or
  an IM header, an `objects --owner` listing, a friend or group member list.
- **Regions**: the name; the name URL-encoded; inside a SLURL and a
  `maps.secondlife.com` link; a prefix used as a map search key; the
  simulator's host name, address and port; the region id; and the grid
  square in every form above, with global metres and the map tile URLs.
- **Parcels**: the name and the description; the local id with its region;
  the parcel id; the flags word and the other numbers read off About Land;
  the area with the position; and a landmark's default name, `Parcel,
  Region (x, y, z)`, which carries the region and the position at once.
- **Groups**: the name, the id, a role's name, a title, a notice's text.
- **Objects and inventory**: names and descriptions, the creator's and the
  owner's names, a script's and a notecard's name, a notecard's text.
- **Anything people wrote**: chat, instant messages, notices, profiles,
  picks, classifieds.

### How a name is disguised

A name that was found and replaced is rarely replaced everywhere, because
it is rarely written one way.

- In any case, and in lower case in a test of case-insensitive matching.
- As HTML entities, JSON `\u` escapes, URL encoding, XML entities or
  Markdown escapes: a name with an ampersand is `&amp;` in a guide.
- Welded into an identifier -- camelCase, PascalCase, snake_case,
  kebab-case -- in a Go name, a test name or a file name.
- Wrapped across lines and across comment markers.
- Truncated: the first word alone, or a prefix a test searches by.
- In a generated file: `cmd/slsh/askindex.txt`, and the published
  handbook bundle, which are made from the man pages and the guide and
  carry whatever those carried.
- In a commit message, a tag message, a release note, or a page published
  outside the repository, where no sweep of the tree reaches.

## Other identifying values

- **Addresses and host names**: a simulator's, and the owner's network's.
  An address outside loopback, private and documentation ranges is refused.
  A host name is not an address and is not recognised.
- **Password digests, MFA tokens and secrets**: a digest is a 32-digit
  value and takes the signature; a token has no shape and is replaced
  when it is read.
- **Machine ids**: `mac` and `id0` are 32-digit values and take the
  signature. A raw hardware address and a serial number have no shape a
  rule can use, and are invented and not kept.
- **Email addresses and real names**, which no rule recognises.
- **L$ amounts with a transaction id**: the id is a uuid; the amount is
  not found, and a figure beside a name or a time says who paid.
- **A timestamp together with a region**, which places somebody at a
  moment.

## How each is enforced

| What | Enforced by |
| --- | --- |
| uuid, in any form above | the signature rule: `tools/scan-ids`, run by `tools/check-identities` and by `go test` (`tools/tree_test.go`) |
| password digest, `mac`, `id0` | the signature rule, with `tools/known-hashes` for the one real digest |
| elided id | the signature rule on the first group |
| id in a file name, tag or commit message | the signature rule (`--all`, `--message`) |
| id as two uint64s or a big integer, or derived by arithmetic | review |
| grid square | the square rule: `0xAA` marked, or listed in `tools/known-squares` |
| address | the address rule |
| avatar, region, parcel, group, object names | the local list of real names kept off the tree (a backstop), and the allowlist when it comes |
| names in a disguise | the same list, which finds an entry in any case and written with HTML entities; the other disguises are for review |
| host names, email addresses, real-life names, tokens, serials | the local list, and review |
| chat, IMs, notices, profiles, picks, classifieds | reading what came back before committing it: captured speech is always replaced, because no rule can recognise it |

`tools/check-identities` runs every check on every run and exits 1 at the
end if any found something; only the checks of names need the list
kept off the tree, and without it those are skipped with a note while
the rest run. `go.sum` is not read, since it is a list of hashes of other
people's code and base64 inside it means nothing.
