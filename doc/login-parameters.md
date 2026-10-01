# What a viewer sends to log in

Everything below was read out of the Firestorm source at
`~/src/phoenix-firestorm`, so it does not have to be read again. Line
numbers are from the checkout as of August 2026 and will drift; the
function names will not.

The request is built in one place:

    indra/newview/lllogininstance.cpp:155   LLLoginInstance::constructAuthParams

which fills an `LLSD` map and hands it to `LLLogin::connect`, through

    indra/viewer_components/login/lllogin.cpp:135   loginCoro
    indra/newview/llxmlrpclistener.cpp:210          param -> XML-RPC struct
    indra/newview/llxmlrpctransaction.cpp           the actual POST

The method is `login_to_simulator`, and the whole thing is one XML-RPC
struct.

## Every parameter

The "slgo" column is what `agent.Login.body` sends today.

| Parameter | Value in a viewer | slgo |
|---|---|---|
| `first`, `last`, `passwd` | `LLCredential::getLoginParams`, `llsecapi.cpp:127`. `passwd` is `"$1$" + md5hex(password)` | yes |
| `username`, `passwd` | the other credential shape, for "account"-type logins, where `passwd` is the secret unhashed | no |
| `start` | `last`, `home`, or `uri:Region&x&y&z` (`construct_start_string`, `lllogininstance.cpp:631`) | yes |
| `version` | viewer version, e.g. `7.1.11.12345` | yes, `slgo 0.1` |
| `channel` | viewer channel, e.g. `Firestorm-Releasex64` | yes, `slgo` |
| `platform` | `win`, `mac` or `lnx` (`llappviewer.cpp:353`) | yes, from the host |
| `platform_version` | dotted OS version, `LLOSInfo::getOSVersionString` | yes, from the host |
| `platform_string` | readable OS name, `LLOSInfo::getOSStringSimple` | yes, from the host |
| `address_size` | `32` or `64`, the build's pointer width | yes |
| `mac` | **md5 hex of the machine's unique id** -- not a MAC address | yes, hashed on the way out |
| `id0` | **md5 hex of the hardware serial number** | yes |
| `agree_to_tos` | `0` at first; `1` only after the user accepts the dialog | yes, always `true` |
| `read_critical` | `0` at first; `1` only after the user reads the notice | yes, always `true` |
| `extended_errors` | `1` -- asks for `message_id` and `message_args` on a refusal | yes |
| `last_exec_event` | how the previous run ended, `eLastExecEvent` (`llappviewer.h:69`) | no |
| `last_exec_duration` | seconds the previous run lasted, `-1` if unknown | no |
| `last_exec_session_id` | UUID of the previous session, from the marker file | no |
| `host_id` | `gSavedSettings("HostID")`, "Machine identifier for hosted Second Life instances", empty by default | no |
| `token` | `""`, filled with the MFA code on a retry | no |
| `mfa_hash` | `""`, or the hash remembered for this (grid, user) | no |
| `options` | array of extra response blocks to ask for | yes, `inventory-root` |

`last_exec_event` is an integer from this enum, in order:
`LAST_EXEC_NORMAL` (0), `FROZE`, `LLERROR_CRASH`, `OTHER_CRASH`,
`LOGOUT_FROZE`, `LOGOUT_CRASH`, `BAD_ALLOC`, `MISSING_FILES`,
`GRAPHICS_INIT`, `UNKNOWN`, `LOGOUT_UNKNOWN` (10).

## mac and id0 are both md5 digests

This is the part most easily got wrong, because of what the field is
called. A viewer never sends a MAC address.

`llhasheduniqueid.cpp:33` takes `LLMachineID::getUniqueID()` -- falling
back to `LLUUID::getNodeID()`, which is the network card -- md5s the six
bytes, and sends the **32 lowercase hex digit** digest as `mac`. When it
cannot get an id at all it sends the literal string of 32 zero digits
and logs "cannot uniquely identify this machine".

`id0` is `LLAppViewer::mSerialNumber`, from the platform's
`generateSerialNumber`. On macOS (`llappviewermacosx.cpp:411`) that is
the md5 of `IOPlatformSerialNumber`; on Windows
(`llappviewerwin32.cpp:1266`) the volume serial. Same shape: 32 hex
digits.

So a real viewer's pair is two md5 digests, and so is ours. `agent.Login`
hashes `MAC` on the way out (`hashMAC`), which puts the split in the
useful place: `~/.config/slgod/config` keeps the invented address
`7E:52:2B:3C:57:06`, which a person can read and check, and the wire
carries the md5 of its six bytes, 32 hex digits, which is what a viewer
would carry. A value that is already 32 hex digits passes straight through, so
a digest lifted out of a viewer's log can be used as it stands.

We sent the colon form until August 2026. Correcting it moved the
accounts to a new computer once, as any change to the pair does -- which
is why it is worth getting right early and then never touching. See
[the machine identity](../cmd/slgod/machine.go).

## Types

`llxmlrpclistener.cpp:221` decides the XML-RPC type:

- `TypeString` -> `<string>`
- `TypeInteger`, `TypeReal` -> `<int>`, `<double>`
- `TypeBoolean` -> **`<int>` 0 or 1**, not `<string>true</string>`
- anything else is a fatal error

So a viewer sends `agree_to_tos` as `<int>0</int>`. We send
`<string>true</string>` for `agree_to_tos` and `read_critical`, which
the server accepts; the numeric fields we do send (`address_size`,
`extended_errors`) are `<int>`, matching the viewer.

## The options array

`options` is inserted as an ordinary parameter whose value is an array
of strings (`llxmlrpclistener.cpp:237`). Each one asks the login server
for another block in the response. **The server answers only what it is
asked**, and a block that was not asked for comes back missing rather
than empty, which reads exactly like a real "you have none".

Firestorm asks for, in order (`lllogininstance.cpp:161`):

    inventory-root          inventory-skeleton      inventory-lib-root
    inventory-lib-owner     inventory-skel-lib      initial-outfit
    gestures                display_names           event_categories
    event_notifications     classified_categories   adult_compliant
    buddy-list              newuser-config          ui-config
    advanced-mode           max-agent-groups        map-server-url
    voice-config            tutorial_setting        login-flags
    global-textures

plus `god-connect` when `ConnectAsGod` is set, and on OpenSim grids
`currency`, `max_groups`, `search`, `destination_guide_url`,
`avatar_picker_url`.

Commented out in the source, so evidently still understood by the
server: `inventory-meat`, `inventory-skel-targets`, `inventory-meat-lib`,
`profile-server-url`.

Note the comment at line 167: not requesting the library options trips
`mFatalNoLibraryRootFolder` -- that is a viewer-side assertion, not a
server requirement.

Group membership is **not** available here at all, asked for or not. It
arrives later, on the event queue.

## The response to a refusal

`login` comes back `false` with a `reason`, and the reasons the viewer
branches on (`lllogininstance.cpp:318` and `llstartup.cpp:1805`) are:

- `key` -- wrong username or password
- `presence` -- a previous session has not finished ending
- `connect` -- could not reach the simulator to put the agent on it
- `tos` -- must accept the terms; retry with `agree_to_tos = 1`
- `critical` -- must read a notice; retry with `read_critical = 1`
- `update`, `optional` -- client too old; `message_args["VERSION"]` names
  the one to get, and `optional` means it would be allowed in anyway
- `mfa_challenge` -- retry with `token` set (see below); here
  `message_id` is the prompt to show rather than an error

`CURLError` and `BadType` also turn up as reasons, but those are the
viewer's own transport failures rather than the server's answer.

Because we send `extended_errors`, a refusal also carries:

- `message_id` -- an identifier such as `LoginFailedAccountSuspended` or
  `LoginFailedAccountMaintenance`
- `message_args` -- a struct of values for the blanks in that message,
  e.g. `TIME` for a suspension, `VERSION` for an out-of-date client

Both land in `agent.LoginError.MessageID` and `.MessageArgs`. The server
does not name every refusal, so both may be empty while `message` is
not; match on `message_id` when it is there and fall back to `reason`.

## MFA, if an account ever turns it on

Not implemented here, recorded so it does not have to be worked out
again. The server refuses with a challenge; the viewer prompts, then
retries the *same* request with `params["token"]` set to the code
(whitespace stripped -- SL-17034). On success the response carries an
`mfa_hash`, which the viewer stores per (grid, user) and sends as
`mfa_hash` on later logins to avoid being challenged again. A `tos`
failure arriving mid-challenge expires the token, and the viewer prompts
for a new one rather than reusing it.

## What we deliberately do not send

- `agree_to_tos` / `read_critical` as `0`. A headless client has nobody
  to show a dialog to, so it agrees up front. This is the one place we
  are knowingly not viewer-shaped, and it is a choice, not an oversight.
- `last_exec_*`. Crash telemetry about a viewer run that never happened.
  Cheap to add if the shape ever matters -- slgod knows how long the
  previous run lasted and what session it held.
- `host_id`. For Linden-hosted viewer instances; empty is correct.
- The rest of the `options` list. Each one is a block of the response we
  would then have to have a use for.
