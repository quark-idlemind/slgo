# slgo on Windows

slgo builds and runs on Windows (amd64), and its tests pass there with
the exceptions listed at the end. This page is how that was done and
what was found on the way. It was measured on 2026-11-12 on Windows 11
Pro (build 26200) with Go 1.27.4, the tree copied across rather than
cloned.

## Building

Install Go from go.dev, then from the top of the tree:

    go build -o bin\ ./cmd/...

The commands are the same as everywhere else. Profiles and the daemon's
files live where they do on Unix, under the home directory:
`%USERPROFILE%\.config\slgo`, `%USERPROFILE%\.config\slgod`, and the
rest. `$HOME` is honored first on every platform, so a shell from Git
for Windows, which sets it, finds the same files. `XDG_CONFIG_HOME`,
`SLGO_CONFIG_DIR` and `-config` work as they do anywhere.

Windows has neither `umask` nor `openssl`, so where slgod says how to
make the shared secret it gives a PowerShell line instead.

## Smart App Control

On the machine this was measured on, Smart App Control was on, and it
refused to run every program built there: slgod.exe, slsh.exe, slhash.exe,
the test binaries `go test` builds, and once Go's own `link.exe`. Each
was refused as "An Application Control policy has blocked this file",
and the Code Integrity event log named Smart App Control as the reason.
A slgod.exe built on a Mac and copied across was refused too; a copied
slsh.exe was let through.

By Microsoft's account of it, not tested here, Smart App Control lets a
program run on its reputation or on a signature from a certificate
authority Microsoft trusts, and has no exception for one file. It was turned off on that machine to do this work. On some builds
of Windows 11 turning it off cannot be undone without reinstalling.

So a slgo built from source, or handed over unsigned, will not run on a
machine that has it on. Signing the binaries is the fix; running the
Linux build under WSL is the workaround. Neither has been tried here.

## File permissions

On Unix a profile, the shared secret, the machine identity and the logs
are refused if their mode lets anybody but the owner in. On Windows Go
makes the mode bits up -- 0666 for a file, 0444 for a read-only one,
0777 for a directory -- from the read-only attribute alone, and who can
read a file is its access control list, which the bits do not show.
Every such check refused everything there.

`internal/private` is where the checks ask, and on Windows it reports
nothing as open. A file under the profile directory is private to its
user by that directory's access control list, which is the default.
The tests that check a mode skip that check on Windows.

## The clock

Go's `time.Now` on that machine moved in steps of about half a
millisecond: 20 steps in a row measured 509 to 565 µs apart, with about
two million reads in between that repeated the last value. On macOS and
Linux it moves every nanosecond or so.

Wherever two things were told apart by when they happened, that was
wrong on Windows, because the fake grid in the tests answers inside
half a millisecond (whether the real one ever does was not measured):

- Offers, group invitations, lures and teleport requests came back in a
  different order each time they were asked for: they are kept in maps
  and sorted by their time, and two heard together had the same time.
  They are sorted by time and then by the order they were noted
  (`byArrival` in `sl/arrival.go`). The shells number them by this
  order, and in one Windows run slbotd's test of `:accept 2` answered
  the first offer instead of the second.
- Asking an object's name again waited for a name stamped after the
  asking. An answer stamped in the same step was taken for the old
  name, and the lookup waited four quiet seconds before selecting the
  root as well. Names are now numbered as they arrive and compared by
  number.
- slate sorted what it heard to one side of a step's start by its
  stamp, so an answer to the stimulus could read as having come before
  it: "becomes looping" never saw the sound start. A step's start and
  end are now taken by `boundary` (`slate/clock.go`), which waits for
  the clock to move on before the stimulus is sent.
- A refusal from slgod at the end of a bounded wait for slots is told
  from an early one by whether the deadline has passed, and allows for
  one step of the clock (`clockGrain` in `client/slots.go`).

## One daemon to a configuration

slgod locks its configuration directory so that a second instance
cannot log the same avatars in again. On Unix that is `flock`; on
Windows it is `LockFileEx`, on a byte far past the end of the lock
file, because Windows does not let anybody else read a locked range
and the next instance reads the holder's pid from the start of it.

## What does not run on Windows

- The pseudo-terminal tests of slsh: goterm has no pseudo-terminal on
  Windows. slsh's own terminal handling is not covered there, and a
  console window that changes size is noticed by asking its size four
  times a second, since Windows sends no signal.
- The tests that fake sl-host, or run `true` and `false`, with a shell
  script.
- The tests that interrupt the program they test by sending SIGINT or
  SIGTERM to their own process, which Windows cannot do. slgod's tests
  stop the daemon by canceling the context it waits on instead.

## Against the grid

With the owner's beta-grid avatar (`qi-beta`), slgod built on Windows
logged in, served over TLS with the shared secret, and `slsh -c where`,
`who` and `agents` answered through it; `slsh --logout` logged it out.
