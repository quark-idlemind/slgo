# Finding a label or a figure in a picture

`imgfind` answers the questions a test asks of a screenshot: where is
this label, where is this arrow, where is the outlined button. It reads
an image, or a PNG, JPEG or GIF file, and gives back each match with its
top-left, its size and its centre, which is the point a click would use.
It imports nothing from slgo. The picture a face of an object shows is
built by `sl` ([What a face shows](face-pictures.md)), so the same
questions can be asked of a texture. `cmd/slpic` is its command. This is
what the thresholds rest on.

## What the numbers were measured on

Every threshold below was measured on one sample image, one 1024 by
1024 HUD texture, and on the scenes the package's own tests draw. The sample is not in the
repository, because it shows a product's interface and the rules of this
repository keep that out. A number below that says "the sample" was
read off it and has not been checked on any other image. The drawn
scenes are in `imgfind/find_test.go`: a ring, three triangles, a
rectangular frame and a word, black on white, and a ring and a frame,
white on black.

When the package meets an image that is not like these, the thresholds
are the first thing to doubt, and the first thing to do is measure one
more image and say so here.

## Calling it

```go
func Find(img image.Image, reqs ...*Request) ([]*Item, error)
func FindFile(path string, reqs ...*Request) ([]*Item, error)
```

`Find` takes a decoded image. `FindFile` decodes a PNG, JPEG or GIF and
calls `Find`.

The constructors are:

- `Text(s)` matches a word, or words on one line joined by one space
  (`"Example Menu"`). The match is case-sensitive and exact after the
  request is trimmed.
- `Pattern(expr)` applies an unanchored regular expression to those
  same words and lines.
- `Drawing(name)` matches `circle` (also `circles`, `ring`), `arrow`
  (also `arrows`), or a direction: `left arrow`, `right arrow`,
  `up arrow`, `down arrow`. `arrow left` and `leftarrow` are the same
  direction. Case, hyphens and extra spaces are ignored.
- `Box()` matches every outlined box. Its `Contents` is `outlined box`.

`Item.Request` is the caller's pointer, and one request may match many
times. Nothing found is an empty slice and not an error. Items come in
the order of the requests, and within a request from top to bottom and
then left to right; two locations within 8 px in Y are one row.

`Location` is the top-left, `Width` and `Height` the size in pixels,
and `Center` is `x + w/2`, `y + h/2` in integer division. The origin is
the top-left of the image and Y increases downward.

Errors start with `imgfind:`. A nil request, blank text, a pattern that
does not compile, an unknown drawing and an unknown kind fail before
the file is opened or the image looked at. The image is decoded once, and tesseract is run
only when some request is text or a pattern. A drawing or a box is read
from the pixels and needs nothing installed.

## How text is read

Tesseract is run with page segmentation mode 11 and TSV on stdout, and
the picture is written to its standard input. A path under `/tmp` was
seen to fail with "failed to open locally", so no file name is passed.
The text tests skip when `tesseract` is not on `PATH`.

Luminance is `(3R+6G+B)/10`. When the four corners average below 128
the page is dark and the image is inverted, so ink is the low value
either way and the thresholds stay where they are.

One threshold could not read every label on the sample: a threshold
that keeps the button labels breaks the thin stem of a tall letter, and
the title was read with a letter wrong. Three
passes therefore run together, each with a 90 second timeout:

- Stroke, threshold 140, no dilation. It reads the button labels. For a
  correctly read button word tesseract reported confidence 0, and the
  letters were right.
- Closed, threshold 125, dilated once. It reads one short word that the
  stroke pass misreads (confidence about 10 against 75 to 82).
- Title, threshold 115, dilated once. It reads the display type. The
  other passes misread a title at confidence about 90, and the title
  pass read it right at confidence 0, so confidence cannot choose
  between them.

Readings of one place are one word when they overlap a member of that
cluster: intersection over union at least 0.3, or one centre inside the
other box. The overlap is against a member and not the cluster's
bounding box, so a loose box cannot bridge two labels. A token is kept
when every rune is a letter or a digit, which drops a punctuated
misreading and lets the stroke pass win that word.

A word at least 40 px tall takes the title pass. Titles on the sample
are about 50 px and button labels about 25. A shorter word takes the
reading of highest confidence of at least 40, and when none reaches 40
the stroke pass is kept.

Words whose vertical centres lie within `max(10, half the taller
height)` are one line. A gap of at most three quarters of the taller
height joins them with a space. The two words of a title joined on the
sample (a gap of about 21 px at a height of about 50), and labels in
separate buttons did not, because the buttons sit further apart. A
test that needs no tesseract, `TestPhrasesJoinWordsOnOneLine`, holds
the joining.

Cropping a button and reading it again at page segmentation mode 7 or
8 was worse than the three full-page passes, and taking the highest
confidence on a tall word chose the misreading over the right one.

## How figures are read

Components are 8-connected, walked with an explicit stack, at two
thresholds. 170 is dark enough that a round button splits into its
ring and the arrow inside it. 205 is light enough that the pale lower
edge of a button stays with the darker top, so the box is the whole
button. At 170 only the top arc of a button remains, and its bottom-edge
coverage is about 0.02 to 0.25.

A circle has both sides at least 48, an aspect between 0.85 and 1.18,
corner ink under 0.08, and either a radial coefficient of variation
under 0.15 (a ring; the sample's rings are about 0.08 with corners
about 0.01) or a fill above 0.65 (a disk). Letters on the sample are
about 30 px. An arrow's radial variation is about 0.18 to 0.20, so it
is not a circle.

Direction is which edge the ink occupies, in a band of
`max(2, min(w,h)/10)`. The base is the edge the ink runs along, and the
point is the other end. Splitting a chevron into thirds finds almost
the same ink in each third (about 1.07 to 1.18) and missed every arrow
on the sample. The sample's left glyph, about 56 by 58, measures 0.22 on
the left edge and 0.43 on the right, so it is a `left arrow`; the right
glyphs are the mirror. The ratio floors the smaller fraction at 0.02
before dividing: adding 0.05 instead made that glyph 1.59, under the
1.6 cutoff, and the arrows vanished. When the horizontal ratio is at
least the vertical one and at least 1.6, more ink on the left is a
`right arrow` and more on the right is a `left arrow`. When the
vertical ratio is greater and at least 1.6, more ink on the top is a
`down arrow` and more on the bottom an `up arrow`. A circle's four
edges sit near 0.56 to 0.58, a ratio of about 0.95, and the circle's
own component is skipped by id.

A mark whose centre is within 0.7 of a circle's radius is that
button's arrow and may be 24 px on a side. Any other arrow must be at
least 70 px on its short side: at 40, the letters of a word drawn at
scale 8 (about 40 by 48) were reported as arrows. The aspect must lie
between 1/3.5 and 3.5, which also drops the long top arc of a button
(one measured 142 by 27).

An outlined box is a wide button or a rectangular frame. A button is at
least 90 wide and 28 to 90 tall, with an aspect of at least 2.8, at
least as many dark pixels as its width, and a fill under 0.85. The
sample's buttons are about 144 by 41 to 44 with a fill of about 0.66 at
luminance under 210. A frame is at least 40 by 30, each edge at least
0.55 covered in a 3 px band at threshold 205, and its inner fifths
under 0.2 dark.

## Limits found drawing test pictures for buttons

These were found on 2026-10-01 while drawing the pictures for the
runner's button tests ([Buttons](slate-runner.md#buttons)). The
pictures were drawn scenes, as in the tests above, and not a live HUD,
so they say what the finder does on those and not on a product's
interface.

- tesseract did not read a word inside a 4 px outline, and did read one
  inside a 2 px outline.
- A solid filled rectangle is not an outlined box, because a button
  box must be under 0.85 dark. A pale ellipse 160 by 50 px with a dark
  28 px bold word in it was read as both the word and a box, before and
  after the texture's compression and on the grid
  ([Ninth round](slate-runner.md#ninth-round-wave-2-end-to-end)).
- A small round letter `O`, about 5 px wide, was itself found as an
  outlined box. A `box` request therefore finds more than a button's
  outline when the text is small, and a `text ... box` button can be
  ambiguous because of it.

- On 2026-10-01 a live face picture, read from the grid, misread one
  label that the full texture read correctly, so a label's reading can
  depend on how much of the texture the face shows.

The button tests therefore draw thin frames and 9-row glyphs.

For an author this means two things. Prefer `text` alone when the label
is unique on the face. Expect `box` to find more than the button's
outline in small text, and do not read a count of boxes as a count of
buttons.

## What the tests expect

`TestADrawnPicture` draws a ring, a right-pointing triangle, a
left-pointing triangle, an up-pointing triangle, a rectangular frame
and the word Menu, and checks one of each plus a `right arrow` request;
the pattern `^M` matches the one word. `TestFiguresOnADarkPage` draws a
white ring and a white frame on black, which is the inverting path, and
asks only for a circle and a box, so tesseract is not involved. The
others cover a file decoded by `FindFile` and one that is missing or is
no picture, a bad request, no image and a blank page. No test
reads the sample, and none could be added that did without putting it
in the repository.

Small marks at the sample's edges read as noise on some passes, so no
test asked for a total count of every word.

## The command

`cmd/slpic` is `imgfind` and the face pictures from a shell. The binary
is `slpic`.

```
slpic faces [-addr ADDR] [-agent NAME] [-raw] OBJECT DIR
slpic find IMAGE text TEXT
slpic find IMAGE pattern EXPR
slpic find IMAGE drawing NAME
slpic find IMAGE box
```

`faces` finds the object with `ObjectsNamed`, which must return exactly
one, and writes `DIR/N.png` for each textured face from
`Session.FacePicture`, or the texture as uploaded with `-raw`, read with
`Faces` and `TextureImage`. A face with no texture is left out of the
list of paths it prints, and an object with no texture at all is an
error. A planar face or one whose texture is animating is an error,
except with `-raw`. `find` runs `FindFile`; a request that matches nothing prints
nothing and the command succeeds. Each match is one tab-separated line:
the kind, the query, the contents, the top-left, the width, the height
and the centre, with the query and the contents quoted.

`-addr` is where slgod is, and `$SLGO_ADDR` is the same answer when the
flag is empty. With neither, `sl-host` is asked about the avatar
(`-agent`, or `$SLGO_AGENT`) the way `slsh` asks, and a machine without
`sl-host` uses `localhost:7807`.

## Tried and not used

Asking a local vision model for a box and a centre was looked at. `Item`
carries a box and a centre for that reason, but `Find` calls no
model and the package adds no module requirement. The macOS Vision
framework was tried for reading text and failed on the sample with an
image reader error, so the fallback, tesseract, ran; the package goes
straight to tesseract, and there is no cgo.
