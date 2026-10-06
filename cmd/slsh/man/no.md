`no` declines one of the things `waiting` lists, by its number, and
tells whoever asked.

That last part is the difference between `no` and `ignore`.  A declined
teleport, item, friendship, permission or group invitation sends a
refusal, so the person or script on the other end stops
waiting.  `ignore` says nothing to anybody.

    no 2

A dialog is the exception, and it says so when it happens.  There is
no way to refuse a blue menu -- it can be answered or left -- so `no`
on a dialog or a text box is the viewer's Ignore: nothing is sent to
the grid, as the viewer sends nothing for Ignore, and the object's own
listen is left to expire where it was raised.  slgod is told, so it
stops listing the dialog to every program attached to the avatar, and
`no` says so.  If slgod cannot be told, the error is printed and the
dialog stays listed.

A teleport request is the other exception.  Nothing can be sent to
refuse one -- a viewer's No button sends nothing either, and the person
who asked is never told -- so `no` tells slgod instead, and the request
leaves the listing of every other client on the avatar as well as this
one.

Declining something that was being ignored also stops it being
ignored, since it is dealt with now either way.

See also: `waiting`, `answer`, `ignore`.
