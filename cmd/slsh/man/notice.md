`notice` lists the group notices heard lately, and with a number
prints one of them in full.

    notice 2

A notice posted to one of this avatar's groups is announced as it
arrives, in one line: who posted it, the group, and the subject.

    12:03:04 * notice 1 from Example Resident in Example Group: Meeting moved to Friday

The number is what `notice N` takes.  The body, and the name of any
item attached, are left for that.

## Options

It takes none but `-h`.  With no number it lists the notices still
kept, one line each in the same form, oldest first.  With a number it
prints that one: its number, when it arrived, who posted it, the
group, the subject, the item attached if there is one, and then the
body as it was written.

## How long a notice is kept

For 15 minutes after it arrives, and then it is forgotten.  The
`notice_keep` setting changes that:

    set notice_keep 1h

A notice that repeats one still kept -- the same group, subject and
body -- is not announced and not numbered, and `notice` lists only the
first.

## The number belongs to the notice

Numbers start at 1 and climb for as long as the shell runs.  One is
never given to a second notice, even after the first is forgotten, so
a number read off the screen means that notice or nothing.  Asking
for one that has been forgotten says so, and so does asking for one
that never arrived.

## The group's name

The group is named from this avatar's own list of groups, the one
`group` prints.  A notice from a group that list does not have yet --
it arrives on its own shortly after login -- is shown with the first
eight characters of the group's key instead, and `notice N` gives the
whole key.

## What it does not do

An item attached to a notice is named and not taken.  Nothing here
accepts or declines it.

## Examples

    notice
    notice 3

See also: `group` for the groups this avatar is in, `set` for
`notice_keep`, and `waiting` for the things that want an answer.
