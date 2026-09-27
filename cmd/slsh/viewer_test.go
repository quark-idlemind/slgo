package main

// Where a real viewer would log in, and starting one.
//
// Nothing here starts a viewer.  What can be tested without one is the
// whole of what slsh decides: which command it would run, what it puts
// in it, what it refuses to do, and what it prints -- and the launch
// itself is one exec.Command away from that, run through a program that
// exits at once.
//
// The daemon end is a real gRPC server on loopback (newDaemonShell),
// because the endpoint and the credential both come across the wire and
// there is no way to fake a client.Conn.

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"

	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// aViewerStatus is a daemon serving viewer logins for an avatar.
func aViewerStatus(attached bool) *pb.StatusResponse {
	return &pb.StatusResponse{
		Agent:  &pb.AgentInfo{Name: "fake", AvatarName: "Quark Idlemind", Region: "Test Region"},
		Viewer: &pb.ViewerEndpoint{LoginUri: "http://127.0.0.1:9000/", Attached: attached},
	}
}

// aCredential is what a daemon answers a minting call with.
func aCredential() *pb.ViewerCredentialResponse {
	return &pb.ViewerCredentialResponse{
		LoginUri:      "http://127.0.0.1:9000/",
		First:         "Quark",
		Last:          "Idlemind",
		Password:      "0123456789abcdef",
		ExpirySeconds: 60,
	}
}

// TestViewerSaysWhereTheEndpointIsAndAsWhom: the address was a line in
// the daemon's log at startup and nowhere else, so a shell attached
// afterwards could not ask.  The avatar name is beside it because that
// is what a viewer's login box wants -- the profile name means nothing
// to a viewer.
func TestViewerSaysWhereTheEndpointIsAndAsWhom(t *testing.T) {
	x, d := newDaemonShell(t)
	d.status = aViewerStatus(false)

	got := x.do(t, "viewer")
	for _, want := range []string{
		"http://127.0.0.1:9000/",
		"Quark Idlemind",
		"no viewer has taken this session",
		"viewer --launch",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("viewer should say %q:\n%s", want, got)
		}
	}
	// Looking must not mint anything: a credential made by a command
	// that only reports would be a live password nobody asked for.
	if d.credentials != 0 {
		t.Errorf("plain viewer asked for %d credentials, want none", d.credentials)
	}
}

// TestViewerSaysWhenSomebodyIsAlreadyOnIt: two clients can hold one
// avatar, and "why is this session doing things I did not ask for" is
// answered by knowing a viewer took it.
func TestViewerSaysWhenSomebodyIsAlreadyOnIt(t *testing.T) {
	x, d := newDaemonShell(t)
	d.status = aViewerStatus(true)

	got := x.do(t, "viewer")
	if !strings.Contains(got, "a viewer has taken this session") {
		t.Errorf("viewer should say somebody is on it:\n%s", got)
	}
}

// TestNoViewerEndpointIsAnAnswerAndNotABlank: -viewer is not the
// default and the daemon here runs without it, so this is the ordinary
// case.  It has to say how to get one, because a blank field where an
// address goes is indistinguishable from a command that half worked.
func TestNoViewerEndpointIsAnAnswerAndNotABlank(t *testing.T) {
	x, d := newDaemonShell(t)
	d.status = &pb.StatusResponse{Agent: &pb.AgentInfo{Name: "fake", AvatarName: "Quark Idlemind"}}

	got := x.do(t, "viewer")
	if !strings.Contains(got, "-viewer") {
		t.Errorf("the answer should say which flag brings an endpoint up:\n%s", got)
	}
	if strings.Contains(got, "logins at") {
		t.Errorf("a daemon with no endpoint should not be reported as having one:\n%s", got)
	}
}

// TestTheLaunchCommandIsTheOneThatWorkedLive: this exact command line
// was run twice against a real daemon and a real viewer, and the viewer
// took the handover both times.  It is written out here rather than
// assembled from the settings, because what is being protected is the
// command and not the templating: --loginuri in place of --grid, or the
// Second Life flavour of Firestorm in place of the OpenSim one, both
// end at an Agni login screen with nothing to say why.
func TestTheLaunchCommandIsTheOneThatWorkedLive(t *testing.T) {
	var cfg Config
	cfg.ViewerApp, cfg.ViewerGrid, cfg.ViewerLaunch, cfg.ViewerRunning = viewerDefaults("darwin")

	argv, err := viewerArgv(cfg, aCredential())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"open", "-a", "Firestorm-OpenSim", "--args",
		"--grid", "slgod",
		"--login", "Quark", "Idlemind", "0123456789abcdef",
	}
	if strings.Join(argv, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("argv =\n  %q\nwant\n  %q", argv, want)
	}
}

// TestAGridNobodyHasNamedIsRefusedBeforeTheViewerStarts: --grid takes a
// nickname out of the viewer's own grid list and there is no sensible
// default for an empty one -- a viewer launched without it comes up on
// whatever grid it used last, which is the live grid, and says nothing
// about why.  Better to refuse and name the setting.
func TestAGridNobodyHasNamedIsRefusedBeforeTheViewerStarts(t *testing.T) {
	var cfg Config
	cfg.ViewerApp, _, cfg.ViewerLaunch, cfg.ViewerRunning = viewerDefaults("darwin")

	_, err := viewerArgv(cfg, aCredential())
	if err == nil {
		t.Fatal("a launch with no grid named was allowed through")
	}
	if !strings.Contains(err.Error(), "viewer_grid") {
		t.Errorf("the refusal does not name the setting that fixes it: %v", err)
	}
}

// TestAFilledInValueIsOneArgumentWhateverIsInIt: the words are split
// before anything is substituted, so a value with a space in it stays
// one argument.  An avatar with a space in its first name is not
// possible on this grid, but an application called "My Viewer" is, and
// a password that split into two arguments would be a login failure
// nobody could explain.
func TestAFilledInValueIsOneArgumentWhateverIsInIt(t *testing.T) {
	cfg := Config{ViewerApp: "My Viewer", ViewerLaunch: "open -a {app} --args --login {first} {last} {password}"}
	c := aCredential()
	c.First = "Two Words"

	argv, err := viewerArgv(cfg, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(argv) != 8 {
		t.Fatalf("argv = %q, want eight words", argv)
	}
	if argv[2] != "My Viewer" || argv[5] != "Two Words" {
		t.Errorf("a value with a space became more than one argument: %q", argv)
	}
}

// TestAnUnknownPlaceholderIsRefused: a viewer handed a literal
// "{passwrd}" takes it for a password and fails with something about
// the login server, which is a long way from the typo that caused it.
func TestAnUnknownPlaceholderIsRefused(t *testing.T) {
	cfg := Config{ViewerApp: "Firestorm", ViewerLaunch: "open -a {app} --args --login {first} {last} {passwrd}"}

	_, err := viewerArgv(cfg, aCredential())
	if err == nil {
		t.Fatal("a misspelled placeholder was passed through to the viewer")
	}
	if !strings.Contains(err.Error(), "{password}") {
		t.Errorf("the refusal does not say what the names are: %v", err)
	}
}

// TestALaunchWithNothingToRunSaysSo: on a platform nobody has written a
// default for, guessing at a binary name would fail somewhere inside a
// viewer's own startup.  Saying that nobody has said what to run is a
// sentence somebody can act on.
func TestALaunchWithNothingToRunSaysSo(t *testing.T) {
	x, d := newDaemonShell(t)
	d.status = aViewerStatus(false)
	x.cfg.ViewerLaunch = ""

	got := x.do(t, "viewer --launch")
	if !strings.Contains(got, "viewer_launch") {
		t.Errorf("the refusal should name the setting that fixes it:\n%s", got)
	}
	// The placeholders it names are the ones the default line uses.
	_, _, launch, _ := viewerDefaults("darwin")
	named := regexp.MustCompile(`\{[a-z]+\}`)
	if said, want := named.FindAllString(got, -1), named.FindAllString(launch, -1); !slices.Equal(said, want) {
		t.Errorf("the refusal names %v, and the default line uses %v", said, want)
	}
	if d.credentials != 0 {
		t.Errorf("a launch that could not happen still minted %d credentials", d.credentials)
	}
}

// TestARunningViewerIsRefusedRatherThanRaised: "open -a" raises an
// application that is already running and does not pass it the
// arguments again, so a second launch brings the window to the front,
// logs nobody in, and looks exactly like success.  That is the one
// outcome worth refusing outright.
func TestARunningViewerIsRefusedRatherThanRaised(t *testing.T) {
	x, d := newDaemonShell(t)
	d.status = aViewerStatus(false)
	d.credential = aCredential()
	// true(1) stands in for a viewer that is up: the check is an exit
	// status, and nothing here should start a real one.
	x.cfg.ViewerApp = "Firestorm"
	x.cfg.ViewerRunning = "true"
	x.cfg.ViewerLaunch = "false --loginuri {uri} --login {first} {last} {password}"

	got := x.do(t, "viewer --launch")
	if !strings.Contains(got, "already running") {
		t.Errorf("a viewer that is already up should be said so:\n%s", got)
	}
	// Refused BEFORE minting: a credential made for a launch that
	// cannot happen is a live secret nobody asked for.
	if d.credentials != 0 {
		t.Errorf("a refused launch minted %d credentials", d.credentials)
	}
}

// TestALaunchRunsTheCommandAndKeepsThePasswordOutOfSight: the password
// reaches the viewer's argv, which is bad enough; printing it as well
// would leave it in a scrollback that outlives the minute it is good
// for.
func TestALaunchRunsTheCommandAndKeepsThePasswordOutOfSight(t *testing.T) {
	x, d := newDaemonShell(t)
	d.status = aViewerStatus(false)
	d.credential = aCredential()
	// true(1) rather than a viewer: what is being tested is that the
	// command is built, run and reported, not what a viewer does with
	// it.  The running check is emptied so it cannot match this
	// process's own test binary.
	x.cfg.ViewerApp = "Firestorm"
	x.cfg.ViewerRunning = ""
	x.cfg.ViewerLaunch = "true --loginuri {uri} --login {first} {last} {password}"

	got := x.do(t, "viewer --launch")
	if strings.Contains(got, aCredential().GetPassword()) {
		t.Errorf("the minted password was printed:\n%s", got)
	}
	for _, want := range []string{"Quark Idlemind", "http://127.0.0.1:9000/", "PASSWORD", "expires in a minute"} {
		if !strings.Contains(got, want) {
			t.Errorf("a launch should say %q:\n%s", want, got)
		}
	}
	if d.credentials != 1 {
		t.Errorf("one launch asked for %d credentials, want one", d.credentials)
	}
}

// TestTheDaemonsRefusalToMintReachesThePerson: the reasons minting
// fails -- no endpoint, a profile that may not be handed over -- are
// things somebody can act on, and only the daemon knows which it was.
func TestTheDaemonsRefusalToMintReachesThePerson(t *testing.T) {
	x, d := newDaemonShell(t)
	d.status = aViewerStatus(false)
	d.credentialFail = errors.New("example has no viewer_password, so it cannot be handed to a viewer")
	x.cfg.ViewerApp = "Firestorm"
	x.cfg.ViewerRunning = ""
	x.cfg.ViewerLaunch = "true --login {first} {last} {password}"

	got := x.do(t, "viewer --launch")
	if !strings.Contains(got, "viewer_password") {
		t.Errorf("the daemon's reason should reach the prompt:\n%s", got)
	}
}

// TestAnExpiryIsSaidInSomethingAPersonReads: the credential lives five
// minutes now, which the daemon counts out as 300 seconds -- a number
// somebody has to divide before it tells them anything.
func TestAnExpiryIsSaidInSomethingAPersonReads(t *testing.T) {
	for _, tc := range []struct {
		seconds int32
		want    string
	}{
		{300, "5 minutes"},
		{60, "a minute"},
		{45, "45 seconds"},
		{90, "90 seconds"},
	} {
		if got := expiryInWords(tc.seconds); got != tc.want {
			t.Errorf("expiryInWords(%d) = %q, want %q", tc.seconds, got, tc.want)
		}
	}
}

// TestARunningCheckThatCannotRunIsNotReadAsNo: answering "no viewer is
// up" to a check that never ran is how the case the check exists for
// gets missed, so a command that is not there is an error and not a
// green light.
func TestARunningCheckThatCannotRunIsNotReadAsNo(t *testing.T) {
	cfg := Config{ViewerApp: "Firestorm", ViewerRunning: "no-such-command-slsh-viewer-test"}

	up, err := viewerAlreadyRunning(context.Background(), cfg)
	if err == nil {
		t.Fatal("a check that could not be run was taken as an answer")
	}
	if up {
		t.Error("a failed check reported a viewer")
	}
}

// TestARunningCheckIsAnExitStatus: pgrep's shape -- nothing found is a
// failure and not an error -- is the whole contract of the setting.
func TestARunningCheckIsAnExitStatus(t *testing.T) {
	for _, tc := range []struct {
		command string
		want    bool
	}{
		{"true", true},
		{"false", false},
		{"", false}, // no check at all: the launch goes ahead
	} {
		up, err := viewerAlreadyRunning(context.Background(), Config{ViewerRunning: tc.command})
		if err != nil {
			t.Errorf("%q: %v", tc.command, err)
			continue
		}
		if up != tc.want {
			t.Errorf("%q reported running = %v, want %v", tc.command, up, tc.want)
		}
	}
}

// TestSplitCommandGroupsWithQuotesAndNothingElse: a setting is a
// command to run and not a shell line.  Quotes have to group, because
// an application can be called "Firestorm Release", and a config that
// quietly grew redirects or variables would be a way to run anything
// from a file that looks like settings.
func TestSplitCommandGroupsWithQuotesAndNothingElse(t *testing.T) {
	got, err := splitCommand(`open -a "My Viewer" --args > not-a-redirect`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"open", "-a", "My Viewer", "--args", ">", "not-a-redirect"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("split = %q, want %q", got, want)
	}

	if _, err := splitCommand(`open -a "unclosed`); err == nil {
		t.Error("an unclosed quote should be refused rather than guessed at")
	}
}

// TestViewerDefaultsNameTheViewerThatCanReachAPrivateGrid: the
// Firestorm most people already have -- the Second Life flavour, at
// /Applications/Firestorm-Releasex64.app -- cannot be pointed at slgod
// at all.  It compiles the grid manager whose grid-file block is
// disabled (llviewernetwork.cpp:149-201), so it never reads
// grids.user.xml, and asking it for the slgod grid logged "Unknown
// grid 'slgod'" (llviewernetwork.cpp:214) and defaulted to Agni.  A
// default naming that build would send somebody to a live-grid login
// screen with nothing on it to explain itself.
func TestViewerDefaultsNameTheViewerThatCanReachAPrivateGrid(t *testing.T) {
	app, grid, launch, running := viewerDefaults("darwin")
	if app != "Firestorm-OpenSim" {
		t.Errorf("app = %q, want the OpenSim build; the Second Life one cannot reach a private grid", app)
	}
	if grid == "" {
		t.Error("no grid nickname, and --grid is the only flag that points the viewer anywhere")
	}
	if !strings.HasPrefix(launch, "open -a {app} --args") {
		t.Errorf("launch = %q, want it to go through open(1)", launch)
	}
	if !strings.Contains(launch, "--grid {grid}") {
		t.Errorf("launch = %q; --loginuri is read into a setting nothing looks at, so "+
			"--grid is what actually points the viewer at slgod", launch)
	}
	// A whole word: "--grid" contains "-g", which is how this check
	// first read itself wrong.
	for _, w := range strings.Fields(launch) {
		if w == "-g" {
			t.Errorf("launch = %q; -g would leave the viewer behind other windows, "+
				"and being brought to the front is the point", launch)
		}
	}
	if !strings.Contains(running, "{app}") {
		t.Errorf("running = %q, want it to follow the application setting -- the process is "+
			"called plain Firestorm whichever build it came from", running)
	}

	if app, grid, launch, running := viewerDefaults("plan9"); app != "" || grid != "" || launch != "" || running != "" {
		t.Errorf("viewerDefaults(plan9) = %q, %q, %q, %q; want nothing guessed", app, grid, launch, running)
	}
}
