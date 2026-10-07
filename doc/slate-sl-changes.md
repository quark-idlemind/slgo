# What Slate changes in slgo

Slate is a language for testing Second Life products, and a runner for it. The runner is a new package, `slate`, and a new command, `cmd/slate`. It is designed in [slate-runner.md](slate-runner.md). The language is in [slate-language.md](slate-language.md).

This page lists what Slate needs in slgo itself, outside the `slate` package: one change to the object store and the calls that read it, one export, one accept for an object's give, the picture a face shows, the picture finder `imgfind` with its command `cmd/slpic`, and `.gitignore` entries for two commands. The picture a face shows and `imgfind` with `cmd/slpic` are in the repository now.

## Why these are separate

Each change is to code that exists today, or to code that is a package of its own. None of them depends on the Slate runner, and each can be reviewed and merged without it. The click change alters a wire message and the daemon, so it is best merged alone.

## Click action

A full `ObjectUpdate` carries a `ClickAction` byte, and a compressed update carries a `Click` byte in its fixed header. Both are decoded and neither is kept. `Objects.update` never reads `d.ClickAction`, and `Objects.compressed` never reads `c.Click`. A terse update does not carry the byte.

A zero byte is a legal value: it is the touch action. A missing reading therefore cannot be the zero value. Add to `agent.Object`:

```go
Click      uint8
ClickKnown bool
```

`ClickKnown` says the byte was read. A zero `Click` with `ClickKnown` false means no update has said. A zero `Click` with `ClickKnown` true means touch.

### Which store paths set them

| Path | Click and ClickKnown |
|---|---|
| `update` (full update) | Set from `d.ClickAction`. |
| `compressed` (a compressed update that decoded whole) | Set from `c.Click`. |
| `unsure` (a compressed update that did not decode whole) | Set from `c.Click`. |
| `moved` (terse update) | Not touched. |
| An appearance forgotten (`unsure` clearing `TextureEntry`, or `forgetAppearance` after `ObjectImage`) | Not touched. |

`unsure` sets the fields. It does not merely refrain from clearing them. `DecodeCompressed` reads `Click` in the fixed header, and returns `c` with an error when a later field fails. It returns a nil `c` only when the header itself failed, and the caller skips that. A first sighting that arrives as a truncated compressed update, with an intact header, therefore has a known click byte. Forgetting an appearance is not forgetting a click action, because the byte is not part of the appearance.

### Crossing the process boundary

A client that dials with `sl.DialWeak` reads slgod's store through `Hosted.Objects`, which builds `sl.Seen` from `pb.ObjectInfo`. That message has no click field. `texture_anim` is field 15, the last field of `ObjectInfo` in `proto/slgo.proto`, and fields 16 and 17 are free. Add:

```protobuf
uint32 click = 16;
bool click_known = 17;
```

Then regenerate `proto/slgov1` and copy the two values at each step:

- `internal/server/grpc.go`, `Server.Objects`, copies them from `agent.Object` into `pb.ObjectInfo`.
- `sl/hosted.go`, `Hosted.Objects`, copies them from `pb.ObjectInfo` into `sl.Seen`.
- `sl/direct.go`, `Direct.Objects`, copies them from `agent.Object` into `sl.Seen`.
- `sl.Seen`, in `sl/query.go`, gains `Click` and `ClickKnown` with the same meaning.

### An old slgod

Proto3 omits a zero and a false. An slgod that does not know fields 16 and 17 makes a new client read `ClickKnown == false` for every prim. The client and the daemon ship together in this change. Slate does not guess touch from a zero byte. A script that expects a click action against an old slgod fails setup, and does not time out a step. How the runner waits for the byte is in [Step lifecycle](slate-runner.md#step-lifecycle).

### Tests

- A full update with a non-zero click byte is stored, and `Objects` returns the byte with `ClickKnown` true.
- A zero byte with `ClickKnown` true is distinct from a prim that has never been described.
- `DecodeCompressed` is given a blob whose header is intact and whose body is truncated. The test calls `unsure` and reads `ClickKnown`, which is true, and `Click`, which is the header's byte.

The hosted and direct tests that compare a `Seen` are updated for the two new fields.

## Physical material

A full `ObjectUpdate` carries a `Material` byte and a compressed update carries one in its fixed header; `PRIM_MATERIAL` sets it. The store keeps it as `agent.Object.Material` with `MaterialKnown`, set by `update`, `compressed` and `unsure` from the same header the click byte is read from, and not touched by `moved` (a terse update has none) or by an appearance forgotten. Stone is 0, so `MaterialKnown` is what says a byte was read. It crosses the process boundary as `ObjectInfo.material = 32` and `material_known = 33`, copied in `internal/server/grpc.go`, `sl/hosted.go` and `sl/direct.go` (`seenFromAgent`), and `sl.Seen` has `Material` and `MaterialKnown`. An older slgod sends neither, so a new client reads `MaterialKnown` false for every prim; unlike the click byte there is no setup describe for it, and an `expect substance` against such an slgod fails at its step with `no update has said the material of NAME; this slate requires the slgod that stores material and material_known` among its notes. What the store keeps is in [the physical material](objects.md#the-physical-material). Unmeasured.

## ScriptsBlocked

`scriptsBlocked` in `sl/landscripts.go` says why the land will not run scripts in an object. Export it:

```go
func (w *Session) ScriptsBlocked(ctx context.Context, o *Object) string
```

The change is the name. The body is as it is.

It returns a sentence when the region's flags stop every script, or when the parcel runs only its owner's scripts (and its group's, where group scripts are on), the object is none of those, and the object is within 50 m of the ground (an inference from one spot). It returns `""` where the land will run the script, and also where it cannot tell: an object that is worn, nil, has a zero id, or has not been described; land that has not arrived; a parcel or an object that does not answer. So `""` is not proof that a script will run. slgo's `doc/ground.md`, "Where the land stops running scripts", lists the cases.

Slate needs it because `InstallScript` does not call it. Only `Session.Run` does, and Slate installs through `InstallScript`. Without the export, a script installed on land that will not run it fails as a missing hello, with no reason. How Slate uses the result is in [Probe and bridge](slate-runner.md#probe-and-bridge).

## Accepting an object's give

An object's `llGiveInventory` arrives as a dialog-9 instant message, and the accept slgo had, `AcceptInventoryOffer`, answers dialog 4. Measured on Agni on 2026-10-01 ([an object's give](im-senders.md#an-objects-give)): the dialog-5 accept delivered nothing in 10 s, and dialog 10 to the offer's `From`, quoting its transaction, with the Scripts folder as the bucket delivered the item into that folder within 9 s. Other folders were not measured, and neither was a give from an object the tester does not own.

The change is in `sl/im.go`. `InventoryOffer` keeps the offer's `Dialog`, and `InventoryOfferFrom` builds one from dialog 4 and from dialog 9. The viewer's rule is generic: the accept is the offer's dialog + 1 and the decline + 2, so `Accept` and `AcceptInventoryOffer` send dialog 5 for an avatar's offer and dialog 10 for an object's give, and `Decline` and `DeclineInventoryOffer` send 6 and 11. The accept goes to the offer's `From`, quoting its transaction, with the folder as the binary bucket. The decline of dialog 11 is the viewer's rule, not measured.

```go
DialogTaskInventoryAccepted = 10 // the accept of dialog 9
DialogTaskInventoryDeclined = 11 // the decline of dialog 9

func (o *InventoryOffer) Accept(ctx context.Context, into msg.UUID) error
```

A dialog-9 offer's bucket is one byte, the asset type, and carries no item id, so `Asset` is read from it and `Item` stays zero; `Name` is the item's name read out of `Text` (`TaskOfferItemName`). Its unit tests are `TestAnObjectsGiveIsAnInventoryOffer` and `TestAnObjectsGiveIsAcceptedTheWayTheViewerAcceptsIt`. The change is already in the development clone. The comment on `DialogTaskInventoryOffered` and `doc/im-senders.md` were corrected from "not measured". How Slate uses it is in [Give](slate-runner.md#give).

## What a face shows

Slate reads the picture a face of a prim shows. `sl` has it as two additions, in `sl/facepicture.go`, with the story in [face-pictures.md](face-pictures.md):

| Name | What it is |
|---|---|
| `Face.Picture(tex image.Image) (*image.NRGBA, error)` | The picture the face shows of a texture in hand: turned about the middle by the rotation, tiled by the repeats, shifted by the offset, cropped to the face, nearest texel, no side past `MaxDecodeSize`. |
| `Session.FacePicture(ctx, o *Object, face int) (image.Image, error)` | The same for one face of an object: reads the faces, fetches the texture with `TextureImage`, and returns `Face.Picture` of it. |
| `ErrNoTexture`, `ErrPlanarFace`, `ErrAnimatedFace` | What `FacePicture` returns, wrapped with the face number, for a face with no texture, a planar face, and a face whose texture animation is running. |

The animation check is the one `placeTouches` uses. It is in the repository, with tests that present faces and a texture to the fake session.

## Sculpt and mesh

A sculpt and a mesh are one extra-parameter block, type `0x30`, 17 bytes: a texture or asset id and a type byte whose low three bits are the kind (`LL_SCULPT_TYPE_*`, `llvolume.h:189-199`: 1 to 4 sculpt, 5 mesh, 6 glTF) and whose top two are the invert and mirror flags. Their `ProfileCurve` and `PathCurve` are ordinary ones, so the shape alone cannot say a prim is not a torus. `agent.Object.Sculpt`, a `msg.SculptMark{Kind, ID}`, keeps the block; its zero value is neither (`isSculpt`, `llvolume.cpp:3300`, takes kind none as no sculpt).

A full or compressed update says it afresh, so one with no block clears it. A terse update and a compressed one that did not decode whole leave it alone, as they leave the shape. It crosses as `ObjectInfo` fields 18 and 19 and reaches `sl.Seen.Sculpt`.

`Seen.FaceCount` is what to call for a prim's face count: it is `Shape.Faces` after `Form`, and false for a sculpt, a mesh and an object no update has described. The tests are `TestTheSculptMarkIsWhatTheLastFullOrCompressedUpdateSaid`, `TestSculptMarkOf`, `TestFaceCountIsNotGivenForASculptOrAMesh`, and the daemon's `TestObjectsWithoutAReadableIdAreDropped` and gRPC test.

## imgfind

Slate depends on the image finder, package `imgfind`, which is in the repository. It reads text and simple figures in a picture and imports nothing from slgo. The story and the thresholds are in [imgfind.md](imgfind.md).

It exports:

| Name | What it is |
|---|---|
| `Find(img image.Image, ...*Request)` | Finds each request in a decoded image and returns items with a location, size and centre. |
| `FindFile(path, ...*Request)` | Decodes a PNG, JPEG or GIF and calls `Find`. |
| `Text`, `Pattern`, `Drawing`, `Box` | Constructors for a request: a word or line, a regular expression over words, a circle or an arrow, an outlined box. |

Text and patterns need `tesseract` at run time. A drawing or a box is read from the pixels and does not. The text tests skip when `tesseract` is not on `PATH`.

`cmd/slpic` comes with the package. It has `faces`, to write the picture of each textured face of an object (`Session.FacePicture`, or the texture as uploaded with `-raw`), and `find`, to run requests on an image file. Its binary is `slpic`.

The thresholds were measured on one sample image, one 1024 by 1024 HUD texture, and on the scenes drawn in the package's own tests. The sample is not in the repository, so no test reads it, and the thresholds have not been measured on another image. The identity check (`CLAUDE.md`) applies to all of it.

## .gitignore

`.gitignore` says a new command wants its name in two lists: under `/cmd/*/`, because a binary is left beside its source by `go build` inside a command directory, and in the root, because `go build ./cmd/slate` leaves it there. Add, with the other commands:

```text
/cmd/*/slate
/cmd/*/slpic
```

and in the root list:

```text
/slate
/slpic
```

`/slate` on its own would also hide the `slate` package directory, because a leading slash with no trailing slash matches a directory too. Follow it with `!/slate/`, the way the file does for `/md` and `!/md/`. There is no package named `slpic`, so `/slpic` needs no exception.

## Order

1. The click change depends on nothing.
2. The `ScriptsBlocked` export depends on nothing.
3. The `InventoryOffer` form of the give accept depends on nothing, and is needed before the Slate PR that accepts gives, PR 6.
4. `Face.Picture` and `Session.FacePicture` depend on nothing in this page; they need `sl` as it is. `imgfind` and `cmd/slpic` depend on nothing in this page, and `cmd/slpic` needs `FacePicture`.
5. The `.gitignore` lines go in with the first change that adds either command: `cmd/slpic` with `imgfind`, and `cmd/slate` with the last Slate PR.
6. The Slate runner depends on all of them: click, `imgfind` and `FacePicture` before the PRs that read a click action or a button, the accept before the PR that accepts gives, and the export before the PR that installs the probe.

The runner's PR plan is in [slate-runner.md](slate-runner.md#pr-plan).
