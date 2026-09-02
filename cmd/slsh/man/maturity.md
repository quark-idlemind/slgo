Asks for the highest rating of land this avatar should be shown --
general, moderate or adult -- and prints what the grid granted, which
is not always what was asked for.

    maturity adult
    maturity moderate
    maturity general

The letters the protocol uses are taken as well, so `maturity a` and
`maturity pg` mean the same as the words.  "Mature" is read as
moderate, which is what that rating used to be called.

## Two numbers wear this name

An account carries both of these, and only the first is anybody's to
set:

- **The preference.**  The highest rating this avatar has asked to be
  shown.  That is what this command sets, and it is the same setting a
  viewer's maturity menu changes.
- **The ceiling.**  The highest the account is *permitted* to ask for.
  It comes from age verification, it is `agent_access_max` in the login
  response, and no client can raise it -- there is no message for it,
  and Linden Lab's own viewer offers only what the account already
  allows and sends you to the website for the rest.

So this asks, and the grid answers with what it granted.  Where they
agree there is nothing more to say, and this is what was measured on
Agni on 2026-09-02:

    /$ maturity adult
    this avatar is shown land rated adult and below

Where they differ, the answer is the ceiling, and the line says so.
That case has NOT been seen here -- both accounts this was run against
were granted what they asked for -- so what follows is what the
command would print and not a transcript:

    /$ maturity adult
    asked for adult and was granted moderate
    moderate is as high as this account may go, which is age verification
    rather than a setting: no client can raise it, and it is changed on the
    account page and nowhere else

Being granted what was asked for does not mean anything changed:
asking for a preference already in force is answered the same way.

## Why you would want it

An avatar refused entry to a region is told this, on the teleport:

    RegionTPAccessBlocked: "You aren't allowed in that Region due to your
    maturity Rating. You may need to validate your age and/or install the
    latest Viewer. Please go to the Knowledge Base for details on accessing
    areas with this maturity Rating."

That sentence names both causes at once and picks neither, so there is
no telling from it whether the preference is too low or the account is
not verified.  This command tells them apart in one call: ask for
adult, and what comes back says which of the two you are looking at.

Measured on Agni on 2026-09-02, in that order and on one avatar:

    /$ tp Nightmire Sea 128 128 25
    teleporting to Nightmire Sea at 128, 128, 25
    slsh: tp: sl: the grid refused the teleport: ... RegionTPAccessBlocked:
    "You aren't allowed in that Region due to your maturity Rating. ..."
    /$ maturity adult
    this avatar is shown land rated adult and below
    /$ tp Nightmire Sea 128 128 25
    teleporting to Nightmire Sea at 128, 128, 25
    Nightmire Sea at 128, 128, 26

So the block was the preference and not the account, and one command
cleared it.  A second avatar on the same grid walked into that region
throughout, which is what made the difference visible in the first
place.

A headless login starts with whatever the account already had, and
these avatars are usually set up through a viewer once and never again
-- so a preference somebody set years ago on one account and never on
another is the ordinary reason two avatars behave differently on the
same land.

## It cannot be read without asking

There is no listing form.  The preference arrives in the login
response, which the daemon holds and does not pass on, and the
capability this uses has only one question -- "set it to this".  A bare
`maturity` would therefore have to ask for something in order to report
anything, and the only rating it could safely ask for would be general,
which would quietly *lower* a preference somebody had set higher.  So a
bare `maturity` is refused with this explanation instead.

Asking for the rating you believe is already in force is the way to
read it: it changes nothing and the answer is the truth.

## The rating is per account, not per session

It is remembered by the grid and outlives the session, so setting it
once is setting it for good -- an avatar logged out and back in is
still shown what it was last granted.  It is not part of the profile
this shell keeps and it is not affected by `logout`, `login`, or the
daemon being restarted.

See also: `regions` and `look`, which print the maturity rating of land
rather than of an avatar, and `tp` for the refusal that sends most
people here.
