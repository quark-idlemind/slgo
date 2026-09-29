# Money

What the grid says about an avatar's L$, what paying looks like on the
wire, and the rules a profile puts on the programs that pay or buy
with it. The comments in `agent/money.go`, `agent/prices.go`,
`internal/pay`, `server/pay.go`, `sl/money.go` and `cmd/slsh/money.go`
say what the code does. This page is why.

## What the grid says

Measured on Aditi, the beta grid, in its play money, on 2026-09-28, by
Claude. Two avatars were held by one slgod: qi, and a second avatar
called Example Resident here.

- A `MoneyBalanceRequest` with a zero `TransactionID` was answered in
  well under a second with a `MoneyBalanceReply`: `TransactionID` zero,
  `TransactionSuccess` true, the balance, an empty `Description`, and
  a `TransactionInfo` of type 0, with zero ids and an `Amount` of -1.
- Paying L$1 -- a `MoneyTransferRequest` of `TransactionType` 5001,
  with a `Description` -- brought one `MoneyBalanceReply` to each side
  about 0.2 s later, both with the same `TransactionID`. The payer's
  had success true, its new balance, the `Description` "You paid
  Example Resident L$1.", and a `TransactionInfo` of type 5001 with
  `SourceID` the payer, `DestID` the payee, `Amount` 1, and
  `ItemDescription` the `Description` that was sent, echoed. The
  payee's was the same but for its own balance and the `Description`
  "Quark Idlemind paid you L$1.".
- Paying more than the balance: only the payer heard anything. Its
  reply had success false, the balance unchanged, the `Description`
  "Insufficient funds.", and the `TransactionInfo` filled in as for a
  success -- type 5001, both ids, the amount asked for, and the echo.
- No `ImprovedInstantMessage`, `AlertMessage` or `AgentAlertMessage`
  came with any of it; a trace of those three held none.

So the reply is the whole of the answer, a refusal included, and
`agent` keeps the balance from every one of them
(`Agent.Balance`) and hands each to `Options.OnMoney`.

## What the viewer sends

Read from Firestorm's source, not measured. `give_money`
(`newview/llviewermessage.cpp:458-494`) fills every
`MoneyTransferRequest`:

- `SourceID` the agent, `DestID` whoever or whatever is paid.
- `Flags` from `pack_transaction_flags(false, is_group)`
  (`llinventory/lltransactionflags.cpp:42-48`): 0 for an avatar and for
  an object, 2 for a group.
- `AggregatePermNextOwner` and `AggregatePermInventory` both
  `AP_EMPTY`, 0 (`llinventory/llpermissions.h:396`).
- `TransactionType` from the pay dialog: `TRANS_GIFT`, 5001, for an
  avatar, and `TRANS_PAY_OBJECT`, 5008, for an object
  (`newview/llfloaterpay.cpp:622-624, 648`;
  `llinventory/lltransactiontypes.h`).
- `Description`: for an avatar, the dialog's payment message, which
  takes at most 127 bytes (`skins/default/xui/en/floater_pay.xml:109`)
  and is empty when none was typed; for an object, the root object's
  name (`llfloaterpay.cpp:615-624`).

It sends nothing for an amount of 0, takes the absolute value of any
other, refuses a null id, and sends nothing it cannot afford by the
balance it last heard, opening the buy-currency window instead
(`llviewermessage.cpp:461-470, 488-493`; `can_afford_transaction`,
`newview/llstatusbar.cpp:1228-1231`). `sl` does the same, but refuses a
negative amount rather than turn it round, and reads the balance just
before paying rather than trust an old one.

For an object, `DestID` is the object and not its owner.

## Finding a payment's answer

Nothing in a `MoneyTransferRequest` is the sender's to choose and comes
back in the reply: the transaction id is the grid's. So `sl` finds a
payment's answer by what the answer describes -- the type, this avatar
as the source, the destination, the amount, and the reason echoed back
-- and a payment waiting takes the first answer that matches it, the
oldest waiting first, so that two payments alike are answered one each.

A payment received in the meantime has somebody else as its source and
is never taken for the one sent. A plain balance has no source at all.

Two things are read from the viewer's source rather than seen:

- The viewer takes an echoed "Payment" to mean no reason was given --
  "Simulator returns "Payment" if no custom description has been
  entered" (`llviewermessage.cpp:5846-5847`) -- so a payment sent with
  no reason also takes an answer echoing "Payment".
- Which id the grid puts in `DestID` when an object is paid, the object
  or its owner, has not been measured, so `PayObject` takes either.

An answer that matches nothing is not lost: it is delivered, and it
counts in the balance check below.

## Never twice

`Pay` never sends a payment again. A reply that does not come is not a
payment that did not happen -- the one measured came 0.2 s later, over
UDP, and either leg can be lost -- and nothing in the request lets the
grid tell a second one from a first. Sending again is how a lost reply
becomes two payments.

## After an answer that did not come

The balance is read before paying, and again when the answer has been
waited for as long as it will be. The difference is what the balance
check goes by:

- down by exactly the amount: paid, and the answer was lost.
  `PayUnconfirmed` says `PaidUnconfirmed`, and slsh reports it as paid.
- the same: not paid.
- anything else: not known, with both balances named, so that a person
  can work it out.

What else moved the balance in between is allowed for, as far as it can
be told apart: a payment received arrives as its own reply and is
added, and a payment another call took as its answer is taken off.
A payment of this avatar's that no call took as its answer -- another
client's, a viewer's, or this one's with an echo that did not match --
could be anything, so with one of those in between an unchanged balance
is not known rather than not paid. So is one with more answers in
between than the session keeps, sixty-four.

A caller that gives up is owed the same answer, so the balance is read
on a context the cancel does not reach.

## How long to wait

0.2 s was measured on a quiet region. A busy one can take much longer,
and a bound too near the measurement turns a slow success into a
payment reported not confirmed. So every wait for the grid about L$ --
`Balance`, a payment's answer, and the balance read after an answer that
did not come -- is bounded at 15 s, the figure the other read-backs
use, and `sl.Options.MoneyTimeout` changes it. A test that proves one
runs out sets it short.

## The rules

A profile says what its avatar's programs may pay:

    pay       = on
    pay_max   = 10
    pay_daily = 10
    pay_to    = Example Resident
    pay_to    = 92f67e57-7e57-c0de-24de-53f27a898992

- `pay = on`, and paying is off without it.
- `pay_max`: the most one payment may be, L$10 when not given. A
  purchase is held to it item by item: each object of an `ObjectBuy` is
  one.
- `pay_daily`: the most all of them may come to in the last 24 hours,
  rolling, L$10 when not given. Payments and purchases are one total.
- `pay_to`: an avatar that may be paid, by name or by key, any number
  of lines, and `pay_to = *` for anybody. None is nobody, so `pay = on`
  alone still pays nobody, and the refusal says to add `pay_to`. A
  purchase pays whoever it pays -- an object's owner, a parcel's
  owner, a group -- and that key must be here; a fee the grid itself
  takes has nobody to be here, so `pay = on` and the two limits are
  all that hold it.

The defaults are small on purpose. A program that pays is a program
that can empty an account by looping, and one that was only ever meant
to tip a few L$ should not need anybody to have thought about limits
before it is safe.

A refusal for a payee says who it was, with the name when it is known:
`Example Resident (92f67e57-7e57-c0de-24de-53f27a898992) is not in
this profile's pay_to`. The name is one the session has heard, or one it
asks the grid for with a `UUIDNameRequest` and waits three seconds for;
a refusal that gets no name says the key alone rather than wait longer.

A name is asked of the grid, by the whole-name search a viewer's older
search makes (`AvatarPickerRequest`), and the key it answers with is
what is matched. A name of one word is taken as that word and
`Resident`, and a dot as the space it stands for. The answer is kept
for the life of the session, a name nobody has included. A name the
grid could not be asked about refuses the payment, and is asked about
again next time.

An object is paid only when its owner may be, and the owner is what the
region said in the object's properties. An object nobody has said an
owner for is refused; `sl.PayObject` asks for the owner before it pays,
which is what puts it where slgod looks.

All of this is in `internal/pay`, which slgod and `sl` both use.

## Where the rules are checked

In slgod, where it forwards a client's messages, and not only in `sl`.
A client's message is a body the daemon frames and forwards, so any
client with the secret can send its own `MoneyTransferRequest`,
whatever it was written with; a check in `sl` alone would hold only the
programs that chose to use it. `sendMessage` decodes that one message
-- and the few below that spend, the only bodies the server reads --
checks it, and sends it only if the rules let it through. The unary
`Send` is checked the same way.

The messages that buy are checked at the same two places, by the same
gate: `pay.Spends` says which message numbers are looked at, so that the
body of any other is never read, and `Gate.CheckMessage` reads one and
decides. A body sent as it is framed (`msg.Raw`) is decoded first, and
one that will not decode is refused rather than let past unread. The
capability that answers a group invitation, `AcceptGroupInvite`, is a
request and not a message; slgod's `Cap` and `Direct.DoCap` check it as
accepting the invitation, whether it is named or asked for by its
address (`Agent.CapOf`).

A viewer handed a session through slgod is not checked. That is a person
using the viewer's own pay dialog, with its own confirmation, and the
rules are about programs. Nor is it for a purchase: a viewer's ObjectBuy
goes down its own circuit, as its payment does. Nothing needs doing to exempt it: a viewer's
messages go down its own circuit, straight to the session, and never
pass through `sendMessage`.

A session held with `--direct` has no daemon, so `sl`'s `Direct`
checks the same rules itself, in `Send`, from the profile it logged in
with.

Every payment or purchase let through and every refusal is logged by
slgod: who asked, whom it was for, how much, and why it was refused or
which `pay_to` line let it through. A purchase's line reads `asked to buy
with ObjectBuy a copy of object 77 for L$6 to 92f67e57-...; passed by
pay_to ...`. What the grid then says of each payment
this avatar makes is logged too.

## How a refusal reaches the client

A client on a stream is sent a `MoneyBalanceReply` that slgod made up,
signed `slgod` in `from_client`: success false, the transaction filled
in as it was asked for -- as the grid fills in its own refusal -- the
reason as its `Description`, and the last balance slgod heard, or -1
when it has heard none. It goes on the queue of answers to that
client's own requests, which is never dropped, and to that client
whatever it subscribed to, and to no other.

It is the simplest thing the stream already carries. A program already
waiting for the grid's answer to its payment finds this one the same
way, and reads the reason where it would have read the grid's; a
program that matches nothing is still sent the message it would have
been sent for a refusal. A new kind of notice would have needed a new
field on the wire and a new case in every client. The signature is
what tells it from the grid's: a `MoneyBalanceReply` with any other
`from_client` is an echo of something a client sent, and `sl` believes
neither kind as a balance.

A refused purchase is answered the same way, with the closest thing the
stream already carries: a `MoneyBalanceReply` signed `slgod`, success
false, the reason as its `Description`, and a `TransactionInfo` of the
purchase's own transaction type (below) with this avatar as the source,
whoever was to be paid as the destination when that is known, and the
amount, or -1 for a message that has no figure. `ItemDescription` is the
message's name. Nothing more specific exists to send: none of these
messages has a reply of its own that a refusal could be, so nothing but
a program watching for the `slgod` signature will see one. `sl`'s calls
that buy do not wait for it, so through slgod `AcceptInvitation` returns
nil for a refused acceptance, as it does for one the grid ignores; a
session held with `--direct` returns the `PayRefused` from `Send`.

The unary `Send` has no stream to answer on, so its refusal is its
error, `PERMISSION_DENIED` with the reason. So is the `Cap` call's.

## The daily total

A record of every payment or purchase let through -- when, how much, and to whom --
kept one file per profile, `pay-PROFILE`, mode 600, in slgod's own
directory (`~/.config/slgod`, or the `-config` directory), beside the
seats. A payment is dropped from it a day after it was made. It is read
again before every payment and written whole after one, so the total
outlives a restart of slgod, and a `--direct` session of the same
profile, which keeps its record in the same place, counts against the
same total.

It counts what was let through, not what the grid confirmed. A payment
the grid then refused still counts until it is a day old, and so does
one whose answer was lost. Erring that way pays less than the limit,
never more.

A record that cannot be read stops the profile paying until somebody
looks, rather than count from nothing.

## What else spends L$

Read from Firestorm's source and its message template, not measured:
nothing here was sent to the grid to see what it does. A search of the
viewer for every message that carries a price, a fee or an amount, and
of what sends each, found these, besides the payment above.

| Message | Where the viewer sends it | What it costs |
|---|---|---|
| `ObjectBuy` | `LLSelectMgr::sendBuy`, `newview/llselectmgr.cpp:5015-5036`, from the buy floaters (`llfloaterbuy.cpp:330`, `llfloaterbuycontents.cpp:292`) | The object's sale price, in the message per object as `SalePrice`, with `SaleType` original, copy or contents. The viewer's note says it is "used for verification only, if it doesn't match region info then sale is canceled" (`llselectmgr.cpp:5012-5014`). Paid to the object's owner. |
| `BuyObjectInventory` | Not sent by the viewer: the name is in `llmessage/message_prehash.cpp:701` and the template (`scripts/messages/message_template.msg:2254`) and nowhere else | An item in an object, at a price the message does not carry. |
| `ParcelBuy` | `LLViewerParcelMgr::sendParcelBuy`, `newview/llviewerparcelmgr.cpp:1274-1311` | `Price` in the message, with `Area` and the parcel's `LocalID`. Paid to the parcel's owner. |
| `ParcelClaim` | The same function, for a claim (`llviewerparcelmgr.cpp:1278`) | The message carries a rectangle and no price. |
| `ParcelBuyPass` | `LLViewerParcelMgr::buyPass`, `llviewerparcelmgr.cpp:2699-2712` | The parcel's pass price, from its `ParcelProperties`; the message carries the parcel's `LocalID` alone. |
| `JoinGroupRequest` | `LLGroupMgr::sendGroupMemberJoin`, `newview/llgroupmgr.cpp:1870-1882`, after the viewer has compared the group's `MembershipFee` with the balance (`llgroupactions.cpp:325-348`) | The group's fee, from its profile (`GroupProfileReply`); the message carries the group alone. |
| `ImprovedInstantMessage`, dialog 35 | Accepting a group invitation, `llviewermessage.cpp:736-750` | The fee the invitation quoted in its binary bucket (`llimprocessing.cpp:1502-1518`); the answer carries no figure. |
| `AcceptGroupInvite` capability | The same, for an invitation that arrived while logged out (`llviewermessage.cpp:647-680`, `710-727`) | The same. The request names the group and nothing else. |
| `CreateGroupRequest` | `LLGroupMgr::sendCreateGroupRequest`, `llgroupmgr.cpp:1766-1790`, from `llpanelgroupcreate.cpp:265` | The fee for creating a group, which is not in the message (it carries the new group's own membership fee). |
| `ClassifiedInfoUpdate` | `sendClassifiedInfoUpdate`, `llavatarpropertiesprocessor.cpp:754-780`, with `llpanelprofileclassifieds.cpp:1384` | `PriceForListing`, in the message. Whether an update of an existing classified is charged again is not known from the source, so the price in the message counts either way. |
| `ScriptAnswerYes` | `llviewermessage.cpp:7276-7282` | Nothing at once. With `PERMISSION_DEBIT` in `Questions` (`ScriptTakeMoney`, `0x1 << 1`, `llscriptruntimeperms.h`) a script may take any amount from this avatar, to anyone, at any time afterwards, which is what a spend without a price is. |

Ruled out: `ObjectSaleInfo`, `ParcelPropertiesUpdate`, `UpdateInventoryItem`,
`UpdateTaskInventory`, `UpdateGroupInfo` and the `Rez` messages carry a
price, but it is the price of something this avatar sells or an item
described, and nothing is spent by sending it. `DirLandQuery` has a price
to search by. `RegionInfo` and `GodUpdateRegionInfo` carry the estate's
land price per metre. `TestEveryMessageThatCarriesAPriceIsCheckedOrListed`
in `internal/pay` walks the whole template so that a message added to it
with a field that looks like L$ is either checked or listed there with
why.

Left alone, by decision: an upload. The viewer takes its fee with a
`MoneyTransferRequest` of type `TRANS_UPLOAD_CHARGE` (1101) to nobody
(`llviewermenufile.cpp:1404-1420`, `fsfloaterimport.cpp:1580`), which a
client would send as a payment and which is checked as one; `slsh`'s `put`
uploads by the `NewFileAgentInventory` capability instead, and the grid
charges the fee there. That capability is not checked, and neither is any
other capability but `AcceptGroupInvite`. The viewer's other route to
L$, buying them with money, goes through the web and no message here.

## How each is checked

By what the message says and what the session was told. Nothing is asked
of the grid but a name, and a price or a payee the session has not heard
is not known, and is refused rather than passed as nothing:

- `ObjectBuy`: per object, the owner and price the region last said
  (`ObjectProperties`, `ObjectPropertiesFamily`, and an update that
  carries the owner), by the object's local id. The higher of the
  message's price and the region's is what counts. An object the session
  has not been told of, or whose owner it does not know, is refused, as
  is a sale type that is none of original, copy and contents.
- `ParcelBuy`, `ParcelBuyPass`: only the parcel this avatar stands on is
  known, by its `LocalID`, from the `ParcelProperties` the region pushes
  on arrival; the region's other parcels are not described to a session
  unasked. Any other is refused. The higher of the message's price and
  the parcel's counts, and the owner is paid.
- `JoinGroupRequest`, an accepted invitation, `AcceptGroupInvite`: the
  group is paid, so it must be in `pay_to` by key. What joining costs is
  the higher of the group's profile fee and the invitation's, either
  being known only if it was heard: a `GroupProfileReply`, or an
  invitation of the shape the viewer reads. Neither is a promise. The
  grid charges the group's fee as it is when the join arrives, so a group
  that raised it after the profile or the invitation was sent costs more
  than was counted. That is inferred from `sl/invite.go`, not measured.
- `ClassifiedInfoUpdate`: the message's price, paid to the grid.
- `BuyObjectInventory`, `ParcelClaim`, `CreateGroupRequest`,
  `ScriptAnswerYes` with the debit bit: refused always, saying why, since
  no price is known. Answering a script's other questions is not a
  purchase and is not looked at.

Every one is refused unless `pay = on`, except one known to cost
nothing: joining a group whose fee was last said to be L$0, or buying
something for sale at L$0, goes out with paying off and is not recorded
(the owner's decision, 2026-09-28). The grid charges a group's fee as it
is when the join arrives, so a fee changed in between would be charged
unchecked; that window was judged small next to refusing every free
group to every profile. A price nobody has said is still refused.

The transaction type a refusal reports is the viewer's: `TRANS_OBJECT_SALE`
5000 for an object, `TRANS_LAND_SALE` 5002 for a parcel,
`TRANS_LAND_PASS_SALE` 5006 for a pass, `TRANS_GROUP_JOIN` 1004,
`TRANS_GROUP_CREATE` 1002, `TRANS_CLASSIFIED_CHARGE` 1103,
`TRANS_LAND_CLAIM` 1001, `TRANS_INVENTORY_SALE` 5004
(`llinventory/lltransactiontypes.h`), and 0 for a permission.

## A log that is there before the first reply

slgod's log was set on a session after `StartAgent` had returned, and the
circuit is up before that: the grid can answer a payment as soon as the
avatar arrives, and the handler that logs the answer runs on the
dispatch goroutine then. So a `Log` written after the return raced the
read, under `-race`, and a reply that came first found none to write to.
The log is now given to the session when it is made -- `Server.SetLog`,
called before the first login, or `SetBase` -- and the avatar's own id
with it, so that the reply is recognised as this avatar's before the
session says which avatar it is. `TestASessionLogsFromTheFirstMessageItHears`
sends a `MoneyBalanceReply` the moment the avatar arrives and fails
for a log assigned afterwards. Nothing in slgod assigns `Hosted.Log`
after `StartAgent` now.

## slsh

`balance` asks every time. `pay NAME AMOUNT [REASON]` asks at the
prompt before it pays -- `pay Example Resident L$5? [y/N]` -- and pays
on `y` or `yes`. Where nobody is at a prompt -- `-c`, `-f`, a file run
with `.`, a session read from a pipe -- it refuses without `--yes`: the
next line of a script is not an answer to a question it never saw, and
a script that meant to pay says so on the line that pays.

A payment made to this avatar is printed as a line, like an instant
message, with the payer labelled as `Sender.Label` labels anybody.

Accepting a group invitation (`answer`, `accept`) is a purchase to the
rules, and is refused under a profile that does not say `pay = on`,
whether the group charges or not; a person at the shell who answered
with the fee still needs the profile to allow it. Through slgod the
refusal is a `MoneyBalanceReply` that `answer` does not wait for, so
what it prints is what it printed for an answer the grid ignored; `group`
says whether the join happened.

## slbotd

slbotd does not pay, and paying is not among what its model can ask
for. A daemon that answers strangers through a language model is the
last program that should be able to move money on its own, and nothing
it does needs to. `TestNothingHerePays` refuses a payment anywhere in
it. It does accept a group invitation a person tells it to, which is a
purchase to the rules and passes the same gate.
