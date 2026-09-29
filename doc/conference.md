# Conferences

A conference is an instant message with more than two people in it,
which the viewer calls a "Multi-person chat" and the grid calls an
ad-hoc session.  `Session.StartConference`, `ConferenceFromIM`,
`AddToConference`, `SayToConference`, `LeaveConference` and
`AcceptConference` (`sl/conference.go`) take part in one, `Session.Conferences`
lists them, and what is heard arrives on `Session.GroupChats` beside a
group's chat; `conference` is the shell's command (`cmd/slsh/conference.go`).

What the viewer does is read from its source (Firestorm, `indra/`), and
nothing on this page has been measured on a grid unless
[Measured](#measured) says so.  A sentence that says something was inferred
means it.  A conference shares its machinery with a group's chat, which
[group-chat.md](group-chat.md) describes: the same capability, the same
event queue, the same session dialogs.

Paths below are under `indra/newview/` unless they say otherwise.

## What the viewer does

### Starting one

`LLAvatarActions::startConference` (`llavataractions.cpp:421-462`) is
what every route to a conference ends in.  It takes the people, makes a
session called "Multi-person chat" (`strings.xml:2242-2244`, the string
`conference-title`) of type `IM_SESSION_CONFERENCE_START` (16), and passes
the first person as the session's "other participant"
(`llavataractions.cpp:443`).  It does not check how many people it is given.  Its callers are menus
on a selection of avatars (`llfloaterimcontainer.cpp:1064, 1357`,
`llpanelpeople.cpp:1538, 2140`, `fsfloatercontacts.cpp:352` and others)
and adding to an instant message, below, which gives it two or more.
Whether a menu can reach it with one avatar was not looked at; slgo
requires two other people, so that a conference is never what an instant
message is.

1. **The session id is invented.**  `computeSessionID` generates a random
   id for a conference start (`llimview.cpp:2540-2543`).  This is the
   *temporary* id: the grid's reply carries the real one.
2. **An ad-hoc session with the same people is reused.**  Before making
   the session, `addSession` looks for an ad-hoc session of the same type
   whose initial people are the same set, and if it finds one uses it and
   sends nothing (`llimview.cpp:3994-4020`, `findAdHocIMSession`,
   `1547-1580`).  A session is found only while the viewer still holds
   it, that is until it is left.
3. **The start is a POST, not a circuit message.**  The session's
   constructor calls `sendStartSession` (`llimview.cpp:937`), whose
   conference branch (`llimview.cpp:2481-2510`) launches
   `startConferenceCoro` (`llimview.cpp:576-620`) which posts to the
   current region's `ChatSessionRequest` capability:

       {"method": "start conference",
        "session-id": <the temporary id>,
        "params": [<agent id>, ...],
        "alt_params": {"voice_server_type": "vivox" or "webrtc"}}

   `params` is the people, in the order given, the first of them being
   the other participant.  `voice_server_type` is the viewer's
   `VoiceServerType` setting, or when that is empty (its default,
   `app_settings/settings.xml:18353-18362`) the type of the voice module
   in use (`llimview.cpp:588-595`), which follows the region's
   `SimulatorFeatures` `VoiceServerType` and is "vivox" when the region
   says none (`llvoiceclient.cpp:194-198`; the values are
   `llvoicevivox.cpp:83` and `llvoicewebrtc.cpp:83`).
4. **The old way is a fallback.**  When the POST fails with 400 the viewer
   sends `start_deprecated_conference_chat` (`llimview.cpp:2418-2456`,
   `611-616`); it does so as well when the avatar has no region.  That is
   one `ImprovedInstantMessage`, reliably, built as the group start is
   (`session_starter_helper`, `llimview.cpp:2388-2416`: `ToAgentID` the
   other participant, `Dialog` 16, `ID` the temporary id, this avatar's
   name, an empty message, its position) with the people as the bucket:
   their 16-byte ids one after another, in order.  Any other failure is
   not answered at all (`llimview.cpp:602-606`, 613-620).
5. **It waits for the answer up to 30 seconds** (`SESSION_INITIALIZATION_TIMEOUT`,
   `llimview.cpp:113`, `945-946`), and text typed meanwhile is queued
   and sent when the session is initialised (`fsfloaterim.cpp:620-630`).
6. **The answer arrives on the event queue**: `ChatterBoxSessionStartReply`
   (`llimview.cpp:4852-4928`), the same event a group's start is answered
   with, with `temp_session_id` the id the start was made with and
   `session_id` the session.  For a group the two are the same; for a
   conference they differ, and the viewer moves its session from the one to
   the other (`processSessionInitializedReply`, `llimview.cpp:1705-1738`).
   Success hands the body to the speaker list (`agent_info` or `agents`)
   and the pending agent-list updates are applied
   (`llimview.cpp:4889-4894`).  Failure shows
   `ChatterBoxSessionStartError` with the reason (`llimview.cpp:4917-4921`).

### Adding people to a conference

`FSFloaterIM::onAddButtonClicked` (`fsfloaterim.cpp:2411-2430`) shows an
avatar picker, or people are dropped on the window (`dropPerson`,
`fsfloaterim.cpp:2017-2033`); both end in `addSessionParticipants`
(`fsfloaterim.cpp:2457-2477`).  People who are already in the conversation
are refused by the picker (`canAddSelectedToChat`, `fsfloaterim.cpp:2431-2455`),
as is any group session (`fsfloaterim.cpp:2433-2436`).

- **To a conference this avatar started or was invited to**,
  `inviteToSession` (`fsfloaterim.cpp:2105-2140`; Linden's viewer has the
  same in `llfloaterimsession.cpp:1245-1270`) posts to `ChatSessionRequest`:

      {"method": "invite",
       "session-id": <the session>,
       "params": [<agent id>, ...]}

  The viewer allows it only where `isInviteAllowed` says so: a conference
  it started, or an invited session that is not a group's
  (`fsfloaterim.cpp:2098-2103`).  Nothing reads the answer: Firestorm's
  post discards it (`callbackHttpPost`), and Linden's logs "Session invite
  failed" (`messageHttpPost`).  The people invited are remembered, and when
  the grid's agent-list update says they have entered, the viewer says in
  the *session*, as an ordinary message from this avatar, "[NAME] was
  invited to the conversation." (`fsfloaterim.cpp:1795-1868`,
  `floater_fs_im_session.xml:22-27`).  slgo does not send that line.
- **To a one-to-one instant message**, the conversation becomes a new
  conference; the message window is not turned into one in place
  (`addP2PSessionParticipants`, `fsfloaterim.cpp:2478-2525`, after a
  "ConfirmAddingChatParticipants" question).  The viewer:
  1. closes the instant message's window, which is `leaveSession` and
     so sends the leave below to the other person for the message's own
     session (`fsfloaterim.cpp:233-255`, `llimview.cpp:4078-4110`); this
     is true of closing any instant message window, and an instant message
     session is a "P2P session" whatever it is called (`llimview.cpp:915-925`);
  2. starts a conference, as above, with the other person first and the
     people added after them (`fsfloaterim.cpp:2493-2521`), naming the
     old session's id as the window to reuse (`llimview.cpp:3973-3990`).
  The old session's id is the two agent ids exclusive-or'd, as an
  instant message's is (`llimview.cpp:2548-2562`).

### How an invitation reaches an avatar that is not in it

The grid sends `ChatterBoxInvitation` on the event queue.  The viewer's
handler (`LLViewerChatterBoxInvitation`, `llimview.cpp:5036-5225`) has three
shapes, of which two matter here (the third, `voice`, is a call):

- **`instantmessage`**: what a group's invitation has
  (group-chat.md, "Joining a group's chat unasked"), and for a session
  that is not a group's it is handled by the same code: the session is
  made at once as `IM_SESSION_INVITE` named from the bucket, the first
  line is added to it, and unless the speaker is muted or Do Not Disturb
  applies the viewer posts the acceptance, without asking
  (`llimview.cpp:5048-5194`).  That the grid sends this shape for a
  conference, as it does for a group, is not in the viewer's source.
  Firestorm can be set to leave every such session at once instead
  (`FSIgnoreAdHocSessions`, `llimview.cpp:3680-3700`).
- **`immediate`**, with `session_id`, `session_name`, `from_id` and
  `from_name` at the top of the body (`llimview.cpp:5215-5224`): this is
  the invitation to a conference that is asked about.  `inviteToSession`
  (`llimview.cpp:4126-4300`) ignores one from this avatar and one from a
  muted avatar, and shows the "InviteAdHoc" dialog: "[NAME] is inviting
  you to a conference chat" with Accept and Decline
  (`notifications.xml:9546-9549`).  (When the session is one of this
  avatar's groups it is a voice invitation to the group's call, and is
  not a conference: `llimview.cpp:4157-4163`.)

Accepting (`processCallResponse`, `llimview.cpp:3296-3400`) makes the
session, `addSession(name, IM_SESSION_INVITE, session_id)`, whose other
participant is the *session id* (`llimview.cpp:3938-3949`), and posts

    {"method": "accept invitation", "session-id": <the session>}

to `ChatSessionRequest` (`chatterBoxInvitationCoro`, `llimview.cpp:666-730`),
which is the post a group's invitation is answered with.  The answer is a
map handed to the speaker list.  A 404 is shown as
"session_does_not_exist_error" (`llimview.cpp:696-700`).  Declining posts
`{"method": "decline invitation", "session-id": <the session>}`
(`llimview.cpp:3432-3441`); nothing is sent on the circuit.

An invitation's name is the session's name as the grid gave it, the bucket
for the first shape and `session_name` for the second.  When it is empty the
viewer calls the session "<caller> Conference" (`llimview.cpp:3351-3366`)
and then "Conference with <caller>" (`llimview.cpp:1037-1062`,
`strings.xml:2245-2247`).

### What is heard and said

What a conference says arrives as dialog 17 on the circuit, the session
being the message's `ID`, exactly as a group's does (group-chat.md, "What
is heard").  Speaking is `LLIMModel::sendMessage`, which is given the
session, its *other participant* and its dialog
(`fsfloaterim.cpp:623`, 789-790), and sends each piece, split at 1023
bytes as group-chat.md says, as dialog 17 with

- `ToAgentID` the session's other participant: the first person invited,
  for a conference this avatar started; the session id, for one it was
  invited to (`llimview.cpp:3938-3949`, `fsfloaterim.cpp:875-880`);
- `ID` the session (the id the grid replied with, not the temporary one);
- `Offline` `IM_OFFLINE` when the other participant is a friend who is
  offline, `IM_ONLINE` otherwise (`llimview.cpp:2195-2197`).  slgo always
  sends online, as `SendIM` does;
- everything else as a group's message: no region, no position, one-byte
  bucket.

The viewer does not echo it into its own window, as for a group
(`llimview.cpp:2317-2337`).

### Leaving

`leaveSession` (`llimview.cpp:4078-4110`) sends `sendLeaveSession`
(`llimview.cpp:2161-2181`): one `ImprovedInstantMessage`, reliably,
`ToAgentID` the session's other participant (as above), `Dialog` 18, `ID`
the session, empty message, defaults for the rest.  Nothing answers it.  The
snooze branch is a group's only (`llimview.cpp:4083`).

### Who is in it

`ChatterBoxSessionAgentListUpdates` (group-chat.md, "The events") tells
the viewer who entered and left a conference as it does for a group; the
agent list of the start reply and of an acceptance is the base.  An update
that arrives before the session is known is kept
(`llimview.cpp:4399-4404`).

### Can anybody be removed

**No.**  The viewer has no way to remove a participant from a
conference, and slgo builds none.  The evidence:

- Every `ChatSessionRequest` post in the viewer names a method, and the
  methods are `start conference`, `start p2p voice`, `accept invitation`,
  `decline invitation`, `decline p2p voice`, `fetch history`, `invite`,
  `call` (voice), `mute update` and `session update`
  (`llimview.cpp:585, 634, 674, 792, 3422, 3437`; `fsfloaterim.cpp:2126`;
  `llfloaterimsession.cpp:1263`; `llvoicechannel.cpp:631`;
  `llspeakers.cpp:849, 877, 957`; `llinspectavatar.cpp:652`).  A search of
  `indra/newview` for every use of the capability name finds no other
  (the list above is all of them).  None removes anybody: `mute update`
  turns a person's text or voice off, and `session update` changes the
  session's moderated mode for voice.
- The moderator actions on a participant are the "Moderator Options"
  submenu of the participant list (`fsparticipantlist.cpp:515-570`;
  Linden's `llparticipantlist.cpp` is compiled out with `#if 0`).  It is
  shown only where `isGroupModerator` holds, and that requires the session
  to be one of this avatar's groups and the avatar to be a moderator of
  it (`fsparticipantlist.cpp:557`, `683-700`), or where the avatar
  moderates Nearby Chat.  A conference is neither.  What is in it: mute
  a person's text (`allowTextChat`, `llspeakers.cpp:838-861`, the `mute
  update` above), mute their voice, and "Eject from Group"
  (`menu_participant_list.xml:156-165`).
- "Eject from Group", and "Ban from the group" (`fsparticipantlist.cpp:564`,
  shown only for `CONV_SESSION_GROUP` and `767-783`), take a person out of
  the *group* -- `sendGroupMemberEjects`, an `EjectGroupMemberRequest` message on
  the circuit, and the group ban list (`llgroupactions.cpp:858-892`,
  `llgroupmgr.cpp:1926-1990`) -- and are gated on powers held in the group
  whose id is the session's (`llgroupactions.cpp:818-857`,
  `fsparticipantlist.cpp:702-713`).  A conference's session id is no
  group, so neither does anything for one.  Neither is a message that
  removes somebody from a chat.
- A search of the conversation code for a kick or eject of a participant
  (`fsfloaterim.cpp`, `llimview.cpp`, `llspeakers.cpp`, `fsparticipantlist.cpp`,
  `llfloaterimcontainer.cpp`, `llfloaterimsession.cpp`) finds those two
  and nothing else; the one other mention of removal,
  `remove_participant` (`llfloaterimcontainer.cpp:663`), removes a row from
  a list on the screen when the grid says somebody left.

What the grid would do with a message the viewer never sends is not
known, and none is invented.  The way to be out of a conference is to
leave it.

## What slgo does

`sl/conference.go` does what is above and reuses `sl/groupchat.go` for
everything the two share: the session state, the events, the acceptance,
the splitting of long text, the ChatterBox handlers.

### Start

`StartConference(people...)` needs at least two people other than this
avatar and no duplicate, in the order given.  It reuses a conference this
session started with the same set, as the viewer does, and returns its id.
Otherwise it makes a temporary id, posts the start above, and waits up to
`Options.GroupChatTimeout` for `ChatterBoxSessionStartReply` naming the
temporary id, then returns the session id in it.  `voice_server_type` is
the `SimulatorFeatures` value, "vivox" when the region gives none, as the
viewer's default gives.  On a 400 it sends the deprecated dialog 16 and
waits the same way.  A failure reply is a `*GroupChatError` with
`Conference` set.

`ConferenceFromIM(with, add...)` is what adding to an instant message
does: the leave of the one-to-one session, then `StartConference` with
`with` first.  The leave is sent first, as the viewer does, and if the start
then fails the instant message is left all the same.

The conference is named "Multi-person chat".  One invited into is named
by the invitation, or "Conference with NAME" when it has none.

### Add, say, leave, accept

`AddToConference(session, people...)` posts the invite above, refuses a
session this session does not know as a conference (`ErrNotInConference`),
and leaves out anybody already in it or already invited.  `SayToConference`
and `LeaveConference` send what is above, to the session's other
participant.  `AcceptConference` posts the acceptance; a session that
is not a known invitation is refused.  Declining is not built: the
viewer posts `decline invitation`, and `conference leave` refuses an
invitation by sending the leave instead, which the grid may or may not
take as a refusal.  slgo does not send "was invited to the
conversation."

### Hearing

Conference events are the group chat events with `Conference` set: the
session is in `Group`, and the name is `GroupName`.  A dialog 17 message or
an invitation is a conference's when the session is one this session knows
to be one, or when the avatar's group list is not empty and does not
have it.  With an empty list -- which is "not told yet" as well as "none"
-- a dialog 17 and an `instantmessage` invitation are taken to be a
group's, as before, and an `immediate` invitation to be a conference's,
since that shape is only a conference's.  A conference is labelled
`[Conference] NAME` by `SenderConference` and never bare: the name is the
grid's, and whatever the inviter chose.

### The invitation

**It is not answered.**  The viewer answers an `instantmessage` shape at
once and asks about an `immediate` one; slgo answers neither
(`GroupChat.Accept`, `AcceptConference`), on the owner's instruction for group
chat and by the same reasoning here.

### The shell

`conference` (`cmd/slsh/conference.go`), which is documented in its man
page.  It is not fitted to `im`'s shape entirely: a conference has several
people and a name that says nothing (every one this avatar starts is
"Multi-person chat"), so people are separated by commas, each read as `im`
reads a person, and a conference is picked by the number in the listing
or its key.  Lines are printed `< SPEAKER in [Conference] NAME #N: TEXT`.

## Not known

- Which invitation shape the grid uses for a conference, and whether
  both.
- What the start reply and the acceptance list (a group's start reply was
  measured listing nobody).
- Whether the temporary id and the real one differ, as the viewer's code
  handles either.
- Whether the grid answers an `invite` with anything, and whether it
  takes one from an avatar that was itself invited.
- What the grid does with a leave for a one-to-one session, though every
  viewer sends one.
- Whether `alt_params` matters to a text conference, and what becomes of a
  start without it.
- Whether an avatar with no groups is told the invitation is a conference's:
  see "Hearing".
- Whether a leave stops the invitations and the messages to a conference,
  as it did for a group's chat, and whether a leave refuses an invitation.
- Whether the grid sends a speaker's own words back to them, as for a
  group's chat.
- Whether a conference has moderators of any kind.

## The first live run

Three avatars, A, B and C, each attached to its own daemon, and B and C
logged in and named to A (B and C should be people A can name: friends,
or in the region).  Each of these is one line and what it should show:

1. A: `conference start B, C`.  Expect "started [Conference] Multi-person
   chat #1" and both names.  B and C, if watching, get an invitation, in
   one of two shapes, and neither is joined for them.  Note which of the two
   shapes was seen, and whether the start reply listed anybody.
2. B: `conference` lists the invitation as `invited`; `conference join 1`
   joins it and lists who is in.  Note whether A is told B came in.
3. A: `conference say 1 hello`; B should print `< A in [Conference] ...`.
   Note whether A is sent its own line back.
4. B: `conference say 1 hi back`; A prints it.  C, not joined, is told once.
5. A: a fourth person D: `conference add 1 D`.  Note what D is sent, and
   whether the invite is answered with anything.
6. C: `conference leave 1`, then A says something.  Note whether C is sent
   anything after leaving.
7. From an open `im` between A and B: `conference from-im B, C`.  Note
   whether B and C are invited as in 1.
8. Watch whether a conference start with the same people again, after A
   leaves it, makes a new one.

What has to be written down, not guessed: the invitation shape, the
start reply's contents, whether the temporary id and the session id
differed, and every answer to a question under "Not known".

## Measured

On Agni on 2026-09-29, two runs with three of this repository's avatars:
Quark Idlemind and Perrick Hobb through the everyday slgod, and Mirkwin
Resident on a second slgod, each with a slsh built from this branch.
Quark Idlemind started a conference with the other two.

- **Starting sends the others nothing.** The start was answered at once,
  and its reply listed both invited avatars, so the starter was told
  each "came into" the conference before either had heard of it. Neither
  was sent an invitation when it started.
- **The invitation comes with the first line.** When Quark Idlemind first
  spoke, each of the others was sent the line and an invitation together,
  in the `instantmessage` shape, as group chat's are. So `conference join`
  typed before anyone had spoken found no conference to join.
- **The two sides name it differently.** The starter's is "Multi-person
  chat", as the viewer names one it starts; the invited avatars were
  given the grid's name, "Quark Idlemind Conference".
- **Joining and speaking.** Mirkwin Resident joined after the first line,
  and each then heard the other within a second, marked as a conference.
- **Not joining still hears.** Perrick Hobb never joined, and was sent
  every line as it was spoken.
- **A participant nobody has named is shown by id.** On joining, Mirkwin
  Resident was told of Perrick Hobb by the id's first eight characters:
  the name was not known to that session.
- **Not seen.** A refusal, `conference add` to a running conference, a
  conference made from an IM, a start refused and retried the older
  way, and whether the grid sends a speaker's own words back.
