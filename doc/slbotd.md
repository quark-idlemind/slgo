# slbotd, and why it does what it does

The comments in `cmd/slbotd/` say what the daemon does. This page is
why, where the why is a story: what was measured, what the code did
before, and what was wrong with it.

What each of these looks like to somebody running slbotd is in
[doc/guide.md](guide.md), under "slbotd".

## Keeping the kv cache

slbotd keeps a conversation as text, and its kv cache as a state file
on llama-server's side; a conversation that has lost its slot is picked
up again from that file. Restoring a 1457 token conversation took
5.4 ms and saved 5.0 s of prompt processing, and that is the whole
argument for doing this at all.

Against llama-server b11056 on the machine this was written on,
Qwen2.5-0.5B-Instruct Q4_K_M, four slots of 2048 tokens, on an Intel i9
with no gpu offload:

    a 1442 token prompt, cold          5.0 s   (288 tokens/second)
    saving those 1457 tokens          13.1 ms  (17,927,560 bytes)
    restoring them into another slot   5.4 ms
    the next turn, warm                0.53 s  (1442 of 1461 cached)

So restoring costs about five milliseconds and saves about five
seconds, and the cost of carrying a conversation is the disk it sits
on rather than the time to pick it up. That measurement is what the
design rests on; it was taken rather than assumed, and it is the one to
repeat on the machine this ends up running on.

The kv cache is 12,304 bytes a token for that model, which is
2 * 24 layers * 2 kv heads * 64 head dim * 2 bytes plus a header. The
shape matters more than the size of the model: grouped query attention
decides it, so a three billion parameter model with two kv heads costs
less per token than a smaller one with eight.

## The sentence that makes a pause show

`Medium`, what every avatar is told about the channel it is talking
through, ends: "If you are told how long it has been since they last
wrote, remark on it." That third sentence is the price of the gap hint
working at all, and it was measured rather than assumed. Given the
elapsed time and nothing else, the model class it was tried on
mentioned it in NONE of thirty replies -- three placements, ten each.
Asked outright how long it had been it answered from the hint, so the
number IS reaching it and it simply will not volunteer it.

With the sentence in front, about half of twenty-five replies remarked
on a three-day absence in their own words ("Three days, that's a
while"), and three different wordings of the instruction did not
differ enough to choose between. Half, not all: this is an improvement
on inventing a duration, not a guarantee, and about one reply in
twenty-five contradicted the hint outright. A better model is the fix
for that, not a longer sentence in `Medium`.

## Why the note is three labelled lines

`Summarise` asks for three labelled lines because asking in prose does
not work, at any size worth running. The first version of it said
"write a brief note, in the third person, of what has passed between
them" and it was measured against both models to hand: the 0.5B
answered with fragments of the transcript separated by rules, and the
3B answered by copying the exchange back verbatim. Neither summarised
anything, and the name the person had given -- the single most useful
fact in the conversation -- was lost by both.

Three labelled lines with a rule for the empty case work on the 3B
first time and keep exactly what is worth keeping. Measured on the
same exchange:

    THEM: Quark, chandlery, upriver
    TOPICS: tide, berth, rope
    OWED: Hold three coils of rope until Thursday

And they survive being folded again, which is the property the whole
thing rests on: fed that note plus four more turns, the next fold kept
the name, the trade and the promise, and added the new topics to the
middle line. A prose note has nothing to hold on to and drifts; a
labelled one has three places to put things and keeps them.

## Two models talking to each other

Two avatars this daemon drives may talk to each other, and a flat
refusal was the first answer to that and the wrong one. Two of these
talking is not a malfunction -- what actually happened was a dull but
perfectly ordinary conversation -- and an operator who wants them not
to has a way to say so now, by writing the name with a "!" in front of
it. What is wrong with it is only that it does not stop: a reply from
one is an ordinary remark to the other, neither is answering ITSELF,
and neither will ever be the one to get bored.

So it is bounded rather than banned. They may say a few things to each
other and then one of them stops answering, which is what ends it --
there is no other end available, since the far side is as tireless as
this one.

Measured before there was any bound: with "chat = *" on three avatars,
ONE message typed by hand from one of them to another ran to 26
exchanges in ninety seconds on the live grid, and was still going when
it was stopped by hand. Starting it took a person; stopping it was
never going to happen on its own.

## Half a chat configuration

A chat configuration with half its lines -- `chat` with no `llm-url`,
or the other way about -- is almost certainly an unfinished one, and it
used to be fatal. That was wrong, and it was wrong in the way that
matters for a daemon meant to run for weeks: attending avatars and
taking commands is what slbotd is FOR, chat is something bolted on
beside it, and a missing line in the bolted-on part took the whole
thing down on the next restart. slgod has had the right answer to this
all along -- a profile it cannot read is logged and the others are
served -- and this is the same rule.

So it is said, loudly, every time the daemon starts and again whenever
anybody asks; and the avatars are attended. Silence was never the
alternative: a setting that does nothing and says nothing is the fault
this used to be trying to prevent.
