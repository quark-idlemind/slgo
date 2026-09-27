# Who a session is

`sl.Info` is who a session is: the avatar, the session id, the region
it is in and that region's capabilities. `Backend.Info` hands back the
current one, and it changes. The comments in `sl/backend.go` say what
changes and when; this page is why it was written that way.

## A session id that plainly cannot send

`Backend.Info` used to say that it did not change, and that was wrong.
A daemon may re-establish the grid session under an attached client --
same avatar, new session id, new circuit code, new capabilities -- and
a client that goes on sending the old session id is sending into
nothing. The simulator discards it in silence; there is no error and no
notice, and receiving carries on working, which is what made it take
fourteen hours to notice.

`Info.Region` used to say that it was the attach-time region and was
never revised, and that a field which quietly changed under a caller
would be worse than one that plainly does not. The argument was sound
and the conclusion was not: `SessionID` is in the same struct, and a
`SessionID` that plainly does not change is a session that plainly
cannot send. So the whole struct is replaced on a region change rather
than edited -- a re-established session announces itself as a region
change -- and `Info` hands back the current one.
