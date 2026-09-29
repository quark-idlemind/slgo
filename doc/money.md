# Money

What the grid says about an avatar's L$, what paying looks like on the
wire, and the rules a profile puts on the programs that pay with it.
The comments in `agent/money.go`, `internal/pay` and `server/pay.go`
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
`newview/llstatusbar.cpp:1228-1231`).

For an object, `DestID` is the object and not its owner.

## The rules

A profile says what its avatar's programs may pay:

    pay       = on
    pay_max   = 10
    pay_daily = 10
    pay_to    = Example Resident
    pay_to    = 92f67e57-7e57-c0de-24de-53f27a898992

- `pay = on`, and paying is off without it.
- `pay_max`: the most one payment may be, L$10 when not given.
- `pay_daily`: the most all of them may come to in the last 24 hours,
  rolling, L$10 when not given.
- `pay_to`: an avatar that may be paid, by name or by key, any number
  of lines, and `pay_to = *` for anybody. None is nobody, so `pay = on`
  alone still pays nobody, and the refusal says to add `pay_to`.

The defaults are small on purpose. A program that pays is a program
that can empty an account by looping, and one that was only ever meant
to tip a few L$ should not need anybody to have thought about limits
before it is safe.

A name is asked of the grid, by the whole-name search a viewer's older
search makes (`AvatarPickerRequest`), and the key it answers with is
what is matched. A name of one word is taken as that word and
`Resident`, and a dot as the space it stands for. The answer is kept
for the life of the session, a name nobody has included. A name the
grid could not be asked about refuses the payment, and is asked about
again next time.

An object is paid only when its owner may be, and the owner is what the
region said in the object's properties. An object nobody has said an
owner for is refused.

All of this is in `internal/pay`.

## Where the rules are checked

In slgod, where it forwards a client's messages, and not only in `sl`.
A client's message is a body the daemon frames and forwards, so any
client with the secret can send its own `MoneyTransferRequest`,
whatever it was written with; a check in `sl` alone would hold only the
programs that chose to use it. `sendMessage` decodes that one message
-- the only body the server reads -- checks it, and sends it only if the
rules let it through. The unary `Send` is checked the same way.

A viewer handed a session through slgod is not checked. That is a person
using the viewer's own pay dialog, with its own confirmation, and the
rules are about programs. Nothing needs doing to exempt it: a viewer's
messages go down its own circuit, straight to the session, and never
pass through `sendMessage`.

Every payment let through and every refusal is logged by slgod: who
asked, whom it was for, how much, and why it was refused or which
`pay_to` line let it through. What the grid then says of each payment
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
`from_client` is an echo of something a client sent.

The unary `Send` has no stream to answer on, so its refusal is its
error, `PERMISSION_DENIED` with the reason.

## The daily total

A record of every payment let through -- when, how much, and to whom --
kept one file per profile, `pay-PROFILE`, mode 600, in slgod's own
directory (`~/.config/slgod`, or the `-config` directory), beside the
seats. A payment is dropped from it a day after it was made. It is read
again before every payment and written whole after one, so the total
outlives a restart of slgod.

It counts what was let through, not what the grid confirmed. A payment
the grid then refused still counts until it is a day old, and so does
one whose answer was lost. Erring that way pays less than the limit,
never more.

A record that cannot be read stops the profile paying until somebody
looks, rather than count from nothing.

## What is not checked

Only `MoneyTransferRequest`. Other messages spend L$ too -- buying an
object that is for sale, buying land, joining a group with a fee -- and
none of them is checked here. That is inferred from what the messages
are for, not measured.

