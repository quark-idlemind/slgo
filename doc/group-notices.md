# Group notices

A notice posted to a group reaches each member as an
`ImprovedInstantMessage` with dialog 32, `IM_GROUP_NOTICE` in the
viewer's `llinstantmessage.h`. Nothing on this page was measured here:
it is how the viewer's own code reads the message, from its source
(`llimprocessing.cpp`), and `sl.GroupNoticeFrom` in `sl/notice.go`
reads it the same way. Where they differ, it says so.

## The message

`Message` is the subject and the body joined by `|`. The viewer splits
on every `|` and keeps the first two pieces, so a body with a `|` in it
loses the rest there; `sl` splits on the first only and keeps the
rest of the body.

`FromAgentName` is the name of the member who posted it. `AgentID`,
which `sl.IM` calls `From`, is either that member or the group itself:
the viewer checks which, and when it is the group it looks the poster
up by name. So `sl` does not file the name under `From` when `From` is
the group, or when the bucket does not say which group it is.

`ID` is the transaction an item attached to the notice would be
accepted or declined with, by dialogs 33 and 34.

## The binary bucket

    offset  size  field
    0       1     has_inventory, nonzero when an item is attached
    1       1     asset_type of that item
    2       16    group_id
    18      ...   the item's name, ending in a NUL

The viewer drops a notice whose bucket is shorter than 19 bytes or does
not end in a NUL. `sl` keeps it: a bucket shorter than 18 bytes leaves
the group unknown and no item, and a name with no NUL runs to the end
of the bucket.

The viewer has a second reading, used when the message arrives with a
separate group id rather than through the simulator's UDP circuit, in
which the bucket is text separated by `|`. `sl` reads only the
`ImprovedInstantMessage`, and not that form.
