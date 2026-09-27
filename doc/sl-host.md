# sl-host, and why it does what it does

The comment at the head of `cmd/sl-host/main.go` says what the command
does: the rules, the ports, the profiles and the exit status. This page
is the history behind it.

## The script it replaced

This was a ksh script, and that is where it broke: it collected the
addresses one a line and handed the lot to awk as a single -v string,
which awk refuses when it holds a newline -- so on any machine with two
addresses no rule was ever tried, and the caller was left with an empty
host. The same script also required a network to be four dotted octets,
so "0/0" was rejected as a bad network. The Go command keeps the
addresses a list from start to finish, and reads the short forms.
