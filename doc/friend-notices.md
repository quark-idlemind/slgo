# Friends logging in and out

Nothing on this page has been measured on the grid. It is how the
viewer's source reads the two messages, and what slgo was made to do
about it.

## What the viewer does

The simulator sends `OnlineNotification` or `OfflineNotification`, each
a list of `AgentBlock{AgentID}`. At login it sends one naming every
friend already online.

`LLAvatarTracker::processNotify` (`indra/newview/llcallingcard.cpp:780`)
takes both. For each id it marks the friend online or offline when the
id is a friend (`:809`); an id that is not one is logged as a warning
and queued, and is not dropped (`:813-816`). Then, whether or not the
id was a friend, it announces it when `ChatOnlineNotification` is set,
or the friend's contact set asks for it, or `OnlineOfflinetoNearbyChat`
is set (`:829`), by looking the name up and calling
`on_avatar_name_cache_notify` (`:847`). Nothing there distinguishes the
login burst from a later notification, and the repository owner has
watched Firestorm announce the burst, one line per friend.

The words are the notification `FriendOnlineOffline`, "[NAME] is
[STATUS]." (`skins/default/xui/en/notifications.xml:7630-7634`), with
the status "online" or "offline" (`strings.xml:2213-2214`). When
`OnlineOfflinetoNearbyChat` is set the viewer also puts a line in
nearby chat as a radar message, from a system source, under the
friend's name, reading "is online." or "is offline."
(`llcallingcard.cpp:892-902`, `strings.xml:3272-3273`).

## What slgo does

The agent already keeps who is online (`agent/friends.go`). `sl` now
also turns each message into `sl.FriendChange` events, one per id in the
message's order, for a subscriber to `Session.FriendChanges`
(`sl/friendnote.go`). Both messages were already in `sl.Subscriptions`,
so a shell attached to slgod and one logged in directly (`sl.Direct`)
hear the same. The events are not checked against the friend list, as
the viewer does not check either.

slsh prints each as a notice beside the others, `15:07:40 * Example
Resident is online`, naming the friend as a person (`Sender.Label`).
A name the session does not have is asked for and waited for two
seconds, past which the id is printed.  The waiting is done apart from
the loop that prints chat, so it holds nothing else up, and what arrived
together -- a login burst -- is named together with the one wait. This is slsh's way of following the viewer's setting: it is
always on, and there is no `ChatOnlineNotification` switch.

A shell attaching to a daemon that has been up a while was not listening
for the burst, and announces only what arrives after; the burst is not
replayed. `friends` lists who is online now. `cmd/slbotd` does not
subscribe to `FriendChanges`, so it hears nothing new and says nothing
in-world about friends.

## Not measured

That the simulator sends the burst in the form above, and that a
notification for an id that is not a friend arrives at all, are read
from the viewer and have not been seen here.
