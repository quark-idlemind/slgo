`no` declines one of the things `waiting` lists, by its number, and
tells whoever asked.

That last part is the difference between `no` and `ignore`.  A declined
teleport, item, friendship, permission or group invitation sends a
refusal, so the person or script on the other end stops
waiting.  `ignore` says nothing to anybody.

    no 2

A dialog is the exception, and it says so when it happens.  There is
no way to refuse a blue menu -- it can be answered or left -- so `no`
on a dialog or a text box drops it here and lets it expire where it
was raised.

Declining something that was being ignored also stops it being
ignored, since it is dealt with now either way.

See also: `waiting`, `answer`, `ignore`.
