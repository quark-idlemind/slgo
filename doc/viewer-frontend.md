# slgod as a viewer frontend

Written 2026-08-14, against commit `3ebe930`.

slgod holds a live session. Firestorm is then pointed at slgod, speaks the
ordinary login protocol, and is handed *that session's* agent id, session
id and circuit code with slgod's own address in place of the simulator's.
From then on slgod is a simulator to the viewer and a client to the
simulator, and relays between them.

The point is not a proxy. It is that the avatar never logs in twice, so
the session `automate` is driving and the session the viewer is showing
are the same session.

## Effort

18-22 working days of focused work; 3-4 calendar weeks. The prior 2-3
week estimate is optimistic but not wrong about the shape -- it is wrong
about *where* the time goes. The ack translation everyone fears is
largely avoidable; the time actually goes into stages 3-5, the
say-it-once data.

## What already exists, and is worth knowing before reading further

The relay was anticipated, and some of it is built.

- **`msg.Raw`** (`msg/raw.go:11`) carries a message as its number and its
  undecoded body, with a doc comment that says it exists "so that
  something can move a message it does not understand". Its `Encode`
  hands back exactly what it was given.
- **`msg.KeepBody()`** (`msg/receive.go:99`) makes every packet carry its
  raw body, "so a relay can pass on a message without understanding it".
  `server.StartAgent` already sets it (`server/server.go:225`).
- **`agent.Options.Recv`** is documented as the thing "a relay wants".
- **`doc/messages.txt`** classifies all 483 messages by direction:
  223 `out`, 130 `in`, 31 `both`, 99 `int`, 24 retired.
- **`Account.Raw`** keeps the entire login response, so nothing the login
  server said is lost.
- **`Objects`** already shares one store per region across agents
  (`agent/regions.go`), already trims to the union of everyone's draw
  distance, and already re-requests cache misses with
  `RequestMultipleObjects` in batches of 100 (`agent/objects.go:682`).

So the work is less "build a relay" than "finish the one that was
designed for".

## Two ack domains, and why they never meet

The viewer numbers its packets from 1 and wants reliable delivery. The
circuit to the simulator has its own numbering already in flight. The
instinct is to renumber and map acks back.

Do not. Terminate reliability on both sides instead:

- A viewer-facing `msg.Sender`/`msg.Receiver`/`msg.Dispatcher` triple, on
  a socket of slgod's own. `WithSender` (`msg/dispatch.go:92`) makes the
  dispatcher acknowledge the viewer's reliable packets and confirm the
  viewer's acks against slgod's own retransmit queue. That is the whole
  of the viewer side, and it is code that already runs against the grid.
- A message going the other way is *re-sent*, not forwarded: slgod hands
  it to the other side's Sender, which assigns its own sequence number
  and, if the original was reliable, retransmits it on its own schedule.
- `PacketAck` is never relayed in either direction; nor are the acks
  appended to a packet's tail, which the receiver has already stripped
  (`msg/framing.go:65`).

Nothing else carries a circuit sequence number where it can be seen.
`StartPingCheck.OldestUnacked` is the one exception, and pings are
absorbed anyway -- slgod answers the simulator's (`agent/agent.go:397`)
and sends its own to the viewer.

The cost is honest and worth stating: slgod acknowledges a viewer packet
before the simulator has confirmed it. A reliable message abandoned after
five tries (`msg/send.go:386`) is lost silently as far as the viewer is
concerned. `SendStats.Abandoned` counts it, and the trace of stage 1 is
where it shows up.

**One real defect in the relay path.** `WithTap` runs "before routing and
before duplicate suppression" (`msg/dispatch.go:121`), and `Hosted.relay`
uses it (`server/server.go:226`). A retransmission from the simulator
would therefore be forwarded to the viewer as two distinct packets with
two distinct sequence numbers, and the viewer has no way to tell. gRPC
clients tolerate that; a viewer showing a chat line twice does not. Add a
`WithRelay(Handler)` option to `Dispatcher.one` that fires after the
duplicate check at `msg/dispatch.go:281` and before handler routing.

## Zerocoding, and the size of a forwarded packet

`Sender.transmit` (`msg/send.go:288`) never sets `FlagZerocoded`. Against
the grid that is fine. Toward the viewer it is not always: the receiver
expands a zerocoded body before storing it (`msg/receive.go:250`), so a
1200 byte `ObjectUpdate` can come out at several kilobytes, and
re-emitting it uncoded makes a datagram that will be fragmented. On
loopback (MTU 16384) it works; it is still wrong.

`ZeroCollapse` already exists (`msg/framing.go:119`) and is the exact
inverse of the expander, with a round-trip test. Give the Sender an
outbound option that collapses the body and sets the flag when the
message's `Info.Zerocoded` says the template allows it. Note that the
coding covers the message *number* framing as well as the body -- the
receiver expands before `DecodeID` -- so the collapse must span both.

## The five classes

**Forward.** Almost everything. `doc/messages.txt` decides direction; the
relay refuses to forward a message the wrong way, which turns a
classification mistake into a log line instead of a viewer that sits
there.

**Absorb.** `UseCircuitCode`, `CompleteAgentMovement`, `LogoutRequest`,
`RegionHandshakeReply`, `StartPingCheck`/`CompletePingCheck` from the
viewer; `StartPingCheck`/`CompletePingCheck` and `KickUser` from the
simulator. The circuit exists already; forwarding `LogoutRequest` would
end the real session the moment the user quit Firestorm.

**Synthesize.** `AgentMovementComplete` and `LogoutReply` toward the
viewer. Both are small and every field is already held
(`agent/agent.go:425`).

**Replay.** `RegionHandshake` and the land `LayerData`. Recorded as raw
bodies when they arrive, re-emitted on attach. This class did not exist
in the original design and is where stage 3 lives.

**Re-request.** The viewer's own startup questions, forwarded, get real
answers: `AgentWearablesRequest`, `RequestRegionInfo`,
`EconomyDataRequest`, `ParcelPropertiesRequest`, `AgentDataUpdateRequest`,
`MuteListRequest`, `RequestImage`. Free, and it covers most of what looks
at first like more replay work.

---

# Stage 0 -- spikes

A day or two, and every one of them changes a later stage.

## S0.1 -- does `RequestMultipleObjects` re-describe what the sim thinks we have?

The entire empty-world fix rests on this. The existing use
(`agent/objects.go:682`) only covers objects the simulator *told* us we
should have cached, which is not the same question.

Write it as a live test beside `agent/live_test.go`: connect as
`holt-beta`, wait for the object store to settle, take the local ids,
`Flush()` the store, send `RequestMultipleObjects` for those ids with
`CacheMissType: 0`, and count what comes back.

    SLGO_PROFILE=holt-beta go test ./agent -run TestLiveRedescribe -v

**Verification:** the store refills to within a few of its previous count
within ten seconds. If it does not, stage 4 becomes "synthesize
`ObjectUpdate` from the cache" after all, the plan grows a week, and the
result is worse.

**ANSWERED 2026-08-14: yes, completely.** In Dovet, a store that had
settled at 1449 objects was flushed to nothing and refilled to all 1449
in six seconds -- every one of them an id that had been asked for. The
replies came as 1435 compressed and 14 full update blocks, and zero
`ObjectUpdateCached` packets arrived during the window, so nothing but
the request accounts for the refill.

The simulator does not track what it has already described closely
enough to refuse. Stage 4 stands as written: a viewer attaching to a
running session gets the world from the simulator's own bytes, and
nothing is ever synthesized from slgo's cache.

## S0.2 -- does a second `RegionHandshakeReply` bring the terrain back?

In OpenSim, the reply is what triggers `SendLayerData`. Linden's simulator
is not OpenSim and this is not documented.

Same shape of test: connect, wait, register a counter on `LayerData`,
send a second `RegionHandshakeReply` (`agent/agent.go:418` builds one),
and see whether land patches arrive.

**Verification:** `LayerData` packets with a layer type of `L` or `M`
arrive after the reply and not before. If yes, terrain is one line. If
no, stage 3 grows a per-region raw patch store.

**ANSWERED 2026-08-14: no.** Dovet sent 38 land packets, all within the
first few seconds of the session, and then nothing. A second
`RegionHandshakeReply`, sent after the land had been quiet for twenty
seconds, produced not one further patch in thirty seconds of watching.
Linden's simulator does not behave like OpenSim here.

So stage 3 grows the terrain store -- but it is a small one. Those 38
packets carried **41,905 bytes** between them, for a whole region, and
one copy serves every avatar in it. Wind arrived separately and
continuously ('7', 20 packets in the same window), which is why anything
waiting for terrain to go quiet has to filter to `L`/`M` first; waiting
on `LayerData` as a whole never returns.

The timing is the real constraint, and it is sharper than the plan
assumed: the land arrives in the first seconds of the connection, so the
recorder cannot be something a viewer session switches on when it
attaches. It has to be registered with the agent's other handlers,
before `Connect` returns, and it has to write into the per-region store
so the cost is paid once per region rather than once per avatar.

## S0.3 -- a second Firestorm, on Aditi, that disturbs nothing

Firestorm 7.2.4 is at `/Applications/Firestorm-Releasex64.app`. Its
`app_settings/cmd_line.xml` accepts `--multiple`, `--settings`,
`--sessionsettings`, `--loginuri`, `--login`, `--autologin`, `--port`,
`--set`, `--purge`, `--novoice`, `--skipupdatecheck`. `grids.xml` already
carries Aditi (`https://login.aditi.lindenlab.com/cgi-bin/login.cgi`) and,
tellingly, a `localhost:9000` entry with an `http://` login URI -- so a
plain HTTP login endpoint is a shape the viewer is built to accept.

    open -na "Firestorm-Releasex64" --args \
      --multiple --settings viewer_slgod.xml \
      --set CacheLocation ~/Library/Caches/FirestormSlgo \
      --loginuri https://login.aditi.lindenlab.com/cgi-bin/login.cgi \
      --login Taren Holt '<password>' --autologin \
      --novoice --nosound --skipupdatecheck

Three things to settle here and not later:

- **The control server must be pinned first.** It runs with `--title ""`
  (`~/bin/start-control-server`) and re-attaches to the *largest*
  matching window, so a second viewer can take it away from Quark mid
  session. Restart it as `--title "Quark Idlemind"` -- the filter exists
  for exactly this and is tested (`~/src/control/server/main_test.go:42`)
  -- and give the second viewer a deliberately smaller window as a belt.
- **A second control server for the beta viewer**, on another port, with
  `--title "Taren Holt"`. It is the same binary from the same
  path, so the screen-recording grant should carry; confirm rather than
  assume, because if it does not the user has to approve it by hand.
- **The cache must not be shared.** Two Firestorms over one disk cache is
  a known way to lose an afternoon.

**Verification:** two Firestorm windows, Quark's untouched, `curl
localhost:21324/window` still naming Quark, and the beta viewer standing
in Dovet. This one is a prerequisite for every stage after it.

**ANSWERED 2026-08-14, and it is a blocker.** The installed viewer is
the **Second Life-only build** of Firestorm 7.2.4.80712, which cannot be
pointed at a login server that is not Linden Lab's. Everything below was
tested, in this order, and all of it failed:

- `--loginuri http://127.0.0.1:9080/` is **silently ignored**. The
  viewer logged in to `https://login.agni.lindenlab.com/cgi-bin/login.cgi`
  without a word about the flag. The capture server got nothing.
- `--grid localhost` and `--grid localhost:9000` both give
  `WARNING #GridManager# Unknown grid`, then `Default grid to
  util.agni.lindenlab.com`. The `localhost:9000` entry is present in
  both `app_settings/grids.xml` and `user_settings/grids.remote.xml`,
  so the plan's reading of it as an existence proof was wrong -- the
  entry exists on disk and the viewer does not load it.
- `--grid aditi` **works** (`setGridChoice: setting
  util.aditi.lindenlab.com`), which proves the mechanism is fine and the
  restriction is to Linden's own grids.
- A well-formed `slgod` entry written into `grids.remote.xml`, modelled
  field for field on the working `aditi` one, still gives `Unknown grid
  'slgod'`. The file-based list is not consulted when `--grid` is
  resolved at startup. (The entry was removed again; the file is back to
  its original three keys.)
- With `--set ForceShowGrid TRUE` the login screen does grow a **Grid:**
  dropdown, but it offers exactly *Second Life* and *Second Life Beta*.
- `skins/default/xui/en/floater_grid_manager.xml` is **absent** from the
  bundle. There is no grid manager to add one by hand. Only 14 strings
  in the binary mention OpenSim at all.

So stage 2 cannot be verified with the viewer that is installed, and no
amount of configuration will change that. Before stage 2 begins, one of
these has to happen:

1. **Install Firestorm for OpenSim.** A separate download from the same
   project, carrying the grid manager this build lacks. It installs
   alongside as its own bundle, so Quark's Second Life build is
   untouched -- which also makes it the natural home for the second
   instance S0.3 wanted, with its own settings and cache by
   construction.
2. **Use the official Second Life viewer**, which does honour
   `CmdLineLoginURI`. Lighter to install, but it is not the viewer the
   user actually works in, so it tests the protocol and not the workflow.
3. **Another OpenSim-capable viewer** -- Alchemy, Kokua, Singularity.

Option 1 is the one to take: the end goal is watching `automate` from
the viewer the user already lives in, and only the OpenSim build of that
viewer can be pointed at slgod.

**RESOLVED the same day.** The OpenSim build is installed at
`~/Applications/Firestorm-OpenSim.app` -- in the user's own Applications
folder, because this account is not in the `admin` group and
`/Applications` needs root. It is genuinely a different binary (the
SHA-256 differs from the Second Life build) even though both report
their channel as `Firestorm-Releasex64` and share the bundle identifier
`org.firestormviewer.firestorm`.

Three things about it that cost time and should not cost it twice:

- **`open -na <path>` launches the wrong bundle.** Both bundles carry the
  same identifier, so LaunchServices resolves to the copy in
  `/Applications` no matter which path is named. Launch the executable
  directly:
  `~/Applications/Firestorm-OpenSim.app/Contents/MacOS/Firestorm ...`.
  Verify with `ps` rather than the log, because the log's `Version:` line
  says `Firestorm-Releasex64` for both builds and cannot tell them apart.
- **`--loginuri` is ignored by this build too.** It is not a Second
  Life-build restriction; the flag simply does nothing here. The grid
  must exist in the grid list and be named with `--grid`.
- **The grid list that matters is `grids.user.xml`.** Not `grids.xml`,
  which this build ignores, and not `grids.remote.xml`, which is the
  downloaded cache. `user_settings/grids.user.xml` is created by the
  OpenSim build on first run and is what `--grid` resolves against.
  Adding an `slgod` entry there and passing `--grid slgod` works: the
  viewer sent its login to `http://127.0.0.1:9000/` on the next launch.

An `slgod` entry pointing at `http://127.0.0.1:9000/` has been left in
place, so slgod's login endpoint should listen there. The same entry is
also addable through the UI -- the OpenSim build's login screen carries a
**+ Click to add more grids** link that the Second Life build does not,
which is the quickest way to tell the two apart at a glance.

**Both builds share `~/Library/Application Support/Firestorm`.** Same
settings directory, same grid files, same logs. So the `slgod` grid will
appear in the Second Life build's dropdown as well (harmless -- it cannot
use it), and a per-profile `--settings` file is the only thing keeping
the two builds' preferences apart. Worth knowing before blaming a
mysterious setting change on the wrong viewer.

This is worth knowing now rather than at the end of stage 2. It costs a
download, not a redesign -- nothing about the relay changes, only which
bundle is launched -- but it would have presented as "the viewer ignores
slgod and logs in to the real grid instead", which reads exactly like a
bug in the login endpoint.

**Two smaller findings from the same session:**

- **The window title carries the avatar name, but only after a login is
  attempted.** At the login screen it is the bare string `Firestorm`;
  afterwards it is
  `Firestorm-Releasex64 7.2.4.80712 - flint prober`. So pinning the
  control server with `--title "Quark Idlemind"` protects Quark's window
  only once Quark is logged in, and matches nothing while the viewer sits
  at the login screen. A second viewer started during that window would
  still be the only match. Pin it, but do not rely on it alone: give the
  second viewer a smaller window as well.
- **`get_grid_info` does not appear in the binary** (0 occurrences),
  confirming the plan's claim that there is no grid-info endpoint to
  serve.

## S0.4 -- capture what Firestorm actually sends to log in

Stand up a throwaway HTTP handler on 127.0.0.1 that logs the request body
and answers a refusal, point the beta viewer at it with
`--loginuri http://127.0.0.1:9080/`, and keep the XML-RPC `methodCall` it
sends. `doc/login-parameters.md` says what it *should* be; a capture says
what it is, including the exact option list this build asks for and how
it presents the password.

**Verification:** an `agent/testdata` fixture holding a real Firestorm
login request, which the stage 2 parser is then tested against.

**ANSWERED 2026-08-14.** Firestorm 7.2.4.80712 posted a 3163 byte
`login_to_simulator` `methodCall` to the capture server. It is saved,
with the machine fingerprints blanked, as
`agent/testdata/firestorm-login.xml`, and stage 2's parser should be
written against it rather than against the documentation.

What it actually sends, and where it differs from what was assumed:

- **27 options, not the 23 `doc/login-parameters.md:99` lists.** In
  order: `inventory-root`, `inventory-skeleton`, `inventory-lib-root`,
  `inventory-lib-owner`, `inventory-skel-lib`, `initial-outfit`,
  `gestures`, `display_names`, `event_categories`,
  `event_notifications`, `classified_categories`, `adult_compliant`,
  `buddy-list`, `newuser-config`, `ui-config`, `advanced-mode`,
  `max-agent-groups`, `map-server-url`, `voice-config`,
  `tutorial_setting`, `login-flags`, `global-textures`, `currency`,
  `max_groups`, `search`, `destination_guide_url`, `avatar_picker_url`.
  This is the list slgo's own login must request, since the response it
  replays can only contain what it asked for. Today it asks for two of
  them.
- **`passwd` is `$1$` followed by an MD5**, exactly the form
  `hashPassword` (`agent/login.go:232`) already produces. Authenticating
  the viewer against the profile's stored digest is a string comparison,
  as planned.
- The rest of the struct: `address_size`, `agree_to_tos`, `channel`,
  `extended_errors`, `first`, `host_id`, `id0`, `last`,
  `last_exec_duration`, `last_exec_event`, `last_exec_session_id`,
  `mac`, `mfa_hash`, `platform`, `platform_string`, `platform_version`,
  `read_critical`, `start`, `token`, `version`. `first` and `last` are
  separate strings, and `start` was `last`.
- `Content-Type` is `text/xml` and the viewer offers
  `Accept-Encoding: deflate, gzip`, so the login endpoint must not
  assume an identity encoding on the way back.

`mac` and `id0` are host fingerprints. They are blanked in the fixture
and slgod should neither log nor forward them.

The capture tool is kept at `scratchpad/logincapture`: it records
method, URL, headers and body, and answers every login with a refusal,
so nothing is ever signed in by pointing a viewer at it.

---

# Stage 1 -- tracing, before anything is relayed

**Goal.** When the viewer shows a grey void, be able to say which side
stopped talking and about what. Firestorm does not complain; it renders
nothing and waits.

**Files.** New `viewer/trace.go`; `msg/dispatch.go` (the `WithRelay`
hook); `cmd/slgod/main.go` (flags).

**The work.**

- A trace that writes both directions to one file in arrival order, using
  `msg.AppendPacketYAML` (`msg/dump.go:46`), each entry tagged
  `sim>slgod`, `slgod>sim`, `viewer>slgod`, `slgod>viewer`, with the
  sequence number in *both* domains where a packet was relayed. The
  emitter is already fixed and parseable and there is a test that Ruby's
  Psych reads it.
- A running census: per message name, count and last-seen, per direction,
  plus a fourth column for what was dropped and the reason (`absorbed`,
  `wrong direction`, `no viewer attached`). This is the thing that
  actually finds a misclassified message -- a viewer that will not
  proceed is nearly always waiting on one message that a counter shows as
  zero.
- `-trace <file>` and `-trace-messages <names>` on slgod.
- `WithRelay` on the dispatcher, per the defect above.
- A `fakeViewer` test harness modelled on `fakeSim`
  (`agent/agent_test.go:53`): a UDP socket that speaks the viewer half of
  the handshake and records what it is told.

**Risks.** Tracing every packet of a busy region is a lot of YAML.
Default to names-and-counts, full bodies only for a named set.

**Verification.** Unit test only, and say so: `go test ./viewer` drives
the fake viewer through a handshake and asserts the census shows the
expected messages in the expected directions. Nothing renders yet.

**DONE 2026-08-14.** `viewer/trace.go` holds the Census and Trace,
`msg.WithRelay` fires after duplicate suppression, and
`slgod -trace FILE` records a live session. Three things came out of
building it that the plan did not anticipate:

- **A trace needs an outbound hook as well as the tap.** `agent.Options`
  had only `Tap`, which sees what arrived, so the first live trace read
  `slgod>sim 0` -- a record that cannot answer "was it ever sent", which
  is the question a relay actually gets asked. Added `msg.WithSendTap`,
  fired from `Sender.transmit` after the write, and
  `agent.Options.SendTap` to reach it. It deliberately ignores
  retransmissions, which carry the original's sequence number and would
  show one message as several.
- **`StartAgent` was overwriting a caller's `Tap`** rather than chaining
  it with the relay's, so slgod's trace would have been silently
  discarded. Fixed and given a test; it was a latent bug independent of
  this work.
- **The two sequence domains are visible in the first trace**, which is
  a useful thing to be able to point at:
  `slgod>sim RegionHandshakeReply seq=3` sits beside
  `sim>slgod RegionHandshake seq=3`, two unrelated threes.

A baseline capture of an ordinary Aditi session is 618 packets over 30
seconds, 563 in and 55 out, across 27 message types -- which is the
comparison a handover trace gets read against.

---

# Stage 2 -- the handover: Firestorm logs in and holds a circuit

**Goal.** Firestorm connects, gets past "Loading world", and stays
connected. It will be grey. That is the correct result for this stage.

**Files.** New `viewer/login.go`, `viewer/circuit.go`, `viewer/session.go`;
`agent/xmlrpc.go` (encoding, and decoding a `methodCall`);
`agent/presence.go` (suspend); `cmd/slgod/main.go`.

**The work.**

*The login server.* `agent/xmlrpc.go` decodes a `methodResponse` and
nothing else. Two additions: decode a `methodCall`'s single struct
parameter, and encode a `methodResponse`. `decodeValue`, `decodeStruct`
and `decodeArray` are all reusable as they stand; the cleanest move is
`internal/xmlrpc` with `agent` and `viewer` both importing it, since
duplicating a parser that took care to be forgiving would be a shame.

The response is `Account.Raw` (`agent/login.go:126`) with three fields
replaced -- `sim_ip`, `sim_port`, `seed_capability` -- and nothing else
touched. Everything the login server said comes through: the inventory
skeleton, `udp_blacklist`, `region_size_x`, `agent_access`, the gestures,
the global textures. This is worth far more than composing a response by
hand, and it is why the login must *ask*.

**It does not ask today.** `agent/login.go:375` requests
`inventory-root` and `buddy-list`. `doc/login-parameters.md:99` lists the
twenty-three blocks Firestorm asks for. `agent/profile.go:168` already
parses an `options =` line, so this is a profile change for the beta
accounts and a default change for anything meant to host a viewer.
Without it Firestorm comes up with no inventory and, per the note at
`lllogininstance.cpp:167` quoted in that doc, may trip its own
`mFatalNoLibraryRootFolder` assertion -- a viewer-side abort with no useful
message.

Authenticate the viewer by comparing the `passwd` it sends against the
profile's stored `$1$` digest. It is the same form on both sides
(`hashPassword`, `agent/login.go:232`), so it is a string comparison and
it is free. Bind to 127.0.0.1. slgod already refuses to serve gRPC
unauthenticated for the same reason (`cmd/slgod/main.go:321`).

*The circuit.* `net.ListenUDP` on an ephemeral port, bound before the
login response is composed so the port can be named in it. `msg.Receiver`
takes it directly -- `PacketSource` is `ReadFrom` (`msg/receive.go:23`).
`msg.Sender` wants a `Write` (`msg/send.go:24`), so a ten-line adapter
that remembers the viewer's address from the first datagram and
`WriteToUDP`s back to it.

Then: absorb `UseCircuitCode`, checking the code and session id match the
held session -- a mismatch is a bug worth a loud log, not a silent drop.
Absorb `CompleteAgentMovement` and answer with a synthesized
`AgentMovementComplete` built from `a.Position()`, `a.RegionHandle()` and
`a.ChannelVersion()`, all of which are kept (`agent/agent.go:425`).

*The camera.* Suspend `sendPresence` (`agent/presence.go:85`) while a
viewer is attached and forward the viewer's `AgentUpdate` instead, feeding
its `CameraCenter` and `Far` into `SetLook` (`agent/presence.go:75`) on
the way through. The trim (`agent/presence.go:134`) then follows the
user's real camera, which is the useful consequence. A guard: if the
viewer goes quiet for more than a few seconds, resume slgod's own
presence, because a session in nobody's interest list gets no object
updates at all and recovers only by accident.

**Risks.** The viewer may refuse a login response missing a field this
plan has not thought of; the fixture from S0.4 and the real `Raw` make
that unlikely but not impossible. Firestorm's own log
(`~/Library/Application Support/Firestorm/logs/Firestorm.log`) is the
first place to look and should be read alongside the stage 1 trace.

**Verification.**

    slgod -listen :7807 -viewer holt-beta holt-beta &
    open -na Firestorm-Releasex64 --args --multiple \
      --settings viewer_slgod.xml --loginuri http://127.0.0.1:9080/ \
      --login Taren Holt '<password>' --autologin ...

Screenshot the beta window. Expect: the login screen gone, a title bar
naming Taren Holt, the region name in the top bar, and a grey or
white world. Expect in the trace: `UseCircuitCode` and
`CompleteAgentMovement` absorbed, `AgentMovementComplete` synthesized,
`AgentUpdate` flowing viewer->sim at the viewer's rate. Expect on the
grid: slgod's session still up, `slsh status holt-beta` unchanged,
`Send.Stats().Abandoned` still zero.

---

# Stage 3 -- the region: handshake, terrain, capabilities

**Goal.** Ground under the avatar and a sky over it.

**Files.** `agent/agent.go` and `agent/region.go` (keep the raw
handshake); new `agent/replay.go`; new `viewer/caps.go`,
`viewer/eventqueue.go`; `agent/eventqueue.go` (fan-out).

**The work.**

*Replay the handshake, do not rebuild it.* `regionFromHandshake`
(`agent/region.go:104`) keeps twelve fields of a message that has
thirty-odd, and the ones it drops are the eight terrain texture UUIDs,
the eight start-height and height-range floats, `CacheID` and
`BillableFactor` -- precisely what decides whether the ground has a
texture on it. Keep the packet's raw body next to the decoded `Region` in
`regionState` and re-emit it byte for byte. The viewer's
`RegionHandshakeReply` is then absorbed (and, per S0.2, possibly
forwarded once as a deliberate act).

*Terrain.* S0.2 has answered: the land does not come back for the
asking, so it must be kept.

Record every `LayerData` whose layer type is `L` or `M` as a raw body,
from the moment slgod connects, in the per-region store beside the
objects (`agent/regions.go:43` is where a region's shared state lives).
Avatars sharing a region then share one copy of the terrain, and it is
dropped when the last of them leaves. Replay the recorded bodies on
attach, in arrival order.

Three things S0.2 settled that shape the code:

- **It is cheap.** A whole region was 38 packets and 41,905 bytes. There
  is no case for compressing, paging or bounding it.
- **The recorder must exist before `Connect` returns.** The land arrives
  in the first seconds of the session, so a handler that a viewer
  attaches later would already have missed it. This goes in with the
  agent's other handlers, unconditionally, whether or not a viewer is
  ever attached -- 42 KB a region is a fair price for not having to
  predict that.
- **Filter to land, and only land.** Wind ('7') streams continuously for
  as long as the session lives. Recording it would grow without bound,
  and any wait for terrain to go quiet has to ignore it or it never
  returns.

Do not decode the patches. The bodies go out exactly as they came in,
which is the same discipline as the handshake and the object updates.

The risk left is staleness after terraforming, which is rare and
self-corrects: live edits arrive as ordinary `LayerData` and overwrite
the recorded patch.

*Capabilities.* slgod serves a seed capability of its own over plain HTTP
on 127.0.0.1 -- no TLS; the `localhost:9000` entry in Firestorm's
`grids.xml` is the existence proof that the viewer accepts it, and
Firestorm 7.2.4 does not query `get_grid_info` (no such string in the
binary), so there is nothing else to serve.

Do not enumerate capabilities. Proxy the viewer's seed request to the
real seed URL with `RequestCaps`-shaped machinery (`agent/caps.go:77`)
and hand back the answer with **one** entry rewritten: `EventQueueGet`,
pointed at slgod. Everything else -- `GetTexture`, `GetMesh2`,
`ViewerAsset`, `InventoryAPIv3` -- is handed over as the simulator's own
URL, so texture and mesh traffic goes straight from Firestorm to the
simulator's HTTP host and never touches slgod. That is most of the bytes
of a viewer session, avoided.

*The event queue has one consumer.* Events are delivered once and
acknowledged; two pollers split them at random. slgod stays the sole
poller (`agent/eventqueue.go:72`) and fans out from `deliver`
(`agent/eventqueue.go:173`), which already re-encodes each event's body
to bytes for exactly this reason. slgod's own endpoint holds the viewer's
long poll, hands over what has queued with its own `id`, and honours the
viewer's `ack` and `done`.

Filter `EstablishAgentCommunication` out of the viewer's stream for now.
It carries a neighbouring simulator's address and seed capability, and
the viewer would open a child circuit directly to that simulator with
this circuit code -- which might work, and might also walk the agent into
a region slgod is not talking to. Neighbours render empty until a later
stage decides deliberately. Say so in the release note; it is visible.

**Risks.** The long-poll semantics are fiddlier than they look: a viewer
that misses an event never learns of it. Keep a small ring of undelivered
events so a poll that arrives late does not lose them, and count what is
dropped.

**Verification.** Relaunch the beta viewer through slgod, screenshot.
Expect ground with terrain textures on it, sky, water at the right
height, and the region name and parcel bar populated. Expect `Ctrl-Shift-1`
statistics to show a non-zero packet rate. Expect the census to show
`RegionHandshake` and `LayerData` replayed exactly once each and
`EventQueueGet` polled by slgod alone. Compare against a screenshot of
the *direct* login from S0.3, same camera: the ground should match.

---

# Stage 4 -- the world: refilling the objects

**Goal.** The region has things in it.

**Files.** `agent/objects.go`, `viewer/session.go`.

**The work.** On attach, take every local id in the store
(`Objects.All()`) and ask the simulator to describe them again with
`RequestMultipleObjects`, batched exactly as `requestCachedObjects`
already does at `agent/objects.go:682` -- a hundred to a datagram, because
a simulator that cannot parse an oversized request answers none of it.
Pace the batches; a region with two thousand objects is twenty datagrams
and several hundred kilobytes of reply, and hurrying it only fills the
simulator's throttle.

The replies arrive as ordinary `ObjectUpdate` and
`ObjectUpdateCompressed`, go through the relay to the viewer, and back
into slgod's own cache on the way past. Nothing is synthesized, so every
field the viewer needs -- extra params, sculpt and mesh, particles, media,
flags -- is the simulator's own bytes.

What slgod trimmed, or never heard, heals itself: the viewer's draw
distance is now driving `AgentUpdate`, the simulator sends
`ObjectUpdateCached` for what comes into interest, and the handler at
`agent/objects.go:642` already turns every one of those into a request
whose answers are relayed. That path is the reason this works at all and
it is already built.

Forward `ObjectUpdateCached` to the viewer as well. Firestorm has a disk
cache and may already hold the object; if it does, it draws it without
waiting for slgod's request to come back.

**Risks.** A big region could bury the viewer under a burst it is not
throttled for. The viewer's own `AgentThrottle`, forwarded, is the
control -- slgod never sends one, so today the simulator uses defaults.
The refill should also be idempotent and interruptible: a viewer that
attaches, detaches and reattaches must not queue two of them.

**Verification.** Screenshot at the same camera as the direct-login shot
from S0.3. Expect the same objects in the same places. Then, quantitative:
`slsh objects --agent holt-beta` counts what slgod knows, and the
viewer's `Ctrl-Shift-1` statistics count what it has drawn; the two should
land within a few percent. A void here means S0.1 was answered wrong.

---

# Stage 5 -- avatars, parcels, and the rest of the say-it-once traffic

**Goal.** Other people are people, the parcel bar says who owns the land,
and the avatar is wearing its own clothes.

**Files.** `viewer/classify.go`, `agent/replay.go`.

**The work.** Build the classification table properly, driven by
`doc/messages.txt`: a generated table with an override list for the
absorb, synthesize and replay sets, and a test that every message in the
template has a class. A message with no class is a build failure, not a
runtime shrug.

Then work the tail by watching the census for messages the viewer wants
and never gets. The re-request class does most of it -- Firestorm sends
`AgentWearablesRequest`, `RequestRegionInfo`, `EconomyDataRequest`,
`ParcelPropertiesRequest` and `MuteListRequest` during startup, and
forwarded, they are answered by the simulator for real.

Avatars are the open question. Since server-side baking the appearance
textures travel in an avatar's `TextureEntry`, which arrives in the
`ObjectUpdate` that stage 4's refill already produces -- so avatars may
simply work. If they come out as default grey figures, `AvatarAppearance`
joins the replay class. This cannot be settled from the code; it is a
screenshot.

**Risks.** This is the stage that has no natural end. Time-box it by the
census: when nothing the viewer is waiting on shows zero, stop.

**Verification.** Screenshot in Dovet with another avatar present (log the
`hobb-beta` profile in through the second slgod session, or stand near
someone). Expect: a shaped, textured avatar rather than a grey figure;
your own avatar clothed; the parcel name in the top bar; the minimap
populated. Read the chat log for anything the viewer complained about.

---

# Stage 6 -- the viewer sees what automation does

**Goal.** Run an `automate` script and watch the result appear.

**Files.** None expected.

**The work.** Nothing, if stages 3 and 4 are right. slgod's packets
already go to the simulator, the results already come back as ordinary
`ObjectUpdate` and `ChatFromSimulator`, and the relay already forwards
them. This stage exists to *prove* that, and to catch the one thing that
could break it: a message slgod's own handlers consume and the relay
therefore does not forward. The trace's "absorbed" column is where that
shows.

**Verification.** With the viewer attached:

    /Users/quark/bin/automate --agent holt-beta rez-and-say.lsl

Expect the prim to appear in the viewer within a second or two and its
`llSay` to appear in local chat. Screenshot both. Then the reverse: rez
something in the viewer and check `slsh objects --agent holt-beta`
sees it. That second half is the honest test of stage 4's cache staying
current under a viewer's authority.

---

# Stage 7 -- the viewer acts

**Goal.** Touch, chat, select, edit.

**Files.** `viewer/classify.go`, `viewer/circuit.go`.

**The work.** Mostly already done: 223 messages are `out` in
`doc/messages.txt` and the default for `out` is forward. What needs care
is the small set where forwarding is wrong or where slgod holds state
that a viewer's action invalidates:

- `ObjectSelect`/`ObjectDeselect` -- forwarded, and their `ObjectProperties`
  replies go both to the viewer and into slgod's name cache
  (`agent/objects.go:656`), which is a small free win.
- `ObjectGrab`/`ObjectGrabUpdate`/`ObjectDeGrab` -- forwarded. Touch.
- `ChatFromViewer` -- forwarded. Note that slgod's own clients also chat;
  both end up on the same circuit, which is correct and is the point.
- `AgentSetAppearance` -- forwarded, and it means the viewer will rebake.
  Worth a line in the docs: attaching a viewer changes what the avatar
  looks like to everyone, because slgod never sends one.
- The `int` class -- 99 messages that never appear on a viewer's circuit.
  Refuse them in both directions and count the refusals.

**Risks.** Locks. slgod's clients take per-session locks
(`server/lock.go`) over things like the auto objects, and a user editing
the same prim through the viewer will fight a benchmark. This does not
need solving now, but it needs saying: a viewer session should be listed
by `slsh agents` as an attached client so `logout` can refuse while it is
there, exactly as it does for a gRPC client.

**Verification.** `curl "http://localhost:<beta-port>/clicktext?grep=<object
name>"` to touch a scripted prim; expect its reply in local chat in the
viewer *and* in `automate`'s output from the same session -- one session,
two witnesses. Then type in local chat via `/paste` and check the message
arrives at a second avatar. Then move a prim with the build tools and
confirm `slsh objects` reports the new position.

---

# Stage 8 -- asking for a viewer, and getting rid of it

**Goal.** `slsh viewer holt-beta` brings the endpoint up and prints
the login URI; ending it puts slgod back the way it was.

**Files.** `proto/slgo.proto`, `server/grpc.go`, `cmd/slgod/main.go`,
`cmd/slsh/`, `doc/guide.md`, `README.md`.

**The work.** A `Viewer` RPC that starts the login endpoint and the UDP
socket for a named session and returns the URI to paste into Firestorm's
`--loginuri`; a `-viewer <profile>` flag for starting slgod with one
already up. Teardown restores slgod's presence loop, its own draw
distance and its camera -- a camera left where the viewer's user parked it
would quietly empty the interest list for every client that connects
afterwards, which is the failure `setCenter` (`agent/presence.go:65`)
exists to prevent and which the comment there already warns about.

Detach on `LogoutRequest` from the viewer (absorbed, answered with a
synthesized `LogoutReply` so Firestorm quits cleanly), and on a viewer
idle timeout for the case where the window was just closed.

The `README.md` layout list needs a pass anyway -- it still describes
`client/login.go`, `client/session.go` and `client/caps.go`, all of which
now live under `agent/`.

**Verification.** Bring a viewer session up, use it, quit Firestorm
normally, and confirm from `slsh status holt-beta` that the session
is still up, the draw distance is back to slgod's, and `automate` still
runs. Then repeat with the window force-quit rather than logged out.

---

# What is genuinely uncertain

Places where the code could not answer the question and a spike has to.

- ~~**Whether `RequestMultipleObjects` re-describes objects the simulator
  believes the agent already holds.**~~ Answered 2026-08-14: yes,
  1449 of 1449. Stage 4 stands.
- ~~**Whether the terrain can be asked for again.**~~ Answered
  2026-08-14: no. Stage 3 keeps a per-region store of raw land bodies,
  recorded from connect. 42 KB a region.
- **Whether other avatars render from the refilled `ObjectUpdate` alone,
  or whether `AvatarAppearance` must be replayed.** Only a screenshot in
  stage 5 can say.
- ~~**Which viewer this is actually built against.**~~ Answered the hard
  way: not the one that was installed. The OpenSim build now is, at
  `~/Applications/Firestorm-OpenSim.app`, reached by `--grid slgod`
  against an entry in `grids.user.xml`.
- ~~**What Firestorm actually sends in a login request.**~~ Captured;
  see S0.4 and `agent/testdata/firestorm-login.xml`.
- **Whether a second screen-recording grant is needed for a second
  control server.** TCC grants are per binary path, and the OpenSim
  build is a different path, so this has gone from "probably not" to
  "almost certainly yes, once" -- it will be a dialog at whatever moment
  the second control server first tries to screenshot.
- **Whether the two builds sharing one settings directory will bite.**
  They share `~/Library/Application Support/Firestorm` entirely. A
  `--settings` file per build keeps preferences apart; the grid list,
  logs and cache are not separated by it.
- **What Firestorm does with a `seed_capability` on `http://` while the
  other capability URLs are `https://` on a different host.** No reason
  it should mind; nothing in the code proves it does not.
- **How the simulator reacts to an `AgentThrottle` arriving mid-session
  from a circuit that has never sent one.** Almost certainly fine;
  it is a change of regime for a live circuit and worth watching in stage 4.
- ~~**Region crossings and teleports.**~~ Decided 2026-08-16 by
  `doc/teleport.md`'s stage 6: **refuse**. What this said before -- that
  a viewer which teleports takes the agent to a simulator slgod is not
  connected to and ends the session -- stopped being true when the
  daemon learned to follow a teleport. What happens instead is quieter
  and worse: the move succeeds, the daemon arrives, and the viewer is
  told none of it, so it goes on drawing a region the avatar has left
  while the new region's object updates land on top of the old ones
  under local ids that now mean something else. Nothing reports an
  error. So a teleport out of this region is absorbed in `fromViewer`
  with a modal alert saying why and what does work; a teleport *within*
  the region is still forwarded, told apart by the handle; and
  `TeleportFinish` is withheld from the viewer's event queue beside
  `EstablishAgentCommunication`, since `slsh tp` alone can put one there.
  A viewer whose avatar is moved by another client is told to attach
  again, because replaying the new region to it is "follow", which is
  not built.
