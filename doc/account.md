# The avatar's own account details

An avatar's email address, its directory visibility and the retired
IM-to-email flag are the account's own and private. slgo asks only when
a caller asks, nothing logs them, and they are printed only by slsh's
`account` command. Every address in a test, a man page or this file is
on `example.invalid`.

Paths are under Firestorm's `indra/`, which is the reference.  Line
numbers are at Firestorm 4ae31a7ad6, with d1f415d442's in brackets
where they differ.

## How the viewer asks

`LLAgent::sendAgentUserInfoRequest` (`newview/llagent.cpp:5784-5803`)
uses the region's `UserInfo` capability (a GET) when the region offers
it, and sends the UDP `UserInfoRequest` only when it does not. The
viewer asks for the capability at login (`llviewerregion.cpp:3586`
[3581], `capabilityNames`).

`requestAgentUserInfoCoro` (`llagent.cpp:5805-5850`) reads the LLSD:
`success` (false is a failure, with `message`), `email`,
`directory_visibility`, and `im_via_email` only outside Second Life,
kept "for OpenSim".

The UDP reply, `UserInfoReply` (Low 400; `UserData`: `IMViaEMail`,
`DirectoryVisibility`, `EMail`), is handled by `process_user_info_reply`
(`llviewermessage.cpp:8267` [8235-8262]), registered at
`llstartup.cpp:3937` [3929].
It reads `IMViaEMail` only outside Second Life as well.

## What slgo does

`agent.DefaultCaps` asks for `UserInfo` at login, as the viewer does.
`sl.Session.UserInfo` takes the capability when it is offered and
otherwise sends `UserInfoRequest` and waits for the `UserInfoReply`
carrying this agent's id (one for another agent is ignored). It gives up
with the caller's context or after `sl.DefaultUserInfoTimeout`, 5 s, and
a timeout wraps `sl.ErrTimeout`. There is no field in `sl.Options` for
it.

`UserInfo.IMViaEmail` is read but never shown: Second Life retired
IM-to-email, and the viewer ignores the flag there.

## Measured

On 2026-10-03, through slgod, with the owner's avatar, on the main grid.

- Before slgo asked for it at login, the `UserInfo` capability was not
  offered.
- The UDP `UserInfoRequest` was answered by `UserInfoReply` in 190 ms,
  with the agent's id, an email address, a directory visibility and the
  IM-to-email flag all present.
- With a slgod that asks for it at login, the region offered the
  capability.  A GET was answered 200 in 345 ms and again in 732 ms,
  with an LLSD map holding `success`, `email`, `directory_visibility`,
  `im_via_email` and `is_verified`.  The UDP request was still answered,
  in 182 ms.  `is_verified` is not read; nothing in the viewer reads it
  either.

### Not measured

A refusal (`success` false) and the `message` that would come with it.

## Nothing logs it

- `sl.UserInfo`'s errors say what failed. A refusal carries the grid's
  `message`; no error carries the response body or a field of it.
- Neither the agent nor `sl` logs a message's contents, and a request to
  a capability is not logged. The agent's log lines are about circuits,
  drops and alerts, and slgod's census counts messages by name.
- No dump writes a `UserInfoReply`'s blocks: `msg.Private` names it,
  and `msg.DumpMessage` and the packet dump write its name and id and
  `blocks: withheld` in their place.  So `slgod -trace` with bodies
  writes it as a one-line entry, and `slsh watch`, even of every
  message, shows that a reply came and not what it held.  Only
  `slsh account` prints the details.
- slgod relays `UserInfoReply` only to a session waiting for one: it is
  not among `sl.Subscriptions`, and a call over the circuit watches it
  for its own wait and gives it back, as a sit borrows
  `AvatarAnimation`.  Another client of the same avatar is not sent the
  address because somebody else asked.
- `slbotd` never asks, and a test refuses a call of `UserInfo` or a use
  of `UserInfoRequest` in it: it answers IMs with a local model, so what
  it can read can be talked out of it.
