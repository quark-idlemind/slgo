# Group chat

`Session.JoinGroupChat`, `SayToGroup` and `LeaveGroupChat`
(`sl/groupchat.go`) take part in a group's chat, `Session.GroupChats`
delivers what is heard, and `group chat`, `group say` and
`group leave-chat` are the shell's commands for them.  **Everything on
this page about what the viewer does, and what the grid answers, is read
from the viewer's source (Firestorm, `indra/`), not measured.**  Nothing
here was run against a grid.  Where a sentence says what the grid does
it says what the viewer expects of it; where it says something was
inferred, it was.  The first live run should replace this paragraph with
what it saw.

Paths below are under `indra/newview/` unless they say otherwise.

## What the viewer does

### Joining a group's chat, when the user asks

Opening a group's chat window is `LLGroupActions::startIM`
(`llgroupactions.cpp:639`).

1. The group must be one the avatar belongs to: the viewer looks it up
   in its own membership list (`gAgent.getGroupData`,
   `llgroupactions.cpp:652-653`) and, when it is not there, plays an error
   sound and does nothing (`llgroupactions.cpp:673-679`, "this should
   never happen").
2. It makes a session called by the group's name of type
   `IM_SESSION_GROUP_START` (`llgroupactions.cpp:659`).  The session's
   id is the group's id: `computeSessionID` slams a group session's id
   to `other_participant_id` (`llimview.cpp:2536-2540`).
3. Making the session sends the start message from the session's
   constructor, `sendStartSession` (`llimview.cpp:937`, `2460-2472`):
   one `ImprovedInstantMessage`, reliably, built by
   `session_starter_helper` (`llimview.cpp:2388-2421`):
   - `AgentData`: this avatar's id and the session id;
   - `FromGroup` false;
   - `ToAgentID` the group's id;
   - `Offline` `IM_ONLINE` (0);
   - `Dialog` `IM_SESSION_GROUP_START` (15);
   - `ID` the session's id, which is the group's;
   - `Timestamp` 0, `ParentEstateID` 0, `RegionID` null;
   - `FromAgentName` this avatar's full name, `Message` empty;
   - `Position` the avatar's position in the region;
   - `BinaryBucket` the one-byte empty bucket (`EMPTY_BINARY_BUCKET`
     is `""` with size 1, `llmessage/llinstantmessage.cpp:46-47`).
4. It then waits for the answer, up to `SESSION_INITIALIZATION_TIMEOUT`,
   30 seconds (`llimview.cpp:113`, `945-946`).  When the time is up and
   the session is still not initialised it shows
   "session_initialization_timed_out_error" (`llimview.cpp:143-153`).
5. The answer is not on the circuit but on the event queue:
   `ChatterBoxSessionStartReply` (below).  Success initialises the
   session, hands the body to the speaker list and, when the
   "FetchGroupChatHistory" setting is on, asks for recent history
   through `ChatSessionRequest` (`llimview.cpp:4887-4912`).  Failure
   shows `ChatterBoxSessionStartError` with the reason the grid gave
   (`llimview.cpp:4917-4921`).

There is no other message.  The viewer does not post to
`ChatSessionRequest` to start a group session; it does for a
conference (`llimview.cpp:2481-2500`) and for an invitation.

### Joining a group's chat unasked: an invitation

When somebody speaks in a group's chat and this avatar has no session
in it, the grid sends `ChatterBoxInvitation` on the event queue.  The
viewer's handler (`LLViewerChatterBoxInvitation`, `llimview.cpp:5036`)
does the following, for the body that has `instantmessage` (the other
two shapes, `voice` and `immediate`, are voice and conference calls):

1. Reads `message_params` out of it: `message`, `from_name`, `from_id`,
   `id` (the session, which for a group is the group), `data.binary_bucket`
   (the session's name, as text), `offline`, `timestamp`,
   `parent_estate_id`, `region_id`, `position` (`llimview.cpp:5048-5065`).
   The comment there says this is "replicated code from
   process_improved_im".
2. Drops it, without answering, when the avatar is set to Do Not
   Disturb (`llimview.cpp:5067-5074`).
3. Firestorm only: if "FSMuteAllGroups" is set, or
   "FSMuteGroupWhenNoticesDisabled" is set and the group's
   `AcceptNotices` flag is off, the viewer sends `IM_SESSION_LEAVE` for
   the session, leaves it locally, and returns; it does not accept
   (`llimview.cpp:5093-5127`).  See "A group whose chat is off".
4. Drops it, without answering, when the sender is this avatar
   (`llimview.cpp:5148-5158`; the OpenSim branch is another grid's).
5. Adds the message to a session it makes by the name in the bucket,
   as an `IM_SESSION_INVITE` (`llimview.cpp:5159-5172`).  So the first
   thing said arrives inside the invitation and not as a message of its
   own.
6. Returns if the speaker is muted (`llimview.cpp:5181`).
7. Otherwise accepts: a POST to `ChatSessionRequest`, in the current
   region's capability set, with `{"method": "accept invitation",
   "session-id": <session>}` (`llimview.cpp:5186-5194`, `666-680`).
   A 404 answer is shown as "session_does_not_exist_error"
   (`llimview.cpp:696-700`); on success the answer is a map handed to
   the speaker list (`llimview.cpp:702-730`).

So the viewer answers every invitation it does not drop, at once, and
is in the chat from the moment somebody first speaks.  Whether the grid
depends on the answer -- keeps a session waiting for it, or stops
sending messages for want of it -- is not in the viewer's source and
has not been measured.

There is one more way the viewer joins unasked, and it is over UDP: a
message with dialog 17 for a group the avatar is in but has no session
in is normally dropped, but Catznip's snooze patch and Firestorm's
group mute list can restore the session when their own conditions hold,
by `addSession` and then `sendStartSession` with the message above
(`llimprocessing.cpp:1851-1858`, `llimview.cpp:4327-4330`,
`exogroupmutelist.cpp:172-177`).  That is start, not accept.

### What is heard

Once in the session, what people say arrives as an ordinary
`ImprovedInstantMessage` on the circuit with dialog `IM_SESSION_SEND`
(17).  `process_improved_im` reads the session from the message's `ID`
(`llviewermessage.cpp:2524`), the speaker from `AgentData.AgentID`
(`llviewermessage.cpp:2517`) and the speaker's name from
`FromAgentName`.  `FromGroup` is not consulted for this dialog.  The
message is shown only when a session for that id exists
(`llimprocessing.cpp:1851-1858`): the viewer does not display group chat
it has not joined.

### Speaking

`LLIMModel::sendMessage` (`llimview.cpp:2263`) is called with the text,
the session id, the group's id as the other participant and dialog
`IM_SESSION_SEND`.  It:

1. Splits the text into pieces of at most `MAX_MSG_BUF_SIZE - 1` = 1023
   bytes, cutting at the last space before the limit, else at the limit
   backed off to a UTF-8 boundary (`llimview.cpp:2266-2311`,
   `llmessage/lldbstrings.h:77`).  That is a Firestorm patch
   (FIRE-787); `pack_instant_message_block` otherwise truncates at the
   packet size and logs a warning (`llmessage/llinstantmessage.cpp:129-147`).
2. Sends each piece (`deliverMessage`, `llimview.cpp:2185-2231`) as an
   `ImprovedInstantMessage` by `pack_instant_message` with:
   `FromGroup` false, `ToAgentID` the group, `Offline` `IM_ONLINE`
   (a group is not a friend, so the buddy lookup misses and the viewer
   sends online, `llimview.cpp:2195-2197`), `Dialog` `IM_SESSION_SEND`,
   `ID` the session, `ParentEstateID` 0, `RegionID` null, `Position`
   zero, `Timestamp` 0, the one-byte empty bucket
   (`llinstantmessage.h:185-199`).
3. Does not add the line to its own transcript for a group: the local
   echo is only for `IM_NOTHING_SPECIAL` (`llimview.cpp:2317-2337`).
   So the avatar's own words appear in the window only if the grid
   sends them back as dialog 17 like everybody else's, and nothing on
   the receiving path drops a message whose sender is this avatar
   (`llimprocessing.cpp:1851-1935`).  That the grid does so is inferred
   from that, and not measured.

A failure to send is not answered on the circuit: the grid answers
`ChatterBoxSessionEventReply` on the queue with `success` false, and
the viewer shows `ChatterBoxSessionEventError` with the event's own
text (`llimview.cpp:4931-4962`, `2596-2611`).

### Leaving

`LLIMMgr::leaveSession` (`llimview.cpp:4078`) calls
`LLIMModel::sendLeaveSession` (`llimview.cpp:2161-2181`) and forgets the
session.  The message is `ImprovedInstantMessage`, reliably, with
`ToAgentID` the session's other participant (a group session's is the
group), `Dialog` `IM_SESSION_LEAVE` (18), `ID` the session, `Offline`
`IM_ONLINE`, an empty message, and the defaults for everything else
(position zero, region null, one-byte bucket).  Nothing answers it.
Closing a group's chat window is `LLGroupActions::leaveIM`
(`llgroupactions.cpp:759`), which is this and nothing more.  Catznip's
"snooze" (`llimview.cpp:4083-4100`) is the exception, which forgets
the session locally and sends nothing, so that the grid goes on
delivering.

### A group whose chat is off

Linden's viewer has no per-group chat switch; a group's `AcceptNotices`
flag is about notices.  Firestorm has two ways to turn group chat off
and both are read here, though slgo takes neither:

- the settings above (`FSMuteAllGroups`, `FSMuteGroupWhenNoticesDisabled`
  with `AcceptNotices` false): on an invitation the viewer sends
  `IM_SESSION_LEAVE` and does not accept (`llimview.cpp:5093-5129`);
- a per-group mute list (`exoGroupMuteList`): a first message for a
  muted group makes no window, and the viewer sends
  `IM_SESSION_LEAVE` (`llimview.cpp:3598-3611`).  Reading that handler,
  the accept at `llimview.cpp:5187` is still posted afterwards, since
  `addMessage` returns nothing to stop it.  What the grid does with a
  leave followed by an accept has not been looked at.

Both leave rather than only hide, and that is the point of the
comments beside them: the grid goes on sending to a member of the session
until it is told the member has gone.

## The events

All five arrive on the event queue, and the viewer registers each under
`/message/<name>` (`llimview.cpp:5228-5250`); the body is what its
handler reads as `input["body"]`.  Only the keys the viewer reads are
listed.

`ChatterBoxSessionStartReply` (`llimview.cpp:4852-4928`):

| key | |
| --- | --- |
| `success` | boolean |
| `temp_session_id` | the id the start was made with; for a group the group |
| `session_id` | the session, on success |
| `agent_info` | map of agent id to `{is_moderator, mutes: {text}}` |
| `agents` | the older form: array of agent ids |
| `session_info` | present when the session has details |
| `error` | on failure, the name of a string (below) |

(The handler's own `describe` says `reason` where the code reads
`error`; the code is what runs.)

`ChatterBoxSessionEventReply` (`llimview.cpp:4931-4962`): `success`,
`session_id`, and on failure `event` and `error`, both names of strings.

`ChatterBoxSessionAgentListUpdates` (`llimview.cpp:4982-4992`,
`4351-4405`, `llspeakers.cpp:739-830`): `session_id`, and either
`agent_updates`, a map of agent id to `{transition: "ENTER"|"LEAVE",
info: {is_moderator, mutes: {text}}}`, or the older `updates`, a map of
agent id to the transition alone.  An update that arrives before the
session is known is kept and applied when it is
(`llimview.cpp:4399-4404`).

`ForceCloseChatterBoxSession` (`llimview.cpp:4965-4980`): `session_id`
and `reason`, a string name; the viewer shows a dialog and closes the
window.

`ChatterBoxInvitation`: above.

The reasons are the names of strings in
`skins/default/xui/en/strings.xml:2283-2360`.  Those a group can meet:
`generic` ("Please close and reopen the conversation, or relog and try
again."), `insufficient_perms_error` ("You do not have sufficient
permissions."), `session_does_not_exist_error` ("The session no longer
exists"), `no_ability` and `no_ability_error` ("You do not have that
ability."), `not_a_mod_error`, `muted` and `muted_error` ("A group
moderator disabled your text chat."), `removed` and `removed_from_group`
("You have been removed from the group."), `close_on_no_ability` ("You
no longer have the ability to be in the chat session."), and, for an
event, `message` ("The message ... is still being processed. If the
message does not appear in the next few minutes, it may have been
dropped by the server.").

## What slgo does

The rest of this page is what `sl/groupchat.go` and `cmd/slsh` do with
the above, and where they depart from it.
