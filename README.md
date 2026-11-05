# slgo

slgo lets you use Second Life from a terminal, or from your own Go
programs, without running a viewer.

There are three main parts. `slsh` is a shell: you log in, look around,
move through your inventory as if it were a filesystem, and chat.
`slgod` is a daemon that keeps avatars logged in, so slsh, a bot or a
script can attach and detach without logging in each time. And `sl` is
the Go package everything else is built on, which your own programs
can use too.

It's pure Go, and runs on Linux and macOS. Windows isn't
supported. You'll need Go 1.26.1 or newer.

```
$ git clone https://github.com/quark-idlemind/slgo.git
$ cd slgo
```

`go install` puts programs in `~/go/bin` unless you've told it
otherwise, so make sure that's on your `PATH`.

## Logging in with slsh

```
$ go install ./cmd/slsh
$ slsh --direct
First name: Example
Last name: Resident
Password for Example Resident:
logging in as Example Resident...
slsh: Example Resident in Example Landing, logged in directly -- quitting logs out
      help for commands, chat to talk, ESC to come back
```

Then try a few commands:

```
/$ where
Example Landing at 112, 64, 23
/$ who
nobody else is in range
/$ cd Objects
/Objects$ ls -l
object     2026-03-14T10:22:41 3ae67e57-7e57-c0de-c0d9-924bdff0fe21  MCX /Objects/object
/Objects$ chat
Local> hello
23:24:54 > [Local] hello
```

`help` lists everything slsh can do, and `man` followed by a command
name gives the full page for it. `quit` or `exit` logs out.

There's much more in the handbook, which is a set of web pages in the
`handbook` directory. On macOS you can open it with:

```
$ open handbook/index.html
```

and on most Linux desktops with:

```
$ xdg-open handbook/index.html
```

## Keeping an avatar logged in with slgod

`--direct` is fine for a quick look, but every start is a fresh login
and quitting logs you out. slgod holds the login for you. slsh attaches
to it straight away, and when you quit, the avatar stays where it is.

slgod needs a profile for each avatar and a shared secret, which it and
its clients use to recognise each other:

```
$ go install ./cmd/slgod ./cmd/slhash
$ (umask 077; mkdir -p ~/.config/slgo ~/.config/slgod)
$ (umask 077; printf 'first = Example\nlast = Resident\nstart = last\npassword = %s\n' "`slhash`" > ~/.config/slgo/example)
$ (umask 077; openssl rand -hex 32 > ~/.config/slgod/secret)
```

The profile is called `example`, after its file name. `slhash` asks
for the password and prints an MD5 hash of it, which is what goes in
the file. Treat the hash as you would your password, because as far as
Second Life is concerned it is your password. Never let anyone but you
read the files in `~/.config/slgo`; slgod won't load a profile if they
can.

Start slgod and leave it running:

```
$ slgod -listen 127.0.0.1:7807 example
```

When it prints `serving gRPC on 127.0.0.1:7807`, run `slsh`, this time
without `--direct`, in another terminal.

The handbook's [Everything on one
machine](handbook/quickstart-local.html) goes through the same steps
in more detail, including what to do when one of them fails.

## Running slgod on another machine

A common setup is slgod on a server that's always on, and slsh on your
laptop. The password stays on the server, and the laptop only needs
slsh and a copy of the secret. Build both from the same commit, because
slgod turns away a client that's older than it is.

Copy the secret across with scp:

```
$ (umask 077; mkdir -p ~/.config/slgod)
$ scp server.example:.config/slgod/secret ~/.config/slgod/secret
```

Be careful with that file. The connection is encrypted, and each end
checks that the other has the secret without ever sending it, but
anyone who gets a copy can drive every avatar your slgod holds. Don't
paste it into chat or email, and don't open port 7807 to the internet.
slgod and slsh refuse to use a secret that other users on the machine
can read.

The easiest safe setup is to have slgod listen only on the server's
loopback address, and reach it through an SSH tunnel:

```
server$ slgod -listen 127.0.0.1:7807 example
laptop$ ssh -N -L 7807:127.0.0.1:7807 you@server.example
laptop$ slsh
```

If you'd rather connect directly over your LAN or a VPN, start slgod
with `-listen` on that address and tell slsh where it is, either with
`--addr` each time or once in `~/.config/slsh/config`:

```
addr = server.example:7807
```

slsh tries `--addr` first, then `addr =` in its config file, then
`sl-host` if you've installed it, and finally `localhost:7807`. sl-host
is for a laptop that moves between networks: it picks slgod's address
based on the network you're on. If slgod holds more than one avatar,
choose one with `--agent`.

[slgod on a server](handbook/quickstart-remote.html) has the rest,
including firewall rules and what the error messages mean.

## Letting an AI agent drive

Because `slsh -c` runs one command and exits, and slgod keeps the
avatar logged in between commands, an agent that can run shell
commands, such as Claude Code, can drive an avatar too: look around,
move things, read chat, answer people. Some of the measurements in
`doc/` were taken that way.

## Writing your own program

```
$ go get github.com/quark-idlemind/slgo
```

This connects to slgod, prints where the avatar is and who's nearby,
says hello, and then prints local chat until you stop it:

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/quark-idlemind/slgo/sl"
)

func main() {
	ctx := context.Background()
	s, err := sl.Dial(ctx, "localhost:7807", "example")
	if err != nil {
		log.Fatal(err)
	}
	defer s.Close()

	where, err := s.Where(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("in", where.Region)

	people, err := s.Nearby(ctx)
	if err != nil {
		log.Fatal(err)
	}
	for _, p := range people {
		fmt.Printf("%s, %.0f m\n", p.Name, p.Distance)
	}

	if err := s.Say(ctx, "hello", 0); err != nil {
		log.Fatal(err)
	}
	for l := range s.Chat(sl.ChatFilter{}, 16) {
		fmt.Println(l.Sender().Label(l.From)+":", l.Text)
	}
}
```

It reads the same secret file slsh does. To log in from the program
itself, with no slgod, use `sl.LoginDirect` in place of `sl.Dial`.

For something bigger, look at [`examples/greeter`](examples/greeter),
a complete bot that greets visitors and hands out a gift. The
handbook's [Your own bot](handbook/own-bot.html) builds it up a step at
a time. [Go packages](handbook/api.html) and `go doc ./sl` cover the
API.

Most programs only need `sl`. Under it are `agent`, which is one
avatar's connection to the grid, `client`, which talks to slgod over
gRPC, `msg`, which has every UDP message in Linden Lab's template as a
Go struct, and `llsd`.

## The other programs

- `slbotd` runs avatars as bots. They take commands by IM, and with a
  local language model they can hold a conversation.
- `slrun` runs LSL scripts in-world and prints what they say, and
  `slbench` measures how much memory a piece of LSL uses.
- `slate` runs Slate test files against things you've built.
- `slpic` saves the pictures on an object's faces, and can find a label
  or a shape in one.
- `sl-host` works out where slgod is, for a laptop that moves between
  networks.
- `slhash` makes the password hash that goes in a profile.
- `msggen` regenerates the `msg` package from Linden Lab's message
  template.

## Documentation

Start with the [handbook](handbook/index.html) for setting things up,
whether on one machine or several, with or without bots. It also has
the [slsh guide](handbook/slsh-guide.html) and a
[troubleshooting](handbook/troubleshooting.html) page.

`doc/` is about how things work and why, and most of it comes from
testing on the grid. [doc/overview.md](doc/overview.md) is the long
version of this README, the way it used to be. `doc/history/` has the plans written before each
piece was built. Some of them are out of date, so where they disagree
with the code, the code is right.

## Stability

slgo is at 0.9, so things can still change. From 1.0 on, `sl`, `agent`,
`msg`, `llsd`, `client` and the two `proto` packages will follow the
usual Go rules, and code that builds against one 1.x release will keep
building against later ones. There are a few exceptions:

- `sl.Backend` may gain methods. If you write your own, embed
  `sl.UnimplementedBackend` and it will keep compiling.
- The `msg` structs gain fields when Linden Lab's template does, so use
  field names in struct literals.
- Tuning values such as `DefaultTeleportTimeout` may change. Use the
  names, not the numbers.
- In the `proto` packages, a field number is never reused and a field
  never changes type.

None of this covers `internal/`, Slate, `imgfind`, or the commands'
flags and output.

## Working on slgo

```
$ go generate ./msg
$ go test ./...
```

`go generate` downloads Linden Lab's message template from the viewer
and regenerates `msg`. The template itself isn't kept in this
repository.

A lot of slgo was tested on the live grid, on and near other people's
land. Please don't commit anything that names real residents, regions,
parcels or objects. [CLAUDE.md](CLAUDE.md) explains how to make up
names and ids for tests and examples, and
`tools/check-identities --install-hook` adds a check that runs before
each commit. Changes go in by pull request.

## Known gaps

- Walking across a region border works when `neighbours` is on, but
  there's a pause. slgo connects to the new region from scratch instead
  of reusing the connection it already has open to it.
- Nothing from neighbouring regions is kept, so you can't see objects
  or terrain across a border.
- `UploadAsset` accepts sounds, animations and meshes, but so far
  nothing here uploads anything except textures.

## History

slgo began as a Go rewrite of a C client I wrote in the early 2010s,
which isn't part of this repository. Most of the Go was written by AI
coding agents, with a lot of direction from me. slrun and slbench are
based on Go programs I wrote in 2026.

## Licence

Apache 2.0. The full text is in [LICENSE](LICENSE).

Second Life belongs to Linden Lab. slgo is an independent client, and
isn't affiliated with or endorsed by them. Their
`message_template.msg` isn't included here; `msg/messages_gen.go` is
generated from it.
