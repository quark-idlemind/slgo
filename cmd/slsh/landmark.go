package main

// A place kept, and gone back to.
//
// tp is the command for a place said in numbers: a region name, three
// coordinates, and a name that has to be spelled the way the grid
// spells it.  What it cannot do is remember, so a place found once has
// to be written down outside the program and typed back in.  A landmark
// is the grid's own answer to that -- an ordinary inventory item -- and
// this is the command that lists them, reads them, makes them and goes
// to them.  Home is here too, both halves of it: going there, and
// choosing where it is.
//
// The things it can do to the world are one message each, and they are
// sl's: Session.Landmark fetches the asset and parses it,
// Session.MakeLandmark is a create with two type numbers on it,
// Session.GoTo is TeleportLandmarkRequest, Session.GoHome is the same
// message with the null id in it, and Session.SetHome is
// SetStartLocationRequest -- which is not a landmark at all and is here
// because home is, and because splitting the two halves of home across
// two commands is how somebody comes to look for one of them under the
// other.  What is here is the part that has to happen in a shell:
// turning a name somebody typed into exactly one item, and printing
// what came back without pretending to know more than the asset holds.
//
// The measurements behind all of it are in doc/history/landmark.md,
// taken on Agni on 2026-08-18.
//
// # Why going somewhere is a word and not a letter
//
// If going somewhere were the bare form, then reading a landmark and
// being somewhere else afterwards would be one typo apart -- so it is
// not the bare form: "landmark NAME" says where it goes and
// "landmark --go NAME" goes there.  For the same reason none of the four verbs has a short
// letter.  -g beside -m is exactly the typo that the whole word is
// there to prevent, and the only cost of spelling it out is four
// characters on a line that moves an avatar across the grid.
//
// --set-home is the sharp end of the same argument.  It is the one verb
// whose damage a teleport does not undo -- an avatar sent to the wrong
// place walks back, and an account whose home was quietly rewritten
// finds out weeks later, somewhere it did not mean to log in -- so it
// carries the word "set", it has no letter, and it takes no name.
//
// # Why a name means something in inventory and nothing else
//
// An inventory item has an ITEM id and an ASSET id and the grid takes
// only the second: measured, the item id and a uuid that is nothing at
// all are both answered with perfect silence, waited out to twelve
// seconds.  There is no error to catch and nothing to report.
//
// So this command never hands the grid a uuid a person typed.  A name
// is looked up in what this avatar KEEPS -- inventory less the trash,
// see landmarksHeld -- and the asset id comes out of the listing,
// which is the one place the two ids are told apart correctly.  A uuid
// typed here is looked up in that same listing -- as an item id or as an
// asset id, since a person pasting one from "ls -l" has the first -- and
// a uuid that names nothing there is refused rather than sent.  Reading
// an unknown uuid would in fact work, because the fetch either parses as
// a landmark or does not; but the same uuid handed to --go is either an
// asset id or an item id and nothing distinguishes them but a wait that
// never ends, so the two forms would differ in a way nobody could
// predict from the outside.  One rule: a landmark is something you have.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// landmarkOptions is what landmark was asked for.
//
// The four verbs are exclusive and each is a whole word: see the file
// comment for why none of them has a letter.  --wait is tp's flag with
// tp's meaning, because it is the same waiting for the same kind of
// arrival, and a teleport this shell asked for should not have two
// different budgets depending on which command asked.
//
// --set-home is the one that changes something and does not move
// anybody, and it is spelled with the word "set" in it for that reason:
// --home beside a --home that meant "make this home" is one keystroke
// between going somewhere and rewriting where this account starts, and
// the second of those is not undone by teleporting back.
type landmarkOptions struct {
	Make    bool `getopt:"--make    make a landmark of where this avatar is standing, called NAME"`
	Go      bool `getopt:"--go      go to the landmark NAME names"`
	Home    bool `getopt:"--home    go to wherever this account's home is set"`
	SetHome bool `getopt:"--set-home  make where this avatar is standing the place home is"`
	Wait    int  `getopt:"--wait -w=SECONDS  how long to wait for the avatar to arrive [30]"`
	Help    bool `getopt:"--help -h  show what this command takes"`
}

// landmarkDepth is how far down inventory the search for landmarks
// goes, and it is find's depth for find's reason: AIS takes the depth
// on the request, so the whole of it is one round trip whatever the
// number, and four levels reaches everything anybody has filed by hand.
//
// It is deliberately the whole tree and not /Landmarks.  The simulator
// files a new landmark under Landmarks without being asked, so that is
// where nearly all of them are -- but nothing stops one being dragged
// into a folder of its own, and a command that only looked in one place
// would answer "there is no such landmark" about an item the person can
// see in ls.
const landmarkDepth = 4

// landmarkAssetWait is how long --make gives the asset to appear before
// reading it a second time.
//
// Measured on Agni: the item came back about 300ms after the request
// and its asset was readable about 200ms after that.  So the first read
// usually finds it and a read that does not is worth one retry rather
// than a report of failure -- but only one, because a landmark that is
// still not there after half a second is not late, it is missing, and
// the item is made either way.
const landmarkAssetWait = 500 * time.Millisecond

// cmdLandmark lists, reads, makes and goes to landmarks, and sets and
// goes to home.
func cmdLandmark(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o landmarkOptions
	args, done, err := subOptions("landmark", &o, out, args)
	if err != nil || done {
		return err
	}

	asked := 0
	for _, v := range []bool{o.Make, o.Go, o.Home, o.SetHome} {
		if v {
			asked++
		}
	}
	if asked > 1 {
		return usageError("landmark",
			"making a landmark, going to one, going home and setting home are four "+
				"different things; ask for one")
	}
	if o.Wait != 0 && !o.Go && !o.Home {
		return usageError("landmark",
			"--wait is the waiting a teleport does, and neither reading a landmark "+
				"nor setting home moves the avatar")
	}
	if (o.Home || o.SetHome) && len(args) > 0 {
		return usageError("landmark",
			"home is the one landmark nobody has to name, so this form takes none; "+
				"home is set to where this avatar is standing and nowhere else")
	}

	// Joined with spaces, as tp joins a region name: an inventory name
	// has spaces in it far more often than not, and a quoting rule to
	// remember at a prompt is a quoting rule to get wrong.
	name := strings.Join(args, " ")
	if name == "" && (o.Make || o.Go) {
		return usageError("landmark", "which landmark: a name, as \"landmark\" lists them")
	}

	// One deadline for both trips, because it is one question: how long
	// to go on believing in an arrival.  Measured at about a second,
	// within a region and across two alike.
	wait := time.Duration(o.Wait) * time.Second
	if wait == 0 {
		wait = shellTeleportTimeout
	}

	switch {
	case o.Home:
		return landmarkHome(ctx, sh, out, wait)
	case o.SetHome:
		return landmarkSetHome(ctx, sh, out)
	case o.Make:
		return landmarkMake(ctx, sh, out, name)
	case o.Go:
		return landmarkGo(ctx, sh, out, name, wait)
	case name != "":
		return landmarkRead(ctx, sh, out, name)
	}
	return landmarkList(ctx, sh, out)
}

// landmarkList prints the landmarks this avatar keeps.
//
// Keeps, and not holds: what is in the trash is left out, for the
// reason landmarksHeld gives.  Two of the seven in qi's inventory were
// deleted items, and one of them made a live landmark ambiguous with
// its own corpse.
//
// Paths and not names, because the path is the name with the answer to
// "which of the two called that" already in it, and its last segment is
// what a person types when there is only one.  Nothing here is fetched:
// where a landmark goes is inside its asset, and a listing that read
// them all would be a request apiece to say something nobody asked
// about the other twenty.
func landmarkList(ctx context.Context, sh *Shell, out io.Writer) error {
	_, kept, trashed, err := landmarksHeld(ctx, sh)
	if err != nil {
		return err
	}
	if len(kept) == 0 {
		fmt.Fprintln(out, "no landmarks in inventory")
		if len(trashed) > 0 {
			// Otherwise this reads as a listing that has lost
			// something, for somebody who can see them in "ls".
			fmt.Fprintf(out, "%d %s in the trash, and what is in the trash is not "+
				"listed or gone to\n",
				len(trashed), plural(len(trashed), "landmark is", "landmarks are"))
		}
		fmt.Fprintln(out, "\"landmark --make NAME\" makes one of wherever this avatar is standing")
		return nil
	}
	fmt.Fprintf(out, "%d %s\n", len(kept), plural(len(kept), "landmark", "landmarks"))

	// A path that appears twice is two landmarks of one name in one
	// folder, and printing it twice is two identical lines with nothing
	// to choose between them -- which is what stage 3 saw.  Those lines
	// get the id, and only those: a listing that put an id on every row
	// would pay for one collision with a column of noise on every
	// inventory that has none.
	seen := map[string]int{}
	for _, e := range kept {
		seen[e.Path]++
	}
	for _, e := range kept {
		if seen[e.Path] > 1 {
			fmt.Fprintf(out, "  /%-40s %s\n", e.Path, e.ID)
			continue
		}
		fmt.Fprintf(out, "  /%s\n", e.Path)
	}
	return nil
}

// landmarkRead says where one goes, without going.
func landmarkRead(ctx context.Context, sh *Shell, out io.Writer, name string) error {
	e, err := findLandmark(ctx, sh, out, name)
	if err != nil {
		return err
	}
	lm, err := sh.s.Landmark(ctx, e.Asset)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%s\n", e.Name)
	fmt.Fprintf(out, "  in       /%s\n", parentPath(e.Path))
	printLandmark(out, lm, e.ID)
	return nil
}

// printLandmark prints what the asset holds, and both of the item's
// uuids beside it.
//
// The region is an id and is left as one.  A landmark carries no region
// NAME and no handle, and the only way this program has of turning one
// into the other is a world map lookup by name -- so there is nothing to
// look the id up by, and a line that said "region" followed by something
// that looked like a place would be an invention.  What is there to say
// is where you would arrive if you went, which is the point of reading
// one; what is there is at the far end is parcel's question, asked after
// arriving.
//
// The position is printed to two decimals because that is what the
// asset holds: the simulator writes it from where the avatar stood, and
// the couple of centimetres it is out by are a standing avatar's drift
// rather than a rounding this should hide.
//
// Both uuids, and labelled.  The whole trap this command is built
// around is that they are not interchangeable -- the asset id is the one
// the grid takes and the item id is the one a listing puts in front of a
// person -- so a page that showed one of them would be showing half of
// the thing that goes wrong.
func printLandmark(out io.Writer, lm *sl.Landmark, item msg.UUID) {
	fmt.Fprintf(out, "  region   %s\n", lm.Region)
	fmt.Fprintf(out, "  at       %.2f, %.2f, %.2f\n",
		lm.Position.X, lm.Position.Y, lm.Position.Z)
	if !item.IsZero() {
		fmt.Fprintf(out, "  item     %s\n", item)
	}
	fmt.Fprintf(out, "  asset    %s\n", lm.Asset)
}

// landmarkMake makes a landmark of where the avatar is standing, and
// reads back what the simulator wrote in it.
//
// Nothing says where the landmark is of and nothing can: the position
// is the simulator's to write, out of where it believes this avatar is
// at the instant the message lands.  So the answer is read back rather
// than assumed, for the reason sayPosition is read back after a
// teleport -- printing what was asked for would be printing something
// nobody said.
//
// No description.  It is the one field of a landmark that the grid does
// not fill in, and there is nothing to put in it that the asset does
// not already hold; a made-up sentence about the region would be this
// shell writing something into inventory that it was not asked to.
func landmarkMake(ctx context.Context, sh *Shell, out io.Writer, name string) error {
	it, err := sh.s.MakeLandmark(ctx, name, "")
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "made %s\n", it.Name)
	defer sayIfTheNameIsTaken(ctx, sh, out, it.Name)

	if it.AssetID.IsZero() {
		// Nothing to read and nothing to go to.  Said plainly, because
		// a zero asset id handed to GoTo later is the null id, and the
		// null id is what the grid reads as home.
		fmt.Fprintf(out, "  item     %s\n", it.ID)
		fmt.Fprintln(out, "the simulator named no asset for it, so there is nothing to go to yet")
		return nil
	}

	lm, err := readMadeLandmark(ctx, sh, it.AssetID)
	if err != nil {
		fmt.Fprintf(out, "  item     %s\n", it.ID)
		fmt.Fprintf(out, "  asset    %s\n", it.AssetID)
		fmt.Fprintf(out, "the item is made; its asset would not read yet (%v)\n", err)
		return nil
	}
	printLandmark(out, lm, it.ID)
	return nil
}

// sayIfTheNameIsTaken warns when the landmark just made is not the only
// one of its name.
//
// Nothing stops it: inventory takes the same name any number of times,
// and stage 3 made "stage 3 thrushmoor" twice in one folder without a
// word said.  The consequence is silent and arrives later -- from then
// on the name is refused as ambiguous and only an id will do -- so the
// command that created it is the one that should mention it.
//
// After the make and not before.  Before would be a check on a name
// that might not get used, and it would race anything else filing an
// item; after, the count includes what was just made and is simply
// true.  It costs the one listing the rest of the command pays for
// anyway, and a listing that fails says nothing rather than turning a
// landmark that was made into a command that failed.
//
// Written as a deferred call so that every way out of landmarkMake goes
// through it: a duplicate name is worth saying whether or not the asset
// could be read back.
func sayIfTheNameIsTaken(ctx context.Context, sh *Shell, out io.Writer, name string) {
	_, kept, _, err := landmarksHeld(ctx, sh)
	if err != nil {
		return
	}
	if n := len(matchLandmarks(kept, name)); n > 1 {
		fmt.Fprintf(out, "there are now %d landmarks called %q; a name that means "+
			"several is refused here, so this one is reached by its id until one of "+
			"them is renamed with \"mv\"\n", n, name)
	}
}

// readMadeLandmark reads a landmark that has just been created, with
// the one retry the measurement says it is worth.  See
// landmarkAssetWait.
func readMadeLandmark(ctx context.Context, sh *Shell, asset msg.UUID) (*sl.Landmark, error) {
	lm, err := sh.s.Landmark(ctx, asset)
	if err == nil {
		return lm, nil
	}
	select {
	case <-time.After(landmarkAssetWait):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return sh.s.Landmark(ctx, asset)
}

// landmarkGo takes the avatar to a landmark.
//
// What is printed before the wait is the landmark's name and nothing
// else, and that is honest rather than lazy: where it goes is inside an
// asset this has not fetched, and fetching one to make a nicer line
// would be a request spent on a sentence rather than on the journey.
// It is printed before the waiting starts for tp's reason -- a shell
// that has silently stopped answering is indistinguishable from one
// that has hung -- and what is printed after is where the avatar
// actually ended up, read back.
//
// The asset is not fetched to check it first, either.  The id came out
// of a listing that said the item is a landmark, so it is the right
// kind of id already; the silence this command exists to avoid is the
// silence a hand-typed uuid gets, and requiring a name in inventory is
// what rules that out.
func landmarkGo(ctx context.Context, sh *Shell, out io.Writer, name string, wait time.Duration) error {
	e, err := findLandmark(ctx, sh, out, name)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "going to %s\n", e.Name)

	// The name goes down with the request so that a refusal names what
	// was typed.  Stage 3 met the line without it: "the teleport to
	// landmark 0d9b7e57-..." identified the destination by a uuid
	// nobody typed, on the line under one that had just said the name.
	if err := sh.s.GoTo(ctx, e.Asset, strconv.Quote(e.Name), wait); err != nil {
		// Otherwise left as it comes.  A refusal is ErrTeleportRefused
		// with the grid's own two voices in it, a timeout already
		// blames the likeliest cause -- the item id where the asset id
		// was wanted -- and a refusal to shorten a journey already says
		// what it usually means.  None of that is worth restating.
		return err
	}
	return sh.sayPosition(ctx, out)
}

// landmarkHome takes the avatar home.
//
// It is the same message with the null id in it, which the grid reads
// as home rather than as an error, so home is the one landmark nobody
// has to own and it costs no inventory lookup at all.
func landmarkHome(ctx context.Context, sh *Shell, out io.Writer, wait time.Duration) error {
	fmt.Fprintln(out, "going home")
	if err := sh.s.GoHome(ctx, wait); err != nil {
		return err
	}
	return sh.sayPosition(ctx, out)
}

// landmarkSetHome makes where the avatar is standing the place home is.
//
// It says where before it says what the grid said, because the place is
// the half a person can check: the grid's sentence is about a position
// it does not name, and either voice of it -- the yes as much as the no
// -- means nothing without the line above it.
//
// The place printed is the one sl sent and not one read here.  A
// position read for the printing would be a second read, and the two
// are not always the same: an avatar that has just teleported is still
// settling, and this shell printed a position eight metres above the
// one that went out before that was the rule.  Both lines therefore
// come after the answer, which costs nothing a person would notice --
// measured, the grid answers at once.
//
// The grid's own words are printed and not paraphrased.  The answer to
// this arrives as an alert rather than as a reply, so the sentence is
// the whole of what came back, and a shell that reworded it would be
// putting its own account of a refusal in front of the simulator's.
func landmarkSetHome(ctx context.Context, sh *Shell, out io.Writer) error {
	at, said, err := sh.s.SetHome(ctx, 0)
	if at != nil {
		// Said whether or not the grid agreed: a refusal that named no
		// place would leave somebody guessing which parcel refused.
		fmt.Fprintf(out, "setting home to %s\n", positionLine(at))
	}
	if err != nil {
		return err
	}
	fmt.Fprintln(out, said)
	return nil
}

// landmarksHeld lists inventory once and sorts it into three heaps.
//
// kept is the landmarks this command will act on.  trashed is the ones
// that answer to a name and must not be acted on, kept separately
// rather than dropped so that a name which finds only those can be told
// what became of it.  all is everything, because the useful half of
// "there is no landmark called that" is what there is instead, and the
// listing already has it.
//
// # Why the trash is a heap of its own
//
// A viewer's delete moves things to the trash, so an inventory that has
// been used through one has deleted landmarks in it -- two of seven in
// the account this was written against.  (rm in this shell does not put
// them there: it is an AIS DELETE and the item is gone.)  A
// deleted landmark still has a name, an asset and a working teleport,
// so without this the shell would refuse a live landmark as ambiguous
// with its own corpse and --go would travel to a deleted one as
// readily as to a kept one.  Deleted has to mean deleted.
//
// A link is not a landmark here either.  A link's asset id is the ITEM
// id of what it points at, and an item id sent to the grid as a
// landmark is answered with silence -- so following one would produce
// exactly the failure this command is arranged to make impossible.  A
// name that finds only links says so rather than being told there is
// nothing.
func landmarksHeld(ctx context.Context, sh *Shell) (all, kept, trashed []sl.Entry, err error) {
	all, err = sh.s.ListInventory(ctx, "", landmarkDepth)
	if err != nil {
		return nil, nil, nil, err
	}
	bins := trashPaths(all)
	for _, e := range all {
		if e.Folder || e.IsLink || sl.AssetType(e.Type) != sl.AssetLandmark {
			continue
		}
		if inTrash(e.Path, bins) {
			trashed = append(trashed, e)
			continue
		}
		kept = append(kept, e)
	}
	return all, kept, trashed, nil
}

// trashPaths is where the trash is, out of the listing that has just
// been fetched.
//
// By the folder's preferred type and not by its name, for the reason
// sl.FolderTrash gives: a folder is the trash because of its type, it
// can be renamed, and an account made through a viewer in another
// language never called it "Trash" at all.
//
// Out of the listing rather than by asking sl.TrashFolder, which is a
// second round trip to learn something this already has -- and the
// answer would be an id, which would then have to be turned back into a
// path to match the entries against.  A trash folder too deep to appear
// in the listing takes everything inside it with it, so there is
// nothing to exclude and nothing to get wrong.
//
// A slice rather than one path: nothing says an inventory has exactly
// one folder of that type, and looping over one is the same code as
// looping over two.
func trashPaths(es []sl.Entry) []string {
	var out []string
	for _, e := range es {
		if e.Folder && e.Type == sl.FolderTrash {
			out = append(out, e.Path)
		}
	}
	return out
}

// inTrash says whether a path is one of these folders or under one.
//
// A prefix and not a first segment, because the trash need not be at
// the root -- and the separator is required after it, so that a folder
// called "Trashcan" beside the trash is not swallowed by it.
func inTrash(path string, bins []string) bool {
	for _, bin := range bins {
		if path == bin || strings.HasPrefix(path, bin+string(sl.PathSeparator)) {
			return true
		}
	}
	return false
}

// matchLandmarks picks out everything answering to what somebody typed.
//
// A uuid is looked up as an item id or as an asset id, and matches at
// most one thing: the two ids are unique and a person pasting one from
// "ls -l" has the first, which the grid will not take.  Anything else
// is a name, matched exactly, in the case it has, as sl.PickNamed
// matches one -- against the entry's own name, or against its whole
// path with or without the leading separator a listing prints.  A
// landmark is an inventory item, and "Home" and "home" are two of them.
//
// It is one function rather than two so that the same rule decides what
// was meant whether the answer is a landmark, a refusal about the
// trash, or a refusal about a notecard.  A name that means a kept
// landmark and a deleted one must not mean one thing to the search and
// another to the explanation of why the search found nothing.
func matchLandmarks(es []sl.Entry, want string) []sl.Entry {
	if id, err := msg.ParseUUID(strings.TrimSpace(want)); err == nil {
		for _, e := range es {
			if e.ID == id || (!e.Asset.IsZero() && e.Asset == id) {
				return []sl.Entry{e}
			}
		}
		return nil
	}
	path := strings.TrimPrefix(want, string(sl.PathSeparator))
	var out []sl.Entry
	for _, e := range es {
		if e.Name == want || e.Path == path {
			out = append(out, e)
		}
	}
	return out
}

// nearLandmarks is the refusal for a name no kept landmark has, when
// one has it in another case: sl.AllNamedFunc's, naming them as the
// hint, by name or by whole path as the name was typed.  Nil when none
// is that close.
func nearLandmarks(kept []sl.Entry, want string) error {
	key := func(e sl.Entry) (string, msg.UUID) { return e.Name, e.ID }
	if strings.ContainsRune(want, sl.PathSeparator) {
		want = strings.TrimPrefix(want, string(sl.PathSeparator))
		key = func(e sl.Entry) (string, msg.UUID) { return e.Path, e.ID }
	}
	_, err := sl.AllNamedFunc(kept, want, "landmark", "", key)
	if ne := (*sl.NameError)(nil); errors.As(err, &ne) && len(ne.Near) > 0 {
		return err
	}
	return nil
}

// findLandmark turns what somebody typed into exactly one landmark.
//
// Only kept landmarks are searched, so a deleted one can neither be
// travelled to nor make a live one ambiguous.  That holds for the whole
// path and for the id as well as for the bare name: a form that reached
// into the trash would mean --go could still take the avatar to a
// landmark somebody deleted, which is the whole of what excluding them
// is for, and "deleted" that means "deleted unless you type more of it"
// is not a rule anybody can hold in their head.
//
// A name that means several is refused and never guessed at, and the
// reason is tp's: the guess is not a listing that can be read again, it
// is an avatar somewhere it was not sent.  Reading is refused as well
// as going, although reading is harmless, because the two forms differ
// by five characters and a rule that held for one of them and not the
// other would be learnt from the harmless one.
func findLandmark(ctx context.Context, sh *Shell, out io.Writer, name string) (sl.Entry, error) {
	all, kept, trashed, err := landmarksHeld(ctx, sh)
	if err != nil {
		return sl.Entry{}, err
	}

	match := matchLandmarks(kept, name)
	switch len(match) {
	case 1:
		return withAsset(match[0])
	case 0:
		return sl.Entry{}, noSuchLandmark(all, kept, trashed, name)
	}

	// Two landmarks in one folder can share a name outright -- stage 3
	// made a pair on purpose and qi's inventory has them -- and then
	// the path they share is exactly what was typed.  Telling somebody
	// to say which by its whole path sends them round the same loop, so
	// the advice is what is actually left: the id, and only the id.
	for _, e := range match {
		fmt.Fprintf(out, "  /%-40s %s\n", e.Path, e.ID)
	}
	if sharePath(match) {
		return sl.Entry{}, fmt.Errorf("%d landmarks answer to %q and they are one name "+
			"in one folder, so the path cannot tell them apart and the id beside each "+
			"is the only thing that can", len(match), name)
	}
	return sl.Entry{}, fmt.Errorf("%d landmarks answer to %q; say which by its whole "+
		"path, or by the id beside it", len(match), name)
}

// withAsset refuses a landmark whose asset the simulator never named.
//
// A zero asset id is not a missing landmark, it is the null id, and the
// null id is the one wrong id the grid is not silent about: it reads it
// as HOME and obeys.  sl.GoTo refuses one for that reason and this
// refuses it earlier, so that the reading form says the same thing as
// the going form rather than failing somewhere further in.
func withAsset(e sl.Entry) (sl.Entry, error) {
	if e.Asset.IsZero() {
		return sl.Entry{}, fmt.Errorf("the landmark %q has no asset id in the listing, "+
			"and the asset is the whole of a landmark; there is nothing here to read "+
			"and nothing to go to", e.Name)
	}
	return e, nil
}

// sharePath says whether everything here sits at the same path, which
// is what a name in one folder twice comes to.
//
// Compared exactly, as matchLandmarks compares a path: two paths that
// differ only in case are two paths, and each names its own landmark.
func sharePath(es []sl.Entry) bool {
	for _, e := range es[1:] {
		if e.Path != es[0].Path {
			return false
		}
	}
	return true
}

// noSuchLandmark says what there is instead, which is nearly always the
// useful half of the answer.
//
// The trash first, because that is the one case where the thing being
// asked for really is there and really is a landmark, and a person who
// deleted it an hour ago and has forgotten is owed the sentence rather
// than "no landmark called that".  Then whatever else answers to the
// name -- a link, a notecard, a folder -- and only then the ways of
// naming nothing at all: a uuid, a kept landmark's name in another
// case, and nothing like anything.
func noSuchLandmark(all, kept, trashed []sl.Entry, name string) error {
	if gone := matchLandmarks(trashed, name); len(gone) > 0 {
		return inTheTrash(gone, name)
	}
	if other := matchLandmarks(all, name); len(other) > 0 {
		e := other[0]
		if e.IsLink {
			return fmt.Errorf("%q is a link rather than a landmark: a link's asset id "+
				"is the ITEM id of the thing it points at, and an item id sent to the "+
				"grid as a landmark is answered with silence -- name the landmark itself",
				name)
		}
		return fmt.Errorf("%q is %s, not a landmark", name, aKind(kindOf(e)))
	}
	if id, err := msg.ParseUUID(strings.TrimSpace(name)); err == nil {
		return fmt.Errorf("no landmark in inventory has the id %s, as either "+
			"of its two ids; a uuid that is not something this avatar holds is refused "+
			"rather than sent, because the grid answers a landmark id it does not "+
			"recognise with silence and never with an error", id)
	}
	if err := nearLandmarks(kept, name); err != nil {
		return err
	}
	return fmt.Errorf("no landmark called %q; \"landmark\" lists the ones this avatar holds", name)
}

// inTheTrash is the refusal for a name whose only answer is deleted.
//
// It says where the thing is, because the path is what "mv" needs to
// bring it back, and it says plainly that this command will not use it
// -- a listing it is missing from and a refusal that did not explain
// would leave somebody looking for a landmark they can see in "ls".
func inTheTrash(gone []sl.Entry, name string) error {
	if len(gone) == 1 {
		return fmt.Errorf("the only landmark answering to %q is in the trash, at /%s; "+
			"a deleted landmark is not listed here and is not gone to, so that it can "+
			"never be mistaken for a kept one -- \"mv\" moves it back out",
			name, gone[0].Path)
	}
	return fmt.Errorf("%d landmarks answer to %q and every one of them is in the trash; "+
		"a deleted landmark is not listed here and is not gone to -- \"mv\" moves one "+
		"back out", len(gone), name)
}

// parentPath is the folder part of a listing's path, for the line that
// says where an item was found.  The root has no name and prints as
// nothing after the separator, which is how the rest of the shell
// writes it.
func parentPath(path string) string {
	names := sl.SplitPath(path)
	if len(names) < 2 {
		return ""
	}
	return sl.JoinPath(names[:len(names)-1]...)
}
