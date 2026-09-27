# Maturity: what an avatar asks to be shown

`sl.SetMaturity` asks for the highest rating of land an avatar should
be shown, and the grid answers with what it granted. The head of
`sl/maturity.go` says what the two numbers are -- the preference, which
a client sets, and the ceiling, which it cannot move. This page is what
was measured of them.

## What being refused looks like from outside

Measured on Agni on 2026-09-02, two avatars a moment apart, both to the
same public region on the adult continent. One arrived. The other was
refused, on the teleport rather than at the setting:

	RegionTPAccessBlocked: "You aren't allowed in that Region due to
	your maturity Rating. You may need to validate your age and/or
	install the latest Viewer. Please go to the Knowledge Base for
	details on accessing areas with this maturity Rating."

The grid's sentence names both causes at once and does not say which
applies, which is the whole reason `sl/maturity.go` distinguishes them.

The refused avatar was then asked for Adult through `SetMaturity` and
granted it, and the same teleport went through a moment later. So what
had blocked it was the preference and not the account -- which is the
answer the call exists to give, and it took one round trip where the
refusal alone could not have said it at all.

## A grant lower than the request

The other case, a grant lower than the request, has not been seen here:
both accounts this has run against were granted what they asked for.
It is what the capability is documented to do and what the ceiling
means, and `SetMaturity` handles it, but nothing here has watched it
happen.
