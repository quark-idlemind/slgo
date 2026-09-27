# What a profile says about somebody

`sl.Profile` asks the grid what an avatar's profile says. The comments
in `sl/profile.go` say what the code does. This page is why: what the
viewer's source says, and what was measured on Agni.

## Why this takes the older road

The modern viewer does not ask for any of this over the circuit. For
`APT_PROPERTIES` it reads an `AgentProfile` capability over HTTP, and
when that capability is missing it sends nothing at all rather than
falling back -- "Don't sent UDP request for APT_PROPERTIES", and a
warning saying so (`llavatarpropertiesprocessor.cpp:118-131`).

That road is shut here. slgod does not ask the seed capability for
`AgentProfile` -- it is not in `agent.DefaultCaps` -- so there is no URL
to fetch, and opening one is a piece of work of its own: another
capability to request, another body to parse, and an answer whose shape
nothing here has measured. A profile that arrives over three template
messages needs none of that.

So `Profile` takes the road the same viewer still takes for
`APT_PROPERTIES_LEGACY`, which is an `AvatarPropertiesRequest` carrying
the agent, the session and the avatar asked about and nothing else
(`llavatarpropertiesprocessor.cpp:134-138`, and
`sendAvatarPropertiesRequestMessage` at `:162-174`). It is the older
road in both senses: the enum calls it legacy and warns that it
"Truncates data!!!" (`llavatarpropertiesprocessor.h:54`), and the
truncation is real -- the about text is capped at 512 bytes here
(`message_template.msg:3924`) where the capability's is not. A profile
read this way is what the grid will tell a viewer of a few years ago,
which is a great deal more than nothing at all.

## One request, three answers, and the order they arrive in

The simulator answers unprompted with three messages, and the viewer
says so in a comment over each handler it wrote for them:
"AvatarGroupsReply is automatically sent by the server in response to
the AvatarPropertiesRequest in addition to the AvatarPropertiesReply
message" (`llavatarpropertiesprocessor.cpp:616-620`), and the same
sentence over the interests handler (`:467-472`).

Measured on Agni, asking about hobb as qi:

	16:38:20.269  AvatarGroupsReply
	16:38:20.290  AvatarPropertiesReply
	16:38:20.291  AvatarInterestsReply

The groups arrive first, twenty milliseconds ahead of the properties
everything else hangs off. So a call that waited for the properties and
only then started listening for groups would lose them every time --
and lose them silently, since an avatar with no groups to show is an
ordinary thing. `Profile` subscribes before it sends, which is what
`ScriptRunning` and `Run` do and for the same reason.

## An empty answer is a row, not an absence

An avatar with no groups to show still sends one row, of all zeros.
Measured against an avatar who lists none:

	GROUP 00000000-0000-0000-0000-000000000000 "" title "" powers 0x0

A zero group id is not a group, and a caller handed one would print it
as a group with no name, so rows with a zero id are dropped.

## How the grid says it has never heard of a key

It does not say. Measured against a key made up on the spot: the groups
reply arrives, carrying the one empty row, and the properties reply
never comes -- not late, not ever. That silence is the whole of the
answer, so the deadline in `Profile` is not an error path: it is how the
call learns the thing it was asked, and `Profile.Known` is where it says
so. An error is kept for the case that really is one, which is nothing
arriving at all -- no groups reply either -- because that is a question
that went unasked or unanswered rather than an avatar that is not
there.

The avatar need not be anywhere near. Measured against one elsewhere on
the grid entirely, which answered in full: this is a question to the
grid rather than to the region, so nothing needs the avatar to be in the
interest list, or in the region, or logged in.

## The groups are what somebody publishes, not what they belong to

A group appears in a profile only if the avatar has chosen to list it
there -- `mListInProfile`, which starts false (`llgroupmgr.cpp:241`) and
is the "show in my profile" tick beside each group
(`llpanelgroupgeneral.cpp:194`). So this list is smaller than the truth
by however much its owner wanted, and it can disagree with the
membership list the daemon holds for the same avatar, which comes from
`AgentGroupDataUpdate` and is every group they are in. Neither is wrong.
They answer different questions, and a caller showing this one should
say which question it answered.

The reply has no room to say it per group either: `ListInProfile` is
one BOOL in a Single block beside a Variable list of groups
(`message_template.msg:3970-3973`), so it cannot describe a row. The
filtering has already happened at the far end, which is why every row
that arrives is one that was listed.

## What is deliberately not read

`ImageID` and `FLImageID` are textures. This client can fetch a texture
and cannot show one, and a profile picture reduced to a key is a key.
`FLAboutText` is the first-life half of a profile the current viewer no
longer has a tab for. Neither is hard to add if anybody wants them;
they are left out because nothing would read them.
