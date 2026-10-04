# Slate 1: the language

Slate is a language for testing Second Life products. A `.slate` file names the prims it will drive, then holds one test or several, each a list of steps. A step is one stimulus (a touch, a chat line, a payment, a sit, a drag, a dialog answer, a link message) and the effects that stimulus must produce. The effect is often on a different prim from the one that was touched: a button on a HUD changes a face on a sign, and the test says so.

This page is the reference for someone writing a test by hand. How a runner implements it is in [the runner design](slate-runner.md). Where a runner and this page disagree, the runner is wrong until the page is revised. The runner design's [Step lifecycle](slate-runner.md#step-lifecycle) states the same timing rules precisely, and the two must agree.

A runner takes one avatar, the tester, from an already-running slgod. It finds each declared prim by name, performs the stimuli as that avatar, watches chat, dialogs, messages, object changes and inventory, and prints a transcript and an exit code. A missing effect fails the test when its deadline passes. The run does not hang.

Link messages do not leave a linkset. A HUD that changes another object is observed as an effect on that other object (a texture, a chat line, a rez, a give), never as a link message crossing from the HUD to the sign. The only link messages a test can see are those a probe script inside the linkset reports.

## Goals and non-goals

A person can write a test after reading this page, with no Go and no pixel coordinates for an ordinary button. One stimulus can require several effects, on the prim touched or on another, and newly rezzed objects can be named for the rest of the file. A test can wear a HUD or another attachment from the tester's inventory, watch a button appear and disappear, and take the attachment off again. Every wait has a deadline.

Slate does not run more than one avatar (the syntax can name another; the runner does not log one in), drive the camera, walk or teleport, check animations, particles, sounds or the product's inventory, edit the product's scripts, or delete objects, refund payments or remove items a run produced. A permission request is printed and refused with no bits granted, so the product script is not left waiting. A file that does not start with `slate 1` is rejected.

## A first script

A script is UTF-8. The first statement is `slate 1`. Headers follow, then steps. (A file may instead hold several named tests; see [Tests, sequences, before and after](#tests-sequences-before-and-after).) Blank lines do not matter. `#` starts a comment that runs to the end of the line, except inside a string.

```slate
slate 1
timeout 10s

object hud is "Test HUD"
object sign is "Example Sign"

touch hud button text "Open"
expect texture sign face 0 becomes 47227e57-7e57-c0de-1611-3fe69d620af1 within 8s
expect offset sign face 0 is 0.25 0 within 8s
expect repeats sign face 0 is 2 1 within 8s
```

`hud` and `sign` are names in the script. `"Test HUD"` and `"Example Sign"` are the names of prims in the region, matched exactly and case-sensitively. Each must find one prim. Zero prims, or more than one, is a setup failure before any step runs, unless a `description` tells objects of one name apart ([Objects](#objects-names-and-link-numbers)).

A step is one stimulus and the expectations that follow it, up to the next stimulus, a `do`, or `then`. The expectations in a step are a set: they may arrive in any order, and all must arrive before the deadline. Here the touch is on `hud`; the three expectations are on `sign`.

## Lexical grammar

At each byte the lexer tries these in order and takes the first that matches: a comment, whitespace, a string, a duration, a UUID, the two-character token `L$`, a capture, the single-byte tokens `{` and `}`, a float, an integer, a word. Anything else is an illegal byte. A word is one kind of token: the lexer does not know which words mean something.

```ebnf
source     = utf8 without U+0000
whitespace = { " " | "\t" | "\r" | "\n" }
comment    = "#" { byte except "\n" and U+0000 }
string     = '"' { stringchar } '"'
stringchar = byte except '"', "\", U+0000, "\n", "\r"
           | "\\" ( '"' | "\" | "n" | "t" )
duration   = digits unit                 (* no space before the unit *)
unit       = "ms" | "s" | "m"
uuid       = hex8 "-" hex4 "-" hex4 "-" hex4 "-" hex12
hex8       = 8 * hex
hex4       = 4 * hex
hex12      = 12 * hex
hex        = "0".."9" | "a".."f" | "A".."F"
float      = [ "-" ] digits "." digits
integer    = [ "-" ] digits
digits     = "0".."9" { "0".."9" }
ident      = letter { letter | digit | "_" | "-" }
letter     = "a".."z" | "A".."Z"
capture    = "$" letter { letter | digit | "_" }   (* one token *)
brace      = "{" | "}"                (* single-byte tokens *)
```

A string escape is one of `\"`, `\\`, `\n`, `\t`; any other backslash is a lexical error. A string cannot contain a raw newline or a NUL, because chat and LSL strings are cut at the first NUL and a raw newline would break the probe's one-line framing.

A duration is one token: `10s`, `500ms`, `1m`. `10 s` is an integer and an identifier, and it is a parse error. `1.5s` is the float `1.5` and the identifier `s`; where a duration is required that pair is a static error, `write 1500ms, not 1.5s; a duration is whole digits and a unit, with no dot`. `.5` is not a float, because digits are required on both sides of the dot; the lexer rejects it with `a number needs digits on both sides of the dot`.

A UUID is one token, so a leading digit does not split it; hex is case-insensitive. `L$` is one token, and `L$5` and `L$ 5` are both legal. An integer used as a channel must be in −2147483648 to 2147483647; outside that is a static error at the use, since the same token is a link number and a payment. A float that does not fit in 64 bits is a lexical error.

No word is reserved. The words below have a meaning, and each has it only where the grammar expects that word; anywhere else it is an ordinary word and can be a name. Within one production a word position is either a fixed word or a name, never a choice between the two, so the parser needs no list of forbidden names. An object binding, a probe, a sequence name, a `do` target and the name a rez binds with `as` each take any word: `object button is "Test HUD"` and `touch button button text "Open"` are both legal. A step begins with a stimulus word, `expect`, `then` or `do`, and the file begins with `slate 1`. An error message names the word that was expected. The in-world name is a string, so a prim may be called anything. `{` and `}` are tokens, not words. `null` is the UUID `00000000-0000-0000-0000-000000000000`. The words with a meaning are:

```text
slate timeout allow pay object is probe listen
touch anywhere link face at button text pattern symbol image box circle oval
drag from to over press dwell say on as tester owner of avatar anyone
public debug direct sit stand wait choose answer send num key
expect no within then dialog textbox texture offset repeats rotation
position size click give rez name description heard by reason only null
all others children this root
buy play open media zoom disabled none
matching becomes changes original
test before after each sequence do
showing count any fullbright glow colour alpha on off
item wear take attached ordered sorted shown gone if in
```

A capture is a `$` and a name: `$first`, `$tile_2`. It is its own token and not a word, so `$button` and an object called `button` never meet. `$` is a capture only before a letter. `L$5` and `L$ 5` keep their meaning because `L$` is tried first, and a `$` inside a string is an ordinary character, so `"L$5"` and the pattern `"L\\$[0-9]+"` are unchanged. A bare `$` is an illegal byte.

An illegal byte (including `;`) is a lexical error at that byte. Slate has no significant indentation.

## Syntactic grammar

This is a PEG. Alternatives are tried in the order written. Repetition is greedy and stops when the repeated element does not match. That is what makes a stimulus or `then` start a new step instead of being swallowed by the previous step's expectations.

```ebnf
script      = "slate" "1" header* ( suite / body )
header      = timeout / allow / objectDecl / itemDecl / probe / listen
timeout     = "timeout" duration
allow       = "allow" "pay"
objectDecl  = "object" ident "is" string ( "description" text )?
itemDecl    = "item" ident "is" string "in" string   (* item name, top-level folder *)
probe       = "probe" ident
listen      = "listen" integer

suite       = toplevel+
toplevel    = before / after / sequence / test
before      = "before" "each" block
after       = "after" "each" block
sequence    = "sequence" ident block
test        = "test" string block
block       = "{" body "}"

body        = step+
step        = stimulus expectation*
            / "then" expectation+
            / expectation+
            / call
call        = "do" ident

stimulus    = touch / drag / say / pay / sit / stand / choose / answer / send / wait
            / wear / takeoff

touch       = "touch" binding target guard?
guard       = "if" "shown"
target      = "anywhere"
            / "link" integer refine?
            / refine
refine      = "anywhere"
            / "face" integer at?
            / "button" nth? part+ face?
            / "showing" shown at?
shown       = uuid / capture
nth         = integer
at          = "at" number number
face        = "face" integer
part        = "text" ( string / capture )
            / "pattern" string
            / "symbol" string
            / "image" string
            / "box"
            / "circle"
            / "oval"

drag        = "drag" binding ("link" integer)? "face" integer
              "from" number number "to" number number dragtimes
            / "drag" binding "on" "screen" "from" screenpoint
              ("to" / "by") number number dragtimes "settle"?
dragtimes   = ("over" duration)? ("press" duration)? ("dwell" duration)?
screenpoint = number number
            / ("link" integer)? "face" integer "at" number number
say         = "say" string "on" integer ("as" stimspeaker)?
stimspeaker = "tester" / "owner" "of" binding / "avatar" string
pay         = "pay" binding amount ("reason" string)?
amount      = "L$"? integer
sit         = "sit" binding
stand       = "stand"
wait        = "wait" duration
choose      = "choose" chooselabel "on" binding
chooselabel = string / "matching" string / "button" integer / capture
answer      = "answer" string "on" binding
send        = "send" "on" binding "from" "link" integer
              "to" "link" linktarget "num" integer "text" ( string / capture ) ("key" key)?
linktarget  = integer / "all" / "others" / "children" / "this" / "root"
key         = uuid / "null" / capture
wear        = "wear" ident "on" string "as" ident   (* item, attach point, new binding *)
takeoff     = "take" "off" binding

expectation = "expect" "no"? expectbody ("within" duration)? ("as" capture)?
expectbody  = sayexp / dialogexp / boxexp / textureexp / offsetexp
            / repeatsexp / rotationexp / positionexp / sizeexp / clickexp
            / textexp / fullbrightexp / glowexp / colourexp / alphaexp
            / buttonexp / attachexp
            / giveexp / rezexp / linkexp

text        = string / "matching" string / capture

sayexp      = "say" text "on" expchan "from" expspeaker
expchan     = integer / "public" / "owner" / "debug" / "direct"
expspeaker  = "tester" / "owner" "of" binding / "avatar" string
            / "object" binding link? / "anyone"
dialogexp   = "dialog" "from" binding link? ("text" text)? dbutton*
              "only"? "ordered"? ("count" integer)? sorted?
dbutton     = "button" integer? text
sorted      = "sorted" ( "matching" string )?
boxexp      = "textbox" "from" binding link? "text" text
faceall     = "face" ( integer / "all" )
textureexp  = "texture" binding link? faceall ( "changes" / state uuidval )
offsetexp   = "offset" binding link? faceall ( "changes" / state pairval )
repeatsexp  = "repeats" binding link? faceall ( "changes" / state pairval )
rotationexp = "rotation" binding link? faceall ( "changes" / state numval )
positionexp = "position" binding link? ( "changes" / state vecval )
sizeexp     = "size" binding link? ( "changes" / state vecval )
clickexp    = "click" binding link? ( "changes" / state clickval )
textexp     = "text" binding link? ( "changes" / state textval )
textval     = "original" / "any" / text      (* any only after is, with as *)
fullbrightexp = "fullbright" binding link? faceall ( "changes" / state onoff )
glowexp     = "glow" binding link? faceall ( "changes" / state numval )
colourexp   = "colour" binding link? faceall ( "changes" / state triple )
alphaexp    = "alpha" binding link? faceall ( "changes" / state numval )
buttonexp   = "button" binding link? part+ face? ( "changes" / state buttonval )
buttonval   = "shown" / "gone" / "count" integer / "original"
attachexp   = "attached" binding ( "on" string / "off" )
state       = "is" / "becomes"
named       = "original" / "any" / capture
uuidval     = uuid / named
pairval     = number number / named
numval      = number / named
clickval    = clickname / named
onoff       = "on" / "off" / named
triple      = number number number / named
vecval      = number number number / named
clickname   = "touch" / "none" / "sit" / "buy" / "pay" / "open"
            / "play" / "media" / "zoom" / "disabled"
giveexp     = "give" text "from" binding
rezexp      = "rez" "name" text ("description" text)? "from" binding ("as" ident)?
linkexp     = "link" "on" binding "from" "link" integer "num" integer
              "text" text ("key" key)? ("heard" "by" integer)?

link        = "link" integer
binding     = ident
number      = float / integer
```

The in-world name appears only in `objectDecl` and `itemDecl`; a step refers to a prim by `binding`, one identifier, and to an item only in `wear`. The word `object` in `from object sign` is not a use of either. A stimulus and the `expect` lines right after it are one step, so the step after a guarded touch is written `then expect ...`. `button` in `buttonexp` reads the parts of a touch. The parser accepts a number before them, as a touch has, so that the static check can say it is not legal there. A file may open with a step that has no stimulus (the third `step` alternative); its failure block prints `stimulus: (none)`. A bare `then` is rejected. A `call` is a step of its own and takes no expectations: an expectation after `do NAME` starts a new step.

`named` is a value that is not written out: `original` is the reading when the test began, `any` is a reading whatever it is, and a capture is a value an earlier step bound. The grammar lets all three stand wherever a value goes, and [Static checks](#static-checks) says where each is legal: `any` only after `is` and only with `as`, a capture only where its type fits. A rez names the object it finds with `as` followed by a name, before `within`; `as` followed by a `$` name, after `within`, is the capture of any other positive expectation. The parser reads `face`, `link` and `button` beside `showing` so that the static check can name the clash, and `showing` takes a UUID or a capture only.

A file is either a suite (top-level `before each`, `after each`, `sequence` and `test` blocks) or plain steps. A file that has both is a parse error. A plain-steps file is one test, named after the file; the PEG tries `suite` first, so a file that starts with a block and then has a bare step stops at the step and is rejected there. In `from object sign link 3` and in `dialog from vendor link 2`, `link 3` and `link 2` are the `link` production, not a stimulus. `number` accepts an integer as its exact value (`1` is `1.0`); a face, link, num or channel does not accept a float.

## Static checks

Static checks run after the parse, before anything is dialled. A failure is exit code 2 and prints `script:line:column:` and the reason. No region call has been made and nothing has been paid.

| Check | Rule |
|---|---|
| Shape | The first statement is `slate 1`. Every header precedes the first stimulus, expectation, `then`, `do` or top-level block. A file with no step and no test is an error. A plain-steps file cannot also have `before each`, `after each`, `sequence` or `test`; mixing the two forms is a parse error. |
| Timeout | At most one `timeout`. Absent means 10s. Any duration is at least 100ms (a shorter deadline could pass between the send and the first read) and at most 120s (so a typo cannot become an hour). |
| Pay gate | At most one `allow pay`. A `pay` without it is an error. |
| Objects | Binding identifiers are unique across objects, items and the names `wear` binds, and any word may be one. In-world name strings are non-empty. One in-world name may be written on several headers only when every one of them has a `description` and no two are written the same: `description "x"` and `description matching "x"` are different, two of either are not. Otherwise the error is `in-world name "N" is already used; objects of one name need a description each`, or `... is already used with that description`. |
| Descriptions | A description is a string or `matching "RE"`; the pattern compiles. A capture is refused, because a description is read at setup, before any step. The empty string is legal. |
| Items | An item binding is not an object: it is used only by `wear`, and any other use is `X is an item; only wear uses an item`. The item name and the folder name are non-empty after trimming. |
| Probes | The identifier is an object binding, with at most one probe per object. A probe has no channels; the runner picks them. |
| Listens | Each `listen` channel is a 32-bit integer, not 0 and not 2147483647. Duplicates are an error, and there are at most 63 of them. The bridge script opens one listen for its own control channel and one per `listen` channel; LSL allows 65 in one script (published, not measured here), and one is kept spare. |
| References | Every object in a step is a header binding, a name bound with `as` on a positive rez expectation in an earlier step of the same test, or a name bound by `wear`. A name bound in a step is not usable in that same step, except a name `wear` binds, which that step's own expectations may use. A name `take off` ends is not usable from the next step on: `h was taken off at line N; it cannot be used again in this test`. A capture is a different thing and has its own rows below. |
| Rez names | A positive rez expectation has `as`, a negative one does not. The identifier is not already a binding and is not bound twice in one test. |
| `as` scope | A name bound in `before each`, by a rez or by `wear`, is visible in the test and in `after each`. A name bound in a test body is not visible in `after each`, because the test may have failed before binding it. A name bound in a sequence is visible to the caller after the `do`. Each test is checked on its expanded steps (`before each`, the test with every `do` inlined, `after each`). |
| Speakers | A stimulus speaks `as tester` (the default), `as owner of` an object, or `as avatar`. `as object` and `anyone` are expectation-only. |
| Channels on `say` | A stimulus takes an integer. `public`, `owner`, `debug` and `direct` belong to expectations. An expectation on an integer other than 0 or 2147483647 requires a `listen` for it. `on 0` and `on public` are the same channel, as are `on 2147483647` and `on debug`. |
| Buttons | At least one part. `nth` is 1 or more. `pattern` is a regular expression in Go's RE2 syntax and must compile. A literal `text` is non-empty after trimming; a capture used as `text` is checked at the step. |
| Button readings | `expect button` takes the parts of a touch and they are checked as a touch's are, but not `nth`: `button N is not legal in an expectation: a reading is the count of the buttons found, not one of them`. `count` is 0 or more. `link N` needs no probe, as on every expectation ([Objects](#objects-names-and-link-numbers)). A reading takes `as $x` when it is positive, and the capture is a number. `image` and `oval` parts pass and fail at run time. |
| Guarded touch | `if shown` is legal only on a touch whose target is `button` (`if shown guards a touch of a button`), never with `button N` (the guard counts the buttons found, not one of them), and only in `before each` and `after each` (a test is one path, and a guard that may skip its touch makes two). A sequence is checked in the block that calls it. A guarded step has no expectations of its own, and the next step of the same block has a positive expectation, so that a label that is misspelt fails there and is not skipped on every run. |
| Link stimulus | `send` requires a probe on that object. A touch or drag that names `link` does not: the store numbers the links when there is no probe. |
| Link on an expectation | `link N` on a touch, a drag or an expectation (`from object OBJ link N`, `dialog from`, `textbox from`, and the state expectations) needs no probe, and N is 0 or more. A probe is needed only where the probe itself is the mechanism, which is the link messages: `send on OBJ ...` (and so its `from link A`) and `expect link on OBJ ...`; each is refused with `<what> OBJ needs a probe`. |
| Tests | A `test` name is a non-empty string, unique in the file, and there is at least one test in a suite. At most one `before each` and at most one `after each`. Top-level items may come in any order. |
| Sequences | Sequence names are unique. A `do` names a defined sequence (it may be defined after its use). A sequence may `do` another but not cyclically; a cycle or an undefined name is an error. The other checks run on each test's expanded steps, so a sequence no test calls is checked only for its `do` calls. |
| Matching | A `matching` pattern is a regular expression in Go's RE2 syntax and must compile. `matching` is legal only where the grammar writes `text`: `say`, `dialog` and `textbox` message, a `dialog` button clause, `give`, `rez` name and description, link text, a floating text expectation, and an object's `description`; and in `choose matching` and `sorted matching`, which take a string. |
| State words | `becomes`, `changes`, `original` and `any` are legal only on the state expectations (texture, offset, repeats, rotation, position, size, click, text, fullbright, glow, colour, alpha, and the button reading, which has no `any`). `changes` takes no value; `is` and `becomes` require one. |
| Length | A `say` on a negative channel is at most 254 bytes (it travels as a dialog reply). A `say` on any other channel is at most 1023 bytes, so the chat field does not cut it. An `answer` body is at most 254 bytes. A `send` text, once quoted, must leave the whole relayed control line within 1023 bytes, because the tester sends the command to the worn bridge as one chat line on a positive channel (measured: a 1000-byte line arrived whole). `choose` has no length check; a label the dialog did not offer fails at the step. A capture used in `send` text is checked at the step, before anything is sent. |
| Link text | A `send` text, and a link expectation's literal text, is printable ASCII (0x20 to 0x7E) plus tab and newline, written `\t` and `\n`. The probe's length check is then a character count, and any other character is a static error rather than a mangled report. A capture used as link text is checked at the step. A `matching` pattern on a link expectation is not limited to that character set, because only a literal is put on the wire. |
| Floats | An offset or rotation literal lies in [−1, 1]. Repeats are unrestricted. `original`, `any` and a capture are not literals and are not range-checked. |
| Position and size | A `position` or `size` literal is three numbers. A position may be any number, negative or zero. Each of the three numbers of a size is above 0: `size 0 is not above 0`. `link N` is checked as on every expectation, and neither has a `face`. |
| Floating text | A `text` expectation takes a string, `matching "RE"`, `original`, `any` (after `is`, with `as`) or a capture. A pattern must compile, and its named groups bind text as in any other `matching` clause. A capture must be text: one bound by `say`, `dialog`, `textbox`, `give` or another `text`, and a text capture is usable here and wherever text is. `link N` is checked as on every expectation, and there is no `face`. |
| Levels | A glow, alpha or colour literal lies in [0, 1]; each of the three numbers of a colour is checked alone. The error names the property and the number: `glow 1.5 is outside 0 to 1`. |
| Origin | `at 0 0` is an error (on `face` and on `showing` alike), and so is a drag whose `from` or `to` is `0 0`, whether written `0` or `0.0`. The error is `at 0 0 is the middle of the face; placeTouches treats a zero ST as not given (sl/touch.go)`. The touch at 0 0 is treated as the middle of the face, so it is rejected. `at 0 0.5` is legal. |
| Drag | A `drag … over D` must fit its step's budget: `D` is at most the longest `within` in the step, or the `timeout` when it has none, because the drag blocks for `D` and the step gives a blocking stimulus no more than that. With `press` or `dwell` the three together must fit (`over` counting 500 ms when omitted): `drag a face 0 from 0.1 0.5 to 0.9 0.5 over 4s press 4s dwell 4s` in a step that allows 10 s is refused with `drag over 4s, press 4s, dwell 4s can take 12s, which is longer than the step's budget of 10s, ...`. |
| Wait | `wait D` is a duration in the usual range, and must fit its step's budget as a drag's `over` does: `wait 12s` in a step that allows 10 s is refused with `wait 12s is longer than the step's budget of 10s, ...`. |
| Drag on the screen | The same budget holds for `drag OBJ on screen`, and `settle` adds the longest it may wait for the HUD to change, `Options.HUDChangeTimeout`, which is 5 s unless a session is given another: `D` (500 ms when `over` is omitted), any `press` and `dwell`, plus 5 s must fit. `drag h on screen ... over 6s settle` in a step that allows 8 s is refused, with `drag over 6s and settle (up to 5s) can take 11s, which is longer than the step's budget of 8s`. The origin rule does not apply: 0 0 is a corner of the screen, and `at 0 0` a corner of a face. |
| Binding on a HUD point | `drag OBJ on screen` needs an object worn on a HUD point (attachment points 31 to 38). Where the file says how the binding was put on, that is refused when it is checked: a binding `wear` put on a point that is not a HUD one (`h is worn on chest; drag on screen needs an object worn on a HUD point`), and one a `rez` expectation bound (`made is rezzed in the world; ...`). A header binding, which may be worn or not, is judged when the step runs ([Drag on the screen](#stimuli)). |
| Money | A pay amount is an integer of at least 1. A reason is at most 127 bytes. |
| Dialog buttons | A `button N` in a dialog expectation, and in `choose button N`, is 1 to 12, the most buttons `llDialog` accepts (published, not measured). `count` is 1 to 12. When `count` is written, the number of `button` clauses is at most `count`, since each clause needs a button of its own. Two clauses may not be pinned to the same `N`. With `only` and `count N` there are exactly N clauses, because `only` gives every button to a clause. |
| Ordered and sorted | `ordered` needs at least two button clauses. A `sorted matching` pattern compiles and has at most one capturing group, the text to compare. A named group of a `sorted` pattern binds nothing. |
| Dialog shape | `choose` and `answer` are not checked against a preceding dialog. That depends on the region and fails at run time; only the range of `button N` and the pattern of `matching` are checked. |
| Wear | The first name of `wear` is an item (`X is an object; wear takes an item`, or `X is not an item`). The attach point, in `wear` and in `attached ... on`, is a name `sl` knows, in any case and with the viewer's spelling or `sl`'s; a name it does not know is refused, not guessed at. The name after `as` is new, and unique among objects, items and the other names `wear` and `rez` bind. A `take off` and an `attached` name an object binding, which is a header object or a name `wear` bound. |
| Capture types | A capture has the type of the place that binds it, and a use must be of the same type. The types are text (a line, a message, an item name, a group, a button label, a floating text), uuid (a texture), pair (offset, repeats), number (rotation, glow, alpha, the count of a button reading), click, colour triple, vector (position, size; a position capture can be used by a size and the other way round) and on or off (fullbright). Text is never accepted where a UUID is expected, even when it looks like one. `key` and `showing` take a uuid capture; `text`, `choose`, `send` text and a dialog button take a text capture. `choose matching $x` is refused, because a capture is data and never a pattern, and a capture is not usable inside a `matching` pattern. |
| Capture scope | The `as` rows above, for captures, in a separate namespace written with `$`: a capture is usable from the next step on and never in the step that binds it, it is visible after a `do`, it is visible from `before each` into the test and into `after each`, and it is not visible from a test body into `after each`. An object and a capture may share a word, since `$a` is not `a`. Each test is checked on its expanded steps. |
| Bound once | A capture is bound once per expanded test, by a positive expectation only, so a sequence that captures can be called once per test. A second binding, in the same step or a later one, is `$t is already bound at line N; a capture is bound once per test`, and when it comes through a `do` the message ends with the test and the calls, innermost first. A negative expectation matches nothing to bind, so `as` on `expect no` is an error; so is `as $x` on a rez or a link, which have nothing to bind. |
| Named groups | A `matching` pattern of a positive expectation binds each of its named groups, `(?P<name>...)`, as the text capture `$name`. A group name that appears twice in one pattern is an error, since RE2 allows it and the binding would be ambiguous. A name that cannot be written as a capture (it must begin with a letter and hold letters, digits and underscores) is an error. |
| `is any` | `any` is legal only after `is`, never after `becomes`, and only on an expectation that has `as $x`, since it asserts nothing and exists only to capture the reading. |
| Face all captures | A capture bound by a `face all` expectation holds every face, and only a `face all` expectation of the same type can use it. A capture bound for one face is refused by a `face all`, and a `face all` capture is refused anywhere else. |
| Showing | `showing` cannot be combined with `link`, `face`, `button` or `anywhere`; it finds the prim and the face itself. Its UUID or capture is a texture, and a capture must be uuid. `any` and `original` are refused after it. |

`image` and `oval` pass the static check and fail at run time; see [Buttons](#buttons).

## Steps and timing

This section states the timing rules once. [Step lifecycle](slate-runner.md#step-lifecycle) gives the same rules as a state machine for the implementer.

**A step and a set.** The expectations in a step are a set. They may arrive in any order. An event can satisfy only one expectation, and is offered to the unmatched expectations in source order, so two expectations that ask for the same chat line need two chat lines. Steps run one at a time.

**Arm point.** A step sees only events observed at or after its arm point that no earlier expectation consumed. The runner keeps one log of events for the whole run ([Event log and arming](slate-runner.md#event-log-and-arming)).

- A step that begins with a stimulus arms just before the stimulus is sent; for a `wear`, before the request to wear. The first step of each test arms when that test starts, so events from an earlier test are never eligible.
- A `then` step, or an expectation-only step after another step, arms at the latest of: the previous step's arm point, the time its stimulus returned, and the observation time of every event its positive expectations matched. It starts when the previous step passed. An event that arrives while the previous step is still in a negative window, a rez settle or a click wait is therefore not lost.
- An expectation-only first step arms when its test started (for a plain-steps file, when setup finished).

**`then` and ordering.** `then` means "after what the previous set matched". It can order only events whose order the grid preserves. Reports from two different prims have no guaranteed relative order. A texture update and a chat line from a script are not ordered by any promise the region makes either. Write `then` only for a real sequence; otherwise put the expectations in one set.

**Timeouts.** The default is 10 seconds, from the `timeout` header or the built-in value. `within D` replaces it for that expectation only. A step's deadline is its start time, plus the time any blocking stimulus actually blocked on the way to success, plus the longest expectation duration in the step. A positive expectation still unmatched when its own duration has elapsed fails the step at that moment, even if another has time left. A negative expectation fails the step the moment the forbidden event is seen.

**Stimulus budget.** A blocking stimulus (`drag`, `sit`, `stand`, `wait`, `pay`, `wear`, `take off`) may take the longest `within` in its step, or the default if there is none; a step with no expectations passes when the stimulus returns inside that budget. If the stimulus fails, the step fails at once. Setup, the tester-position read and the 30 s click wait are not on the step's deadline; setup budgets are under [Reading the result](#reading-the-result).

**When a step passes.** With only positive expectations, as soon as all have matched. With any negative expectation, after the positives have matched and every negative window has elapsed. A step with no expectations passes when the stimulus succeeds; one with only negative expectations passes when its windows elapse. A step with a positive `rez` waits one more 250 ms object poll before passing, and a second prim that matches the same claim in that wait fails the step. A click expectation on a name bound with `as` makes the rez step wait up to 30 s to read the click action; see [Step lifecycle](slate-runner.md#step-lifecycle).

**Dialog hold.** A dialog matched by `expect dialog` or `expect textbox` is held for that object until a `choose` or `answer` on that object consumes it, a later matched dialog from the same object replaces it, or its test ends. It is not dropped when the step that matched it ends, so a `choose` in the next step finds it. At the end of a test an unconsumed hold is forgotten and reported on that test's `dialog left unanswered:` line.

## Tests, sequences, before and after

A file is either plain steps or a suite. Plain steps are one test, named after the file's base name without `.slate`. A suite holds named tests and the blocks that support them. The two forms cannot be mixed. Timing inside any test is as under [Steps and timing](#steps-and-timing).

```slate
sequence NAME { ... }
before each { ... }
after each { ... }
test "NAME" { ... }
```

**Tests.** A `test "name" { steps }` block is one test. Names are non-empty and unique in the file, and a suite has at least one. Top-level items may come in any order. Tests run in file order.

**Before each and after each.** At most one of each. Every test runs `before each`'s steps, then its own, then `after each`'s, as one sequence of steps numbered from 1; the transcript's `pass step N` counts through all three. `before each` should put the world into the state each test needs, and `after each` should undo what the tests do (for example `stand`, or `take off` for what `wear` put on). The runner itself never stands, answers, retries a payment, takes off or deletes.

**Sequences.** `sequence NAME { steps }` names a list of steps that tests share. `do NAME` inlines them at that point, and a sequence may be defined after the `do` that uses it. A `do` is a step boundary: the step before it is closed, and a `then` after it arms from the sequence's last step. A sequence may `do` another, but not cyclically. A `do` of an undefined name, or a cycle, is a static error. Static checks apply to the expanded steps of each test.

**After a failed test.** If a step fails, the test fails and its remaining steps are skipped. `after each` still runs; if it fails, that is reported as an `after each` failure of the same test. The runner then goes on to the next test, with the world left as the failure left it. Each test's `before each` is what makes the next one start from a known state.

**What stops every test.** A setup failure (exit 3), or a click byte still unknown when its 30 s wait ends, because both say the environment is wrong. A dropped chat or instant-message subscription also stops the run (exit 1). Probe install, wearing the bridge and their removal happen once per file, not once per test.

**Bindings.** A name bound with `as` is scoped to one test run. `before each`'s bindings are visible in the test and in `after each`. A name bound in a test body, or in a sequence it calls, is visible to the rest of that test but not in `after each`, because the test may have failed before it was bound. A static check enforces this.

**Dialog hold and arming.** A held dialog ends with its test; an unconsumed hold is reported on that test's result. The first step of each test arms when the test starts. Events from an earlier test are never eligible, so a `before each` that sets up state is not satisfied by what the previous test left behind.

**Running some tests.** The command takes `-run REGEX` and runs only the tests whose names match (RE2, unanchored). Each still runs with `before each` and `after each`. A pattern that does not compile, or that matches no test, is exit 4 and nothing is dialled. The command line itself is in [the runner design](slate-runner.md).

```slate
slate 1

object hud is "Test HUD"

sequence press-open {
  touch hud button text "Open"
}

before each {
  touch hud button text "Reset"
}

test "first" {
  do press-open
}
```

Here `before each` runs first in every test, then `do press-open` presses the button. With `-run "^first$"` only that test runs.

## Speakers and channels

The runner drives one avatar, the tester.

| Phrase | On a stimulus | On an expectation |
|---|---|---|
| `tester` | The default. The avatar speaks or touches. | The speaker is the tester. |
| `owner of OBJ` | Legal only when the tester owns `OBJ`; the tester then speaks, being the owner. Otherwise the step fails before anything is said. A group-deeded object fails this check; write `as tester`. If the owner is not known the step fails with `the owner of OBJ is not known`. | The speaker is `OBJ`'s owner. |
| `avatar "Name"` | Legal in the file. At the step, the name is compared with the tester's displayed name, whole string, ignoring case. If it is not the tester, the step fails with `this runner drives only the tester` and does not speak. | The speaker's whole name equals it, ignoring case. A prefix or one half of a display name does not match. |
| `object OBJ` | Cannot be written. | The speaker is any prim of `OBJ`'s linkset. |
| `object OBJ link N` | Cannot be written. | The speaker is the prim at link number N, from the probe when `OBJ` has one and else from the object store, found when each line arrives ([Objects](#objects-names-and-link-numbers)). |
| `anyone` | Cannot be written. | Any speaker except a probe protocol line. |

Chat text written as a string is matched exactly and case-sensitively, not as a substring. Written `matching "RE"` it is a pattern; see [Expectations](#expectations). Protocol lines the bridge produces never satisfy `say` and never trip `expect no say`.

| Written on an expectation | What matches |
|---|---|
| `on public` or `on 0` | Whisper, say or shout. Chat the avatar hears carries no channel number, so this is all that is delivered for channel 0. |
| `on owner` | `llOwnerSay`. It is not a numbered channel. |
| `on debug` or `on 2147483647` | Debug-channel chat. |
| `on direct` | Chat sent directly to the avatar. |
| `on N`, any other integer | A script's listen on N, forwarded by the bridge. The file must declare `listen N`. The text is the raw tail of what the script said, not quoted and not unescaped. |
| (not writable) | Region chat. The transcript prints it as `chat region from ...`. It satisfies no `say` expectation and trips no `expect no say`. |

A stimulus `say "menu" on -7` is sent as a dialog reply from the avatar, at most 254 bytes. A positive channel, including 0, is ordinary say. A negative channel cannot be typed in the chat bar, so authors should prefer it for menus.

**The 20 m rule.** A `say` is ordinary chat, not as far as the region lookup can see. Before a `say` is sent, the runner measures the distance from the tester to every binding that step names (the `as owner of` binding and each binding an expectation names). A binding more than 20 m away, measured to the root of its linkset, fails the step before the say. The say radius is 20 m, measured ([Measurements](slate-runner.md#measurements)). `on owner` expectations, `send` and bindings the step does not name are not checked. The tester's position must be known exactly; see [Tester position](slate-runner.md#tester-position).

## Stimuli

**Touch.** `touch OBJ anywhere` touches face 0 at its middle; the simulator does not check that the point is on the mesh. `touch OBJ face N` touches face N at its middle, and `touch OBJ face N at S T` touches the point `(S, T)` on it. `touch OBJ link N` touches the prim at link number N ([Objects](#objects-names-and-link-numbers)); a refine may follow it. The prim is found when the step is prepared, and a link the set does not have, or a set whose order is not known, fails the step before a touch is sent ([Reading the result](#reading-the-result)). For `button`, see [Buttons](#buttons). Every touch is a click: a grab followed at once by a release.

**Guarded touch.** `touch OBJ button PARTS if shown` is a touch of a button that may be absent. It is for `before each` and `after each`, which put the world into the state each test needs: press `Close` if the product is open, and do nothing if it is not. It is legal only there and only on a `button` target without a number ([Static checks](#static-checks)). The search is the touch's, on every face, and the outcome depends on how many tuples it finds:

| Tuples | What happens |
|---|---|
| none | Nothing is sent and the step passes. It prints `slate: step N: no <parts as written> button on "<name>"; the touch was not sent`. |
| one | The touch is sent as an unguarded touch is. |
| two or more | The step fails and lists the centres, as an unguarded touch does. |

A face the search refuses (planar, animated, a finder error, `image` or `oval`) fails the step, and is not a count of none. A face with no texture contributes no tuples. A prim with no textured face therefore counts as none and sends nothing, where an unguarded touch refuses it with `no face of "<name>" has a texture`. A guarded step has no expectations, so the step after it is written `then expect ...` and holds a positive expectation, naming the state the block is driving towards: a misspelt label then fails at that expectation instead of being skipped silently on every run.

**Touch showing.** `touch OBJ showing UUID` touches the face that shows a texture, and `touch OBJ showing $tile` the face that shows the texture a capture holds. It is for a palette with one picture on each face, where the label that a button search would use is not there. The runner first asks the region to describe every prim of `OBJ`'s linkset again and reads their faces, because the store can keep an old texture for seconds after a script changes a face ([Texture](slate-runner.md#texture)). It then looks at every face of every prim of the linkset for the texture: exactly the faces a prim has, counted from its shape ([How many faces a prim has](objects.md#how-many-faces-a-prim-has)), or, for a sculpt or a mesh, the faces its texture entry names. Exactly one (prim, face) must show it, and that face is touched at its middle, or at `S T` when `at S T` follows. A texture shown nowhere, or on two or more faces, fails the step before any touch is sent, and the failure lists each prim and face ([Reading the result](#reading-the-result)). `OBJ` needs no probe: the touch goes to the prim found by its id. For a sculpt or a mesh the face count is not known, and a face the entry does not name shows the default texture, which can make a search for that texture ambiguous; see [Known limitations](#known-limitations).

**Drag.** `drag OBJ face N from S0 T0 to S1 T1 over D` presses at the first point, moves to the second over `D` (500 ms if `over` is omitted) and releases. The release is always sent, even if the step is cancelled. The step starts its expectations only after the drag returns. `link N` selects the prim as a touch does. One segment is the whole path.

`press D` holds still at the first point for `D` before moving, and `dwell D` at the last before letting go; without them the drag moves at once and lets go at once. A script that reads a drag from its touch events, as many that move or stretch a picture do, sees the cursor stop only if it dwells: without a dwell the last updates can be lost to the release, and the amount the script works out comes up short. Each is a duration like `over`'s, and the three together count against the stimulus budget.

**Drag on the screen.** `drag OBJ on screen from POINT (to X Y | by DX DY) [over D] [settle]` is the drag a mouse makes on a worn HUD: the cursor is pressed on the screen where the start is, moved to the end over `D` (500 ms if `over` is omitted), and released. The viewer sends the touch of whatever prim is in front at the start, and the rest of the drag is on that prim alone, so a HUD that moves or resizes by the change in `llDetectedTouchPos` sees what it sees under a mouse ([Where a worn HUD is on the screen](hud-screen.md#a-drag)). The start is one of

- `X Y`, a point in the world view, in pixels from its top left corner, X to the right and Y down; decimals are allowed; or
- `[link N] face F at S T`, the point on face `F` of the binding's linkset (of its root unless `link N` names a prim) that `S T` is, turned into pixels when the step is prepared. It spares the author from knowing pixels: `drag hud on screen from face 0 at 0.5 0.9 by 300 200` grabs the HUD by the lower middle of its background wherever the HUD is.

The end is `to X Y`, a point in the same pixels, or `by DX DY`, a distance from the start, so that "move it 300 pixels right" needs no pixels at all. A negative number is a move left or up. `settle` waits after the press, up to `Options.HUDChangeTimeout` (5 s), for the region to say the prim pressed has changed, before the cursor moves: a HUD that grows a transparent prim over the screen when pressed to keep the cursor on it needs it, and without it the cursor leaves the prim at once. A HUD that does not change fails the step after that wait. The release is always sent.

The pixels are those of a virtual world view, which the run states and not the file: `--screen WxH` (1920x1025 unless said, a 1920x1080 window less Firestorm's menu bar) and `--hud-zoom Z` (1 unless said) ([Package and command](slate-runner.md#package-and-command)). Pixels, and not fractions of the screen, because where a HUD sits depends on the world view's shape ([The world view](hud-screen.md#the-world-view)), so a fraction would not make a test independent of the screen; a stated size of screen is honest, and it is what a screenshot and the measurements are in. The face form is for a test that wants none of it. `OBJ` must be worn on a HUD point; one that is not fails the step with `slate: step N: "<name>" is worn on chest; a drag on the screen needs an object worn on a HUD point, and nothing was sent`, or `is not worn`. A face the prim cannot show, or a shape the runner cannot place (only a plain box or cylinder can be), fails it with a sentence too, and nothing is sent.

**Pay.** `pay OBJ L$5 reason "tip"` pays the object. The amount is an integer of at least 1. Omitting `reason` sends the object's name, as the viewer does. Before paying the runner prints `pay L$<amount> to "<name>" <uuid> reason "<reason>"`. A payment the balance cannot cover is refused before it is sent. A refused or unconfirmed payment fails the step with that message, which says whether the balance moved, and the expectations do not run. A payment is never sent twice.

Both `allow pay` in the file and the `--pay` flag on the process are required. The header without the flag fails at the pay step with `paying is off`. The flag without the header is the static error. A copied script does not pay because the process was started with the flag, and a process started without the flag does not pay because the script asked.

**Wait.** `wait D` does nothing for `D` and sends nothing: it is for a product that ignores a touch coming too soon after the last one, or a script that has to finish something first. It is a blocking stimulus, so `D` must fit the step's budget, and expectations in its step start when it returns. A wait says what it is for where `expect no ... within D` would read as a check.

**Sit and stand.** `sit OBJ` succeeds when the avatar has been seated on the object, and leaves it seated until a `stand`. A refusal fails with the simulator's text; a timeout fails with a message that the sit may have taken. `stand` succeeds when the request returns without error, including when the avatar was already standing.

**Choose.** `choose` presses a button on the dialog held for `OBJ` ([Dialog hold](#steps-and-timing)). It has four forms:

| Form | Presses |
|---|---|
| `choose "Red" on OBJ` | The button whose label equals it, ignoring case and surrounding space. Two buttons that fold to the same label fail the step and are listed, so the runner never presses the first. |
| `choose matching "RE" on OBJ` | The one button whose label, trimmed, matches the pattern (RE2, unanchored). Zero matches, or two or more, fail the step and list the buttons. |
| `choose button N on OBJ` | The Nth button in the dialog's list order, counting from 1. A number past the last button fails the step. |
| `choose $x on OBJ` | The button whose label is the text `$x` holds, compared as the literal form is. |

Every refusal is made before anything is sent: so are no held dialog and a held text box, which wants `answer`. Every form sends the position of the pressed button, so the reply names that button even when two share a label. The list order is the order the script gave `llDialog`, not the order on screen; see [Dialog](#expectations).

**Answer.** `answer "text" on OBJ` types into the held text box; if the held dialog is not a text box the step fails and lists its buttons.

**Send.** `send on OBJ from link A to link T num N text "..." key K` makes the probe in link A call a link message to T with number N, text and key (default `null`). `T` is a number or `all` (−1, every prim), `others` (−2, all but the sender), `children` (−3, all but the root), `this` (−4, the sender only) or `root` (link 1). The message is delivered only to scripts in the prims it addresses. A link absent from the probe's report fails before the command is spoken. The step passes when the command is sent.

**Wear.** `wear ITEM on "POINT" as NAME` puts an inventory item on the tester. `ITEM` is an `item` header ([Objects](#objects-names-and-link-numbers)), and `POINT` is an attachment point by name, such as `"HUD Top Left"` or `"Chest"`. The item is added to the point and does not replace what is there. It is a blocking stimulus under the stimulus budget.

- An item that is already worn fails the step before anything is sent: `slate: step N: "Example Hat" is already worn on HUD top; take it off first`. A run that was killed can leave the product worn, so the next run's `wear` fails with this sentence.
- `NAME` is bound to the worn root when the stimulus returns, with its children as its linkset. It is usable by the same step's expectations. That is the one exception to the rule that a name bound in a step is usable only from the next step.
- The step arms before the request. `Wear` returns after about 0.2 s, and the product's own reaction to being worn, its `on_rez` and `attach()`, arrives about 2 s after `Wear` returns (measured, [Eighth round](slate-runner.md#eighth-round)). Give the step a `within` that covers it: the default 10 s does, and `within 1s` does not.
- The runner does not take off what a test wore. Put the `take off` in `after each`, since the test may have failed before its own.

**Take off.** `take off NAME` takes off the item `NAME` is worn from, which is a name `wear` bound or an object the tester is wearing. A binding that is not worn fails the step with `NAME is not worn`. It is a blocking stimulus, and it completes when the store no longer lists the worn root, about 0.1 s after the request (measured, [Eighth round](slate-runner.md#eighth-round)), or fails with `"<item>" is still in the store <budget> after the take off`. `NAME` is not usable after this step, and this step's own expectations, such as `expect attached NAME off`, are what it is for.

## Expectations

**Text and patterns.** Wherever an expectation compares product text, the author writes either a string, which is exact and case-sensitive, or `matching "RE"`, which is a pattern.

| Where | Exact | Pattern |
|---|---|---|
| say text | `expect say "red" on public ...` | `expect say matching "^Thanks, .+!$" on public ...` |
| dialog message | `expect dialog from v text "Choose"` | `expect dialog from v text matching "^Choose"` |
| dialog button | `expect dialog from v button "Red"` | `expect dialog from v button matching "^Re"` |
| text box message | `expect textbox from b text "Name?"` | `expect textbox from b text matching "name"` |
| give item name | `expect give "Example Red Swatch" from v` | `expect give matching "Swatch$" from v` |
| rez name, description | `rez name "B" description "left" ...` | `rez name matching "^B" description matching "left" ...` |
| link text | `... text "go"` | `... text matching "^go"` |

A pattern is Go RE2 syntax and is unanchored, as a button `pattern` is; write `^` and `$` to anchor it, and `(?i)` to ignore case. It is compiled at static check, and one that does not compile is exit 2. In a Slate string a backslash is doubled, so the pattern `L\$[0-9]+` is written `"L\\$[0-9]+"`. Wherever the Exact column takes a string, a capture that holds text takes its place and is compared as that string would be ([Captures](#expectations)); a capture is never a pattern. The `choose` forms are under [Stimuli](#stimuli). `answer` and the text of the stimulus `say` take strings only, and the text of `send` takes a string or a capture. With `give matching`, two new items whose names match are ambiguous, as two new items of one name are.

An expectation passes when its match has been seen after the arm point. When the deadline passes it fails with the line that was written, the duration and a transcript of what was heard instead ([Reading the result](#reading-the-result)).

**Say.** A chat line, or a forwarded listen, whose text and speaker match, on the channel written. `from object OBJ` accepts any prim of `OBJ`'s linkset; `from object OBJ link N` accepts only that prim.

**Dialog.** A dialog offered to the tester, not already showing at the arm point, from the bound prim or any prim of its linkset (the dialog's `Object` is that prim, or its `ObjectName` equals the name of any prim of the linkset). `dialog from OBJ link N` requires that exact prim, which is found when the dialog arrives ([Objects](#objects-names-and-link-numbers)). After that the expectation is any of these, in this order:

- **`text`**, optional. With it, the message equals the string, matches the pattern, or equals the capture. Without it any message matches; `text matching ""` stays legal and means the same.
- **`button` clauses**, each `button TEXT` or `button N TEXT`. Each clause needs a button of its own. A literal and a capture are compared ignoring case and surrounding space; `matching "RE"` is an unanchored RE2 pattern against the trimmed label, case-sensitive unless it says `(?i)`. `button N` pins the clause to the Nth button.
- **`only`**: every button of the dialog belongs to some clause. Without it, extra buttons are allowed.
- **`ordered`**: the buttons given to the clauses stand in the dialog in the same relative order as the clauses are written. It is judged over the assignment the matcher found, and when several assignments give every clause a button of its own, the runner looks for one that is in order. It needs at least two clauses.
- **`count N`**: the dialog has exactly N buttons.
- **`sorted`**: the labels, trimmed, are in non-decreasing order, compared ignoring case. With `matching "RE"`, only the labels the pattern matches are compared, so `<<` and `>>` can be left out. If the pattern has one capturing group, the group's text is what is compared, and not the whole label. The compared texts are numbers when every one of them is a decimal number, `^-?[0-9]+(\.[0-9]+)?$`, and case-folded text otherwise, so `n9` sorts before `n10` under `"^n([0-9]+)$"`, and does not under plain `sorted`.

Clauses are given buttons by matching, not in the order written, so one that could take several buttons does not starve a later one: `button matching "a|b" button "a"` meets a dialog of `a` and `b`, the first clause taking `b`. Two identical literal clauses need two buttons that match, and one button never answers both. The assignment fails when some clause cannot have a button of its own. `button N` counts in the order the script gave its list to `llDialog`, which is the order the simulator sent the buttons in (measured, [Fifth round](slate-runner.md#fifth-round)), and is not the order a person sees on screen.

```slate
expect dialog from hud text matching "^Pick" button matching "^[0-9]+$" button 4 "Cancel" count 5
```

`ordered` and `sorted` judge the order the script gave `llDialog`, which is `Dialog.Buttons`, and not the order a person sees on screen. The details of a failure are [under Reading the result](#reading-the-result).

A text box does not match `expect dialog`. The matching dialog is held for the binding ([Dialog hold](#steps-and-timing)), so `choose` and `answer` on `OBJ` use the hold of any dialog matched for `OBJ`. The author never writes the dialog's channel, and does not expect the reply `choose` sends; the product's next chat line, give or texture change is the expectation. A dialog offered to another avatar is invisible. There is no decline, so an unanswered dialog is left to expire. When no dialog meets the clauses, the `unmatched` line says why the last dialog from that object with that message did not ([Reading the result](#reading-the-result)).

**Text box.** The same as a dialog, for a text box, with a message that is a string or a pattern, no button list, and the same linkset and `link N` rules. Answer it with `answer`, not `choose`.

**State expectations.** Texture, offset, repeats, rotation, position, size, click, text, fullbright, glow, colour and alpha are readings of state. Each takes one of three forms, and a value may be written `original`, as a capture (`$x`), or, after `is` only and with `as $x`, as `any`:

| Form | Passes when |
|---|---|
| `is X` | A reading equals X, including a reading that already did at the arm point. It is a plain reading and cannot by itself prove that the stimulus changed anything. `is any as $x` passes on the first reading, whatever it is, and binds it. |
| `becomes X` | A reading equals X and an earlier reading in the same step's window, the baseline counted, did not. If the baseline already equals X, the value must leave X and come back. |
| `changes` | A reading differs from the baseline. It takes no value. |

`expect texture sign face 0 becomes 47227e57-7e57-c0de-1611-3fe69d620af1` is the way to show that a stimulus changed a texture. `link N` after the binding (`expect texture sign link 3 face 0 ...`, `expect click chair link 2 is touch`) selects that prim of the binding's linkset, found at each poll ([Objects](#objects-names-and-link-numbers)). Without `link`, the expectation reads the bound prim itself.

**Baseline.** A step's baseline for an expectation is the latest reading of that face (or click byte) observed at or before the step's arm point, taken from the event log. The runner reads every prim and face that any state expectation in the test names, from the start of the test. If no reading exists at the arm point, the first reading after it is the baseline, and the transcript says so with a line `baseline for <object> face <n> taken after the arm point`.

**Tolerances.** `changes` and `becomes` compare as `is` does: offset within 2/32767, repeats within 1e-4, rotation within 2/32768 of a turn, position and size within 0.001 m on each axis, glow, colour and alpha within 1/255, texture, click and fullbright exactly. A reading that differs from the baseline by less than the tolerance is not a change.

**Original.** `original` means the reading of that face or click byte when the test's own steps begin, after `before each` has run, so a reset in `before each` is what `original` refers to. It is meant for toggles: touch once and the value `changes`; touch again and it `becomes original`. If no reading exists at that point, `original` is the first reading after it. `original` is legal wherever a value is: a texture, the two numbers of offset or repeats, the one number of rotation, glow or alpha, the three of a colour, a position or a size, `on` or `off`, a click name. With `face all` it is the tuple the test began with.

**Texture.** Face N of the bound prim shows the texture UUID. A reading can lag a script's change by several seconds, so the runner asks the region again about once a second. A reading that still shows the old texture does not fail the step early; only the deadline does. A planar face or a face with a running texture animation does not stop a texture comparison.

**Offset, repeats, rotation.** Offset matches within 2/32767 of each literal, since the stored value is quantised to about one part in 32767. Repeats match within 1e-4 each; negative repeats are a flip and are legal. Rotation is a fraction of a turn, not degrees or radians: `0.25` is a quarter turn and `0.5` a half turn, matched within 2/32768 of a turn.

**Position and size.** `expect position OBJ [link N] (is X Y Z | becomes X Y Z | changes)` and `expect size OBJ [link N] (is X Y Z | becomes X Y Z | changes)` read the prim's position and its scale, in metres, as the object store holds them. They take no `face`, a value is three numbers, and a value may be `original`, a capture or, after `is` with `as`, `any`, as every state expectation does. What a position is depends on the prim, because the store keeps the position of the last update as the region sent it and does not compose it:

| The prim | `position` is |
|---|---|
| A worn root | Its offset from the attach point, so a HUD that has not been moved reads `0 0 0` |
| A child (`link N`, N of 2 or more) | Its position relative to its root, in the root's frame |
| A rezzed root | Its position in the region |

This is the `Position` of the prim's `sl.Seen`, and `Seen.Scale` for `size`; a poll reads both with no request of its own. Both compare within 0.001 m on each axis. The region reports what a script set exactly, so the tolerance is for the author, who writes rounded numbers. A millimetre is about one pixel of the default 1025-pixel world view at HUD zoom 1, since the view is one metre tall ([Where a worn HUD is on the screen](hud-screen.md#the-world-view)). Measurements are under [Position and size](slate-runner.md#position-and-size) in the runner document. The reading is a position in the HUD's own frame, not a place on the screen: `at X Y` on the screen is not built ([Known limitations](#known-limitations)).

**Floating text.** `expect text OBJ [link N] (is TEXT | becomes TEXT | changes)` reads the text a script puts above a prim with `llSetText`, as the object store holds it from the prim's last update. It takes no `face`. `TEXT` is what the other text clauses take: a string, `matching "RE"` (a Go RE2 expression, matched anywhere in the text unless anchored, whose named groups bind text), a text capture, `original`, or after `is` with `as $x`, `any`. A string is compared exactly, byte for byte, newlines and trailing spaces included; a prim with no text reads as the empty string, so `expect text sign is ""` says there is none, and `becomes "x"` after `is ""` is how a script's first `llSetText` is shown. `as $x` binds the whole text, and `link N`, the baseline, `original`, `becomes` meaning a change to a text and the negative forms are those of every state expectation. With a pattern, `becomes` needs a text that does not match before one that does, as it needs a reading that is not X before X: a sign that goes from "Example text 1" to "Example text 2" never `becomes matching "^Example text"`, since it matched all along. To see one matching text replaced by another, expect `changes` and then, in the next step, `is matching`. A floating text is at most 254 bytes. The reading is `Seen.Text` of the prim ([`sl/query.go`](../sl/query.go)); a poll reads it with no request of its own. Measured on the grid on 2026-10-03 with the test avatar and an invented box whose script called `llSetText` when touched: the text was in the store 96 to 171 ms after the touch, median 135 ms, in 20 of 20 touches, so the default window of 10 s has a wide margin and no `within` is needed ([Floating text](slate-runner.md#floating-text)).

**Click.** The prim's click action equals the named value:

| Name | Value | Name | Value |
|---|---|---|---|
| `touch`, `none` | 0 | `play` | 5 |
| `sit` | 1 | `media` | 6 |
| `buy` | 2 | `zoom` | 7 |
| `pay` | 3 | `disabled` | 8 |
| `open` | 4 | | |

These are Linden's published constants, not measured here. Any other value fails the comparison and is printed as a number. The click action needs the click byte kept by slgo ([the click byte](slate-sl-changes.md#click-action)). A click expectation on a header binding is read during setup for up to 30 s; if the value is still unknown, setup fails (exit 3) with the click sentence under [Setup failures](#reading-the-result).

**Face properties.** Four more readings of a face, from the same texture entry as a texture and on the same cadence, so a change is seen as soon as a texture change would be:

| Expectation | Value | The reading | Tolerance |
|---|---|---|---|
| `fullbright OBJ face N is on` | `on` or `off` | The face's full-bright flag, bit 0x20 of its bump byte | Exact |
| `glow OBJ face N is 0.5` | A number in [0, 1] | The glow byte over 255 | 1/255 |
| `colour OBJ face N is 0.25 0.5 0.75` | Three numbers in [0, 1]: red, green, blue | Each of the three colour bytes over 255 | 1/255 each |
| `alpha OBJ face N is 0.4` | A number in [0, 1] | The fourth colour byte over 255 | 1/255 |

Glow, each colour channel and alpha travel as one byte, `round(value × 255)`. That was measured on 2026-10-01 on the grid, with a script setting face 0 and the session reading the store: glow 0.5, 1.0, 0.25 and 0.01 read as the bytes 128, 255, 64 and 3, and the colour `<0.25, 0.5, 0.75>` with alpha 0.4 read as 64, 128, 191 and 102 ([Fifth round](slate-runner.md#fifth-round)). A literal is therefore matched within one step of the byte, so `glow 0.5` accepts the byte 128. The state words, `link N`, `original`, the negative forms and `as $x` are those of every state expectation. The values print as the transcript lines of [Reading the result](#reading-the-result), rounded to four places.

**Face all.** `face all` in place of a face number makes the reading the tuple of every face's value:

- `is X` holds when every face equals X.
- `becomes X` holds when every face equals X and an earlier reading in the window did not.
- `changes` holds when the tuple differs from the baseline tuple.

That is the one rule, and it applies to every expectation that takes a face: texture, offset, repeats, rotation, fullbright, glow, colour and alpha. A value is compared face by face with that expectation's own tolerance. `original` is the tuple the test began with, and `is any as $x` binds the tuple, which only a `face all` of the same kind can use. The tuple has exactly as many elements as the prim has faces. The count comes from the prim's shape, as the viewer computes it and as measured on 340 shapes ([How many faces a prim has](objects.md#how-many-faces-a-prim-has)). For a sculpt or a mesh the count is not known and the tuple is cut where the entry stops differing from its default; see [Known limitations](#known-limitations).

**Button readings.** `expect button OBJ PARTS is shown` reads a button off a prim's pictures instead of touching it: it counts the tuples a button search finds on the prim. The search is that of [Buttons](#buttons), with the same parts, combination and `face` rule, and without `nth`. `link N` after the binding selects a prim of its linkset, as it does on every expectation. The forms are those of the state expectations:

| Form | Holds when |
|---|---|
| `is shown`, `becomes shown` | the count is 1 or more |
| `is gone`, `becomes gone` | the count is 0 |
| `is count N`, `becomes count N` | the count is N |
| `is original`, `becomes original` | the count is what it was when the test's own steps began |
| `changes` | the count differs from the baseline |

The baseline, the negative forms and the rule that a negative needs a real reading are those of every state expectation. `as $x` binds the count, a number, which a `glow`, `alpha` or `rotation` value can use. Every face of the prim is read, or the one `face N`. The count is stamped with the time of the texture reading it was made from, not the time the finder finished.

*A reading may be missing.* A planar or animated face, a texture that cannot be fetched, a finder error, and an `image` or `oval` part are no reading, and a missing reading is never `gone`. The step then fails at its deadline, and the `unmatched` line ends with the sentence a touch would have printed ([Reading the result](#reading-the-result)). A face with no texture contributes no tuples and is not a missing reading.

*Write `becomes gone`, not `is gone`.* `is gone` holds for a reading of zero, and a reading of zero is also what the finder returns for a label it cannot read. `Close` that the finder never reads is "gone" before the stimulus and after it, so `is gone` can pass without anything having changed. `becomes gone` needs a baseline that showed the button, so a label the finder cannot read fails the step instead of passing it.

*Both expectations of a swap go in one step.* When one texture swap makes `Close` go and `Open` appear, write both expectations in the step that has the stimulus. In a later `then` step the baseline is the reading at that step's arm point, and by then it already shows the new state, so `becomes shown` would need the button to leave and come back.

```slate
touch hud button text "Close" box
expect button hud text "Close" box becomes gone within 8s
expect button hud text "Open" box becomes shown within 8s
```

**Attached.** `expect attached OBJ on "POINT"` holds when the object is worn on that point, and `expect attached OBJ off` when the store no longer lists it. They are readings of state with the arm point of the other state expectations: a reading at the arm point that holds passes, and so does any reading after it up to the deadline. The negative forms apply and need a reading, and a name that is not bound yet or is still being worn has none. `POINT` is a name as `wear` takes it.

**Captures.** A capture is a value that an expectation binds and a later step uses as a literal. It is written `$name`. Each of these binds, when the expectation matches:

| What | Binds | Type |
|---|---|---|
| A named group `(?P<name>...)` in any `matching` pattern of a positive expectation, including a button clause | `$name`, the group's text in the event that matched | text |
| `as $x` after `say` | The line's text (the raw tail, for a channel the bridge forwards) | text |
| `as $x` after `dialog` or `textbox` | The message | text |
| `as $x` after `give` | The item's name | text |
| `as $x` after a state expectation | The reading that matched; with `face all`, the tuple | its own type |
| `as $x` after a button reading | The count | number |

The types, the scope and the rule that a capture is bound once per test are in [Static checks](#static-checks). In short, a capture is usable from the next step on, never in the step that binds it, and only a positive expectation binds one. A group that took part in the match binds its text, which may be empty. A group that did not take part (inside an alternative that was not taken, or a `?`) fails the step with `$name did not take part in the match`, and nothing from that match is bound.

```slate
expect dialog from hud button 1 matching "^(?P<first>.+)$"
choose $first on hud
expect say $first on public from object sign
```

A capture is data. It is never parsed as Slate, never compiled as a pattern and never spliced into a string, and it is not usable inside a `matching` pattern. Each use is the value whole, as a literal in that position: exact for a `say` and a `give`, folded for `choose` and a dialog button, as the text of a button part for a `text` part. The checks that are static for a literal are made at the step for a capture, before the stimulus is sent: non-empty after trimming for a button part, and the link-text character rule and the length of the control line for a `send` text. A capture that breaks one fails the step with a sentence that names it and quotes its value, as under [Reading the result](#reading-the-result).

**Give.** Passes when the tester's inventory holds a non-folder item of that name whose id was not there at the arm point, and the offer's `FromName` equals the name of any prim of the binding's linkset. A give stays on the linkset: it takes no `link N`, because the give offer does not say which prim sent it. The name is a string (exact and case-sensitive) or a `matching` pattern. An older item of the same name does not count and does not block the pass. Seeing the offer alone is not enough. The runner accepts the offer, and says so in the transcript: `give accept sent to <uuid> transaction <uuid> into <folder>`. The item's name is read out of the offer text; [Give](slate-runner.md#give) says how. Two offers that match, or two new items of that name, fail the step as ambiguous and name both. If an accept was sent and the count of items of that name has not grown, the unmatched line says so:

```text
unmatched give "Example Thank You" from vendor within 10s; accept was sent and inventory still has 1 item of that name
```

The runner does not decline an offer, and does not delete the item afterwards. A later run passes on a newer id.

**Rez.** A new root prim: its id was not in the region at the arm point, it has no parent and it is a prim. Child prims of a new linkset and avatars are not rezzes. The region does not say which object rezzed a prim, so `from OBJ` is the substitute and is required. A prim is claimed when:

- its position is within 10 metres of some prim of `OBJ`'s linkset, measured from the root of each prim's linkset (child offsets ignored; a worn HUD is measured at the avatar, a seated wearer at the seat). The 10 m is the historical `llRezObject` limit used as a heuristic. It is not a claim that the region enforces it, and it is not measured here; and
- if both owners are known and non-zero, they are equal.

`name` is exact and case-sensitive, or a `matching` pattern; `description`, when written, is the same. If the linkset's position is unknown the claim does not match and the rejection reason is `position unknown`. With zero matches at the deadline, the report lists every new root seen and the reason each was rejected. A second root that matches the same claim fails the step at once and binds neither; the report lists both. With several claims, each matching root goes to the earliest unmatched claim, in source order, that it satisfies. Roots seen in the same poll are ordered by local id, which is not the order of the product's rez calls, so write `description` or a distinct `name` when it matters which is which. A new root that matches `from` and no claim fails the step. A root outside the radius, or with another owner, does not.

The `as` name is bound when the claim is assigned and refers to that root prim in every later step. If the step fails, the name is not usable. Bindings made with `as` are not renames; the prim keeps whatever name the product gave it.

**Link.** A probe report for this object's linkset with the sender, number, text (a string, or a `matching` pattern) and key given (`key` defaults to `null`). `heard by N` also requires that the prim that received it reported link N. Without `heard by`, the first report from any prim in the linkset passes, and further reports of an `all` delivery are not failures.

**Negative expectations.** `expect no BODY within D` passes when BODY does not match at any time from the arm point through `D`, and fails at once, quoting the event, if it does. The forms are the positive ones, except that a negative rez has no `as`, and a negative state expectation still needs a real reading: no reading is a failure at the deadline, not a pass. For state:

- `expect no texture sign face 0 changes within 2s` passes when no reading differs from the baseline during the window.
- `expect no ... becomes X` passes when there is no transition to X during the window.
- `expect no ... is X` passes when no reading equals X, and a reading must exist.

These are the rules of the button reading and of `attached` too.

A negative `say matching` is the way to rule out a family of lines, as in `expect no say matching "(?i)error" ...`. Silence is the pass for say, dialog, give, rez and link. `expect no` is the only negative form. There is no negative that spans the rest of the file. To require a quiet second after a success, write a later step whose only expectation is the `expect no`, with its own `within`.

## Buttons

A button touch is a picture match, then a click at the match's centre. The author does not write pixels. The runner reads the picture on every face of the prim, searches for the parts, and clicks once. Parts are written in the order they appear and each is one finder request:

| Part | Matches |
|---|---|
| `text "Menu"` | Exact, case-sensitive text, after trim. Words on one line are joined by one space. |
| `pattern "^Menu$"` | A regular expression over the same words and lines, unanchored. |
| `symbol "left arrow"` | A drawing: `circle`, `arrow`, `left arrow`, `right arrow`, `up arrow`, `down arrow`. Case, hyphens and extra spaces are ignored. Any other name fails before a touch. |
| `circle` | A ring or a disk, roughly square (aspect 0.85 to 1.18, each side at least 48 pixels). |
| `box` | An outlined box: a wide button or a rectangular frame. |
| `image "..."` | Never matches. |
| `oval` | Never matches. |

**Combination.** A single part matches its own items. Several parts match a tuple with one item from each part whose rectangles have a non-empty intersection (width and height both above zero). An arrow inside a circle intersects it, and text inside a box intersects it. Parts that merely sit near each other do not combine, which keeps a label on one button from pairing with the outline of the next. The click point of a tuple is the centre of the intersection; of a single part, the item's centre. The runner does not prefer the text over the box.

**`nth` and `face`.** `nth` selects the Nth tuple, counted from 1, across faces in face order when no `face` is given, and in top-to-bottom then left-to-right order within a face. Without `nth`, exactly one tuple must exist. Zero tuples fails the step and says which part was missing; two or more fail the step and list every centre, and the runner never clicks the first. A `face N` or `nth` the prim cannot satisfy fails it too. Each sentence is under the button sentences of [Reading the result](#reading-the-result). `face N` restricts the search to that face. Without it, every face of the prim is searched (the link's prim when `link` was written).

**Faces the search refuses.** One planar face, or one face with a running texture animation, fails every button search on that prim, including a search restricted to another face. The step fails before the touch:

```text
slate: step N: face 2 of "Test HUD" is planar, so its offset, repeats and rotation are not the picture on it
slate: step N: face 2 of "Test HUD" has a texture animation, so one still picture is not what it shows
```

**`image` and `oval`.** They are accepted and always fail. The current finder has no image template match and no oval. When any part is one of them the step fails before the touch, even if the other parts would have matched, with one of these two sentences and no others:

```text
slate: step N: button part image is allowed, and the finder does not match one; it matches text, a pattern, a drawing (circle, arrow, and the four directions), and an outlined box
slate: step N: button part oval is allowed, and the finder does not match one; a round ring is circle, and a wide outlined control is box
```

A button step that fails after a search leaves the face pictures in the system temp directory and prints their paths ([Reading the result](#reading-the-result), the button sentences). How the click point is computed is in [Buttons](slate-runner.md#buttons).

## Objects, names and link numbers

One binding is one prim, found by exact, case-sensitive, unique name within draw distance. The name must be unique among everything the runner can see, unless a description tells objects of one name apart. An `as` name binds the root prim of a new rez. The binding is still one prim, but expectations may speak of its whole linkset: `from object OBJ`, `dialog from OBJ` and `give ... from OBJ` match any prim of it, and `link N` picks one.

**Twins and the description.** `object north is "Example Sign" description "north"` finds the objects of that name, asks each one for its properties, and keeps those whose description equals the string, or matches the pattern if it is written `description matching "RE"`. Exactly one must remain; none and several are setup failures ([Reading the result](#reading-the-result)). The description is read once, at setup, so a product that changes its description afterwards keeps its binding. Two objects of one name need a description on each header, and no two descriptions may be written the same ([Static checks](#static-checks)). One prim that matches two headers is a setup failure. A header without a description is as it was: the name must find one prim.

```slate
object north is "Example Sign" description "north"
object south is "Example Sign" description matching "^south"
```

**Items.** `item hat is "Example Hat" in "Objects"` names an inventory item, to be worn. The folder is a top-level folder of the tester's inventory, and the item is one in it. Both names are matched exactly and case-sensitively, and each must pick out one thing. Items are found at setup, so a missing one is exit 3 before any step. An item is not a prim in the region: it is used only by `wear`, and a `wear` makes an object binding of the worn root. The item may be a scripted attachment, and whether it is a HUD or worn on the body is the point `wear` names.

**The linkset.** For probes and link numbers, the linkset starts from the bound prim. If it has a parent that is a prim, the runner climbs to it and repeats. If the parent is an avatar, the bound prim is an attachment root and the avatar is not a member. The prim the climb stops on is the root, and the members are the root and its child prims. A sibling attachment is never a member, and a worn object's children are. A parent missing from view is a setup failure. The runner finds the linkset of every header binding at setup, whether or not it has a probe; for a name bound with `as`, the linkset is the new root and its children. Link numbers for `link N` come from the probe when the binding has one, and from the object store when it has not ([Link numbers](#objects-names-and-link-numbers), below). Two header bindings that resolve to the same linkset and both have a `probe` header are a setup error.

**Link numbers** are what `llGetLinkNumber` reports: 0 for an unlinked prim, which still receives messages addressed to link 1, 1 for a linked root, and 2 and up for children. They come from the store, or from a probe when there is one, which wins. With no probe on the binding the runner asks the session for the linkset of the binding's root ([Link numbers](objects.md#link-numbers)), so a child prim of a product the tester does not own can be named: `touch hud link 3`, `expect texture hud link 2 face 0 ...`, `from object hud link 2`. With a probe, the hello map is the answer exactly as before, because it is the script's own truth; the store is not asked. Nothing is fixed at setup, since a product can relink itself: a touch or drag finds its prim when its step is prepared, a reading on each poll that reads that prim, and a speaker or dialog match when the line or dialog arrives. A reading therefore follows the number to the new prim after a relink. A set whose order the store cannot vouch for, or a link the set does not have, fails with a sentence ([Reading the result](#reading-the-result)). A worn linkset is numbered under its root in the same way.

The probe is still needed for the link messages themselves, `send` and `expect link`, because there the probe is the mechanism and not the map. While the tester sits on the linkset, the avatar occupies a link number and no probe is installed there; a message addressed only to that number is not observed, and the transcript says so on a sit step.

**Probes.** `probe vendor` installs a copy of the runner's probe script, named `slate probe`, in every prim of the linkset. The prim must be modifiable by the tester, and the parcel must run scripts ([Probe and bridge](slate-runner.md#probe-and-bridge)). Each probe reports what its prim received, and the runner picks the channels it uses; the file never names one.

The bridge is a small object the tester wears on a HUD point during the run: `slate bridge` in the tester's inventory, made once with `slate -make-bridge`, which needs build rights once. It is worn only if the file has a `probe` or a `listen`, taken off at the end and kept for the next run. Nothing is rezzed in the region for it, so the bridge needs no build rights during a run. A file that only touches, sits and reads public chat does not wear it. See [Open questions](slate-runner.md#open-questions).

**`listen N`.** Adds a channel the bridge listens on, so `expect say ... on N` can match a product line the viewer is never sent.

**`heard by`.** A report names the link number of the prim that received the message. A `children` message is reported by each child and not by the root, and a message sent to link 3 is not `heard by 1`. Expecting `heard by 1` for a message only sent to a child fails; that is the LSL delivery rule, not a lost packet.

## Reading the result

The runner writes the run to standard output as it happens, so one capture is the whole run; only a usage error and a failed dial go to standard error ([Package and command](slate-runner.md#package-and-command)). `-run REGEX` on the command line limits a suite to the tests whose names match; the tests that do not match are not printed. Timestamps are UTC, `15:04:05.000`.

| Code | Meaning |
|---|---|
| 0 | Every test passed. Cleanup warnings may still be present. |
| 1 | Some test failed (and setup did not): a step failed an expectation, a stimulus or a runtime rule (not the owner, a button part the finder cannot match, paying is off, an ambiguous match). |
| 2 | The file did not parse or failed a static check. The region was not dialled. |
| 3 | Setup failed: the dial (`slate: dial: <error>`, on standard error), object lookup, item lookup, probe install, hello, the bridge. Also a click action still unknown when its 30 s wait ends. Exit 3 outranks 1. For a name bound with `as`, that exit is printed while the rez step is still open, before any later step. |
| 4 | The runner was used wrongly: a missing file, an unknown flag, or a `-run` pattern that does not compile or matches no test. Nothing is dialled. |

The code is 3 if setup failed or a click byte stayed unknown, else 1 if any test failed, else 0. A run with several tests continues after a failed test, so one run can report several failures; see [Tests, sequences, before and after](#tests-sequences-before-and-after).

Each test prints `slate: test "<name>"` when it starts and `slate: pass step N` for each step that passes. A test that passes ends with `slate: pass test "<name>"`; one that fails ends with the failure block. The last line of a run that finished is its verdict, `slate: passed <n> tests` or `slate: failed <m> of <n> tests`; the cleanup lines ([What a run leaves behind](#reading-the-result)) come before it. A run that stopped, on a setup failure or a dropped subscription (`slate: run stopped: <error>`), prints no verdict, and its cleanup lines follow its last line. Probe hellos during setup print `slate: probe <object> link <n> <uuid>` before the first test; they are not steps. A failing step prints this block. The words shown literally are fixed:

```text
slate: fail <path> test "<name>" step <N> lines <L1>-<L2>
  stimulus: <source line, or "(none)">
    <one line: what was sent, or the stimulus error>
  expectations:
    <for each: "matched" or "unmatched" or "forbidden", the source line,
     and for a match the time and a one-line observation>
  failed: <why>
  waited: <duration>
  heard during the step:
    <zero or more transcript lines>
    <or "(none)">
  seated: <no, or the seat object's script name and in-world name>
  dialog left unanswered: <no, or [Object] and the object's name, the message, and the buttons>
  payment: <none, or the amount, the object, and the transaction id or the unconfirmed-payment text>
  captured: <none, or each capture bound so far, as $name and its value, in the order bound>
```

The `captured:` line is always printed, after `payment:`. Each entry is `$name` and the value as the capture line prints it, separated by commas: `captured: $first "Example Sign", $tile 47227e57-7e57-c0de-1611-3fe69d620af1`. The `  failed: <why>` line is optional. It is printed, between the expectations and `waited`, when the step failed for a reason that no one expectation says; the sentences it carries are under the give and rez sentences below. `<N>` counts steps through `before each`, the test and `after each` together. For a step that came from a sequence, or from a `before each` or `after each` block, `lines <L1>-<L2>` are the lines of the step inside that block, and `via do NAME at line L` follows them on the first line. When sequences call sequences, there is one `via` for each `do`, innermost first: `via do inner at line 12 via do outer at line 30`. A failure inside `after each` prints the same block with `after each` after the test name: `slate: fail <path> test "<name>" after each step <N> lines <L1>-<L2>`. If the test itself had failed, its own block is printed first.

**Transcript lines**, one event each:

```text
HH:MM:SS.mmm chat <how> from <who>: "<text>"
HH:MM:SS.mmm dialog from <who>: "<message>" buttons <labels>
HH:MM:SS.mmm textbox from <who>: "<message>"
HH:MM:SS.mmm give from <who>: "<item>"
HH:MM:SS.mmm give accept sent to <uuid> transaction <uuid> into <folder>
HH:MM:SS.mmm rez <uuid> name "<name>" at <x> <y> <z>
HH:MM:SS.mmm texture <object> face <n> <uuid>
HH:MM:SS.mmm fullbright <object> face <n> <on or off>
HH:MM:SS.mmm glow <object> face <n> <level>
HH:MM:SS.mmm colour <object> face <n> <red> <green> <blue>
HH:MM:SS.mmm alpha <object> face <n> <level>
HH:MM:SS.mmm button <object> <parts as written> <n> (faces <list>)
HH:MM:SS.mmm attached <object> <point or off>
HH:MM:SS.mmm capture $<name> = <value> (step <N>)
HH:MM:SS.mmm click <object> <byte>
HH:MM:SS.mmm pay L$<amount> to "<name>" <uuid> reason "<reason>"
HH:MM:SS.mmm probe link <object> heard-by <n> from <sender> num <num> [key <uuid>] "<text>"
HH:MM:SS.mmm probe overflow <object> heard-by <n> bytes <n>
HH:MM:SS.mmm probe bad <object> link <n>
HH:MM:SS.mmm probe fwd-overflow channel <n> bytes <n>
HH:MM:SS.mmm chat channel <n> from <who>: "<tail>"
HH:MM:SS.mmm permission denied from <who>: <mask>
```

`<how>` is `public`, `owner`, `debug`, `direct`, `region`, or `channel <n>`, which is a line the worn bridge forwarded from a `listen` channel; it is product chat, printed as `chat channel <n> from <who>: "<tail>"` with the raw tail. `<who>` is the script's name for a bound prim, otherwise the displayed name. The lines that begin `probe` are protocol, never `chat`. `probe link` carries ` key <uuid>` after the num only when the key the message carried is not the null key. `probe overflow` is a report the probe could not send whole; `probe bad` is a command the probe could not parse, on the link the probe is in; `probe fwd-overflow` is a forward the bridge could not send whole, on a `listen` channel. The pay line prints before the payment, including when it then fails. A texture line prints whenever a reading differs from the last one printed, stale readings included; the offset, repeats and rotation expectations print it too. A fullbright, glow, colour or alpha line prints the same way, and each is compared only with the last line of its own kind for that object and face. A level is rounded to four places. With `face all` the face is written `all` and the line lists the value of each face from face 0, separated by commas: `fullbright sign face all on, off`. A position or size line prints the same way, `position hud 0 0 0` or `size hud 0.5 0.25 0.1`, and is compared with the last line of its own kind for that object. A floating text line prints whenever the text differs from the last one printed for that object, `text sign "Controlling Example Chair"`, with the text quoted as Go quotes a string, as a chat line is, so a newline in it shows as `\n` and a quote as `\"` and neither can start a line of its own. A button line prints whenever the count differs from the last one printed for that prim and those parts, and ends with the faces the buttons were found on, `faces 0, 2`, or `faces none` when the count is 0. With a `link` the object is written `<object> link <n>`. An attached line prints when the reading differs from the last one printed for that name, `attached hud HUD centre 2` or `attached hud off`, and the point is written as `sl` names it. A capture prints when its expectation matches, once, with the step that bound it. Its value is a text quoted as Go quotes a string (a floating text included), a UUID, the two numbers of a pair, one number, a click byte as a number, the three of a colour, the three of a position or size, `on` or `off`, or, for a `face all`, `face all` and the comma-separated values: `capture $first = "Example Sign" (step 3)`, `capture $all = face all on, off (step 1)`. When a step's baseline is the first reading after the arm point, the runner also prints `baseline for <object> face <n> taken after the arm point` (`face all` stands for the face of a `face all`; for a click byte it is `baseline for <object> click taken after the arm point`, and `position` or `size` in the place of `click` for those readings, `text` for a floating text, and `baseline for <object> button taken after the arm point` for a button reading). If chat or instant messages arrive faster than the runner reads them, the run fails with exit 1, `the chat subscription dropped <n> lines`, rather than looking like a timeout.

**Warnings.** Three kinds of line are printed on their own and change no exit code. Two are about a line that looked like the runner's protocol and was not accepted:

```text
slate: warning: ignored a hello from <src> that names <key>
slate: warning: ignored a protocol line from <src>: <err>
```

The first is a hello whose source is not the prim it names (`<src>` is the chat source, `<key>` the one it claimed); the second is a protocol line that did not parse, with the parse error. The third is a cleanup error, `slate: cleanup: warning: <what>`, such as `the probe in "<name>" was not removed: <error>`, `the bridge script was not removed: <error>` or `the bridge was not taken off: <error>`. A setup warning from an install is `slate: setup: warning: <text>`.

**Runtime sentences that name a step.** These three fail the step before a say is sent, exit 1: a binding beyond 20 m, the tester's position not exact (it is read when needed), and a binding whose position is not known. After a `send`, a probe that could not parse the command fails the step with `the probe rejected the command`. A `sit` on a prim that has a probe succeeds and prints a note, once, with the step's number:

```text
slate: step N: seated on <binding>: link numbers at and above the seated avatar's are not probed, and a message addressed only to one is not observed
```

```text
slate: step N: "Example Sign" is 40 m from the tester; a say is ordinary chat, not as far as the region lookup can see (say radius 20 m: heard at 19.5 m and not at 20.5 m)
slate: step N: tester position is not exact; the say was not sent
slate: step N: the position of "Example Sign" is not known; the say was not sent
```

**Link sentences.** A `link N` on a binding with no probe is looked up in the object store when it is used, and two things can stop it. Neither is a timeout; each names the object by its name in the world:

```text
slate: step N: the link order of "Example Tip Jar" is not known; a probe, or taking and rezzing it, gives it
slate: step N: "Example Tip Jar" has no link 9; it has 3 prims
```

The first is a set the store cannot put in order ([Link numbers](objects.md#link-numbers)): one that several prims joined in one update while it watched, or one a daemon older than the link numbers reports. The second is a number the set does not have; a set of one prim has link 0 only, and a larger set has 1 up to its prim count. A touch or drag fails at once, in its prepare step, and the sentence is printed as a line of its own and under `stimulus:`, with nothing sent. An expectation has no reading to fail on, and fails at its deadline with the sentence after its `unmatched` line, as a button reading's reason is, with the step's number:

```text
unmatched texture vendor link 2 face 0 is 6b5e7e57-7e57-c0de-5117-87121399d48f within 300ms; slate: step 1: the link order of "Example Tip Jar" is not known; a probe, or taking and rezzing it, gives it
```

A speaker or a dialog match says the sentence of the last line or dialog it could not place. A binding with a probe never says these; a link its probe did not report is `link N is not one of the probed links of <name> (<links>)`.

**Button sentences.** A button step is searched before anything is sent, so each of these fails the step with no touch, exit 1, and is printed as a line of its own with the step prefix. The two sentences for a planar face and a texture animation, and the two for `image` and `oval`, are under [Buttons](#buttons). The rest:

```text
slate: step N: "Test HUD" has no face 5; it has 3
slate: step N: the finder failed: <error>
slate: step N: button 4: the prim "Test HUD" has 2 matches: face 0 at 120,48; face 1 at 64,200
slate: step N: button matches 2 times on "Test HUD": face 0 at 120,48; face 1 at 64,200; write a number before the parts to choose one
slate: step N: button part text "Menu" matched nothing on "Test HUD"
slate: step N: every button part matched on "Test HUD", and no items of all the parts overlap
slate: step N: no face of "Test HUD" has a texture, so there is no picture to search
slate: step N: reading the faces: <error>
```

In order: `face N` names a face the prim does not have (the count is the prim's faces); the picture finder returned an error; `nth` is past the matches (`button <nth>`, then the number of tuples found, which can be 0); two or more tuples and no `nth`; the first part, in the order written, that matched nothing on any searched face; every part matched but no tuple's rectangles overlap ([Combination](#buttons)); no face has a texture; and the session could not give the faces or a face's picture. The two lists of centres are `face F at X,Y`, the first after a colon and each next after a semicolon, in face order, with `X,Y` in pixels of the face's picture.

A step that fails after a search, with one of the `button`, `every button part` or `finder failed` sentences, also prints one line for each textured face, after the sentence:

```text
slate: step N: face F picture: <path>
```

`<path>` is a PNG in a new directory in the system temp directory. A face whose file could not be written prints `slate: step N: face F picture not written: <error>` instead, and a directory that could not be made prints `slate: step N: face pictures not written: <error>` once. A sentence that comes before the search (the planar and animated faces, `image` and `oval`, no such face, no texture and the reading error) leaves no file, and a step that passes removes none because it writes none.

**Button reading and attached sentences.** A button reading fails at its deadline and not before anything is sent, so what a search refuses is said on the `unmatched` line, after `; `, as the sentence a touch would have printed. The sentences are those of [Buttons](#buttons) and of the button sentences above that a search can meet: the planar and animated faces, `image` and `oval`, `reading the faces`, `the finder failed`, and `"<name>" has no face N; it has M`. A negative one that had no reading fails with the line of every state expectation:

```text
unmatched button sign text "Close" box becomes gone within 8s; slate: step 3: face 2 of "Example Sign" is planar, so its offset, repeats and rotation are not the picture on it
unmatched attached hud on "chest" within 10s; last reading: attached hud HUD centre 2
unmatched attached hud on "chest" within 10s; no reading was taken
  failed: a negative attached needs a real reading, and none was taken
```

The second and third are an `attached` that did not hold, with the last reading taken or none. The last is a negative `attached` with no reading, as the negative state expectations have one.

**Wear and take off sentences.** The stimulus line of a step that passed its stimulus says what was done, and one that did not has the sentence as the stimulus error:

```text
wore "Example Hat" on HUD centre 2 as hud
took off "Example Hat"
slate: step N: "Example Hat" is already worn on HUD top; take it off first
hud is not worn
"Example Hat" is still in the store 10s after the take off
```

In order: a `wear` that succeeded; a `take off` that succeeded; an item that is worn, which fails before any request is sent and prints as a line of its own; a `take off` of a binding that is not worn; and a store that still lists the root when the stimulus budget ends. Any other failure is the error `sl` returned.

**Choose sentences.** A `choose` that cannot press one button fails the step before anything is sent, exit 1. The stimulus line of the block reads `not sent: ` and the sentence, with `OBJ` the script's name for the prim and the buttons quoted in the dialog's list order:

```text
no dialog is held for vendor
the dialog held for vendor is a text box; use answer
there is no button 7 on the dialog held for vendor: "Red" "Blue" "Cancel"
no button matches "^Gr" on the dialog held for vendor: "Red" "Blue" "Cancel"
2 buttons match "^[RB]" on the dialog held for vendor: "Red" "Blue" "Cancel"
2 buttons are called "Red" ("Red" "red"); a label must name one
"Green" is not one of the buttons of the dialog held for vendor: "Red" "Blue" "Cancel"
```

In order: no hold; a text box; `button N` past the last button; `matching` with no match; `matching` with several; a label, literal or from a capture, that folds to two buttons; and one that is none.

**Dialog details.** An `unmatched dialog` line ends with why the last dialog that met its object and its message did not meet the rest. The three reasons, the second naming the clause as it was written (`button`, then its number if pinned, then the literal, `matching` and the pattern, or the capture):

```text
unmatched dialog from hud button "Red" count 3 within 10s; count 3 but the dialog has 4 buttons
unmatched dialog from hud button 2 "Red" within 10s; clauses could not all be assigned: button 2 "Red" has no button
unmatched dialog from hud button matching "^[0-9]+$" button "7" within 10s; clauses could not all be assigned: button "7" has no button of its own
unmatched dialog from hud button "Red" only within 10s; only: "Cancel" is not claimed by a clause
```

An `ordered` or a `sorted` that fails ends the reason with the first pair it found, and when both fail the two are joined by `; `:

```text
unmatched dialog from hud button "a" button "b" ordered within 10s; ordered: "b" comes before "a"
unmatched dialog from hud sorted matching "^n([0-9]+)$" within 10s; sorted: "n10" comes before "n9"
```

`ordered` names the first pair of clauses whose buttons are the wrong way round: the dialog lists the first button before the second, and the clauses were written the other way. `sorted` names the first label that sorts before the label listed ahead of it. `has no button` is a clause no button satisfies at all, and `has no button of its own` one that fits only buttons the other clauses need. When no dialog met the message there is no reason, as before.

**Capture sentences.** A capture whose value breaks a rule that is static for a literal fails the step before the stimulus, with the capture, its value and the rule, and the line is printed twice like every sentence that begins `slate: step`. A group that did not take part is the `failed:` line of the block:

```text
slate: step N: $first is "" and a button part needs text
slate: step N: $line is "<text>" and link text must be bytes 0x20-0x7E, tab or newline
slate: step N: $line is "<text>" and the line to the bridge is <n> bytes and carries at most 1023
  failed: $name did not take part in the match
```

**Showing sentences.** A `touch showing` that cannot find one face fails the step before a touch is sent, exit 1, with one of these, the second listing each prim's name and face, root first, then its children, with each prim's faces in face order, separated by semicolons. A touch that was sent reports `touched "<prim>" face <n> showing <uuid>`:

```text
slate: step N: no face of "Test HUD"'s linkset shows <uuid>
slate: step N: 2 faces of "Test HUD"'s linkset show <uuid>: "Test HUD" face 1; "Example Panel" face 0
```

**Give and rez sentences.** These are the `  failed: <why>` line of the block, exit 1. A give, in the order it can go wrong:

```text
  failed: ambiguous: two offers match: "Example Red Swatch" from Example Vendor transaction <uuid> and "Example Red Swatch" from Example Vendor transaction <uuid>
  failed: the offer carries no asset type, so the folder it goes to is not known
  failed: the offer is of asset type 7, whose default folder is not known; only a script's has been measured
  failed: the accept was not sent: <error>
  failed: ambiguous: two new items match: <uuid> ("Example Red Swatch") and <uuid> ("Example Red Swatch")
```

The first is two offers that match, or an offer that matches after an accept was sent; the second is not accepted. The two items are in the order of their ids. The asset type is the number the offer carries; [Give](slate-runner.md#give) lists the types that have a folder.

A rez:

```text
  failed: a second root matches the rez claim <claim text>: <uuid> "Example Block" at 1.0 2.0 3.0 and <uuid> "Example Block" at 1.5 2.0 3.0
  failed: a new root <uuid> "Example Block" at 1.0 2.0 3.0 is from vendor and matches no rez claim (<why>; <why>)
```

Each `<why>` is the reason one claim did not take the root: `name "X"`, `description "X"`, or `owner <uuid> is not the owner of vendor`. A claim that did not match by the deadline ends its `unmatched` line with the report instead, one entry for each new root seen, `<uuid> "<name>" at <x> <y> <z> (<reason>)`:

```text
unmatched rez name "Example Block" from vendor within 10s; new roots rejected: <uuid> "Example Other Block" at 9.0 2.0 3.0 (name "Example Other Block"), <uuid> "Example Block" at 40.0 2.0 3.0 (distance 40.0 m from vendor)
unmatched rez name "Example Block" from vendor within 10s; no new root was seen
```

The reason is `position unknown`, `distance D m from OBJ`, `owner <uuid> is not the owner of OBJ`, `name "X"`, `description "X"`, `properties: <error>` when what could not be read was needed, `matches, and was taken by another claim`, or `not judged: its properties were not read`. A root whose name could not be read is listed `<uuid> "" at ... (properties: <error>)`.

A state expectation that is negative and had no reading by the end of its window, which is a failure and not a pass (see [Expectations](#expectations)), prints on its `unmatched` line `; no reading was taken, and a negative needs one`, and in the block:

```text
  failed: a negative texture, click or state needs a real reading, and none was taken
```

**Setup failures**, exit 3. The 30 s and 15 s are setup budgets, not the script timeout:

```text
slate: setup: "Example Sign" is not in the region, or is beyond the draw distance (looked up for 30s)
slate: setup: "Example Sign" names 2 objects
slate: setup: "Example Sign" with description "north" matches none of 2 objects of that name
slate: setup: "Example Sign" with description "^s" matches 2 objects (<uuid>, <uuid>)
slate: setup: "Example Sign" with description "north" is the same object as south (<uuid>): one object cannot be bound twice
slate: setup: item "Example Hat" in "Objects": <the error>
slate: setup: cannot install the probe in "Example Tip Jar": <install error>
slate: setup: probe in "Example Tip Jar" did not hello from <uuid> within 30s; ScriptsBlocked was empty and that is not proof the parcel will run the script (doc/ground.md)
slate: setup: no "slate bridge" in inventory; run slate -make-bridge once where the tester may build
slate: setup: bridge did not say ready within 30s
slate: setup: properties of "Example Tip Jar" (15s): <error>
slate: setup: the parent of "Example Tip Jar" is not in the store
slate: setup: <the sentence describing why scripts cannot run here>
slate: setup: click action was not on any update of "Example Chair"; this slate requires the slgod that stores click and click_known
```

`slate -make-bridge` prints, on standard output:

```text
slate: make-bridge: "slate bridge" is in the Objects folder
slate: make-bridge: "slate bridge" is already in the Objects folder; nothing was made
slate: make-bridge: <step> (<budget>): <error>
slate: make-bridge: the Objects folder: <error>
slate: make-bridge: reading the Objects folder: <error>
slate: make-bridge: reading the tester's position: <error>
slate: make-bridge: the tester's exact position is not known (the body is not in the store, or its seat is unknown); nothing was rezzed
```

The first is the item made, exit 0. The second is an item of exactly that name already there: exit 0 and nothing made, so the command is safe to run twice. The rest are failures, exit 3. The third is the rez, the rename or the take failing, and a prim it had already rezzed is deleted; the next two are the inventory not being read; the last is a position that is not exact, before anything is rezzed.

Before the probes are installed the runner prints `slate: setup: installing the probe in <n> prims` (`1 prim` for one), because the first install of a script waits about 6 s per prim; that wait is not a hello timeout.

The click line also appears, as exit 3, when a prim just bound with `as` is still unknown after its own 30 s wait; the rez step is then still open and no step-failure block is printed. A known value that has not yet become the expected one fails the later step at its own deadline, exit 1. The hello line does not claim the parcel will run the script.

**The command's streams.** A usage error (`slate: need exactly one FILE`, an unknown flag, a `-run` that does not compile or matches no test) and a file that did not parse go to standard error, and so does a failed dial, `slate: dial: <error>`, exit 3. Everything else, including a setup failure, is on standard output once.

**What a run leaves behind.** When a step fails, the test stops and its remaining steps are skipped; `after each` still runs, and the next test runs. A setup failure, an unknown click byte or a dropped subscription stops the whole run. The runner does not stand the avatar up, answer a dialog it has not answered, or send a payment again. A sit that succeeded leaves the avatar seated and the transcript says so. A `wear` leaves the item worn until a `take off`, and the runner does not take it off. A pending permission request is always denied and printed as `permission denied`. On every exit after setup started, the runner removes the `slate probe` scripts and takes the bridge off, keeping it in inventory for the next run, printing `slate: cleanup: removed the probe from <n> prims, settling 6s after each` first (`1 prim` for one), and that line comes before the verdict, so the run's last line is its result; a 20-prim object can spend about two minutes settling after the result is known. A cleanup error is a warning and changes neither the exit code nor an earlier failure. Rezzed objects, a received item and a payment are not undone. The runner itself leaves nothing in the region. A run killed mid-way can leave the bridge worn and probes in the product; the next run reuses or replaces them.

## Worked examples

Each is a complete script, using the default timeout. The names are invented, and no channel is anyone's real channel.

### A HUD button that changes another object's face

The first script above. The three expectations are one set, with an 8 s deadline. Two Open labels fail the step and list both centres. The texture uses `becomes`, so it passes only if the face changes to that texture; the offset and repeats use `is`, which are plain readings and also pass if `sign` already showed those values.

### Owner chat, a dialog, a give

`as owner of vendor` checks that the tester owns it, then says `menu` on channel −7. `choose` begins a new step and presses the held dialog's Red button; the chat line and the give are then unordered.

```slate
slate 1

object vendor is "Example Tip Jar"

say "menu" on -7 as owner of vendor
expect dialog from vendor text "Choose a colour" button "Red" button "Blue"

choose "Red" on vendor
expect say "red" on public from object vendor
expect give "Example Red Swatch" from vendor
```

If the tester does not own the tip jar, step 1 fails with `as owner of vendor but the tester does not own "Example Tip Jar"` and nothing is said.

### Pay, a give, two new objects, then touch one

The process must also run with `--pay`. The balloons share a name, so the descriptions tell them apart. A third balloon within 10 m of the vendor fails the step and binds neither name. `left` is not usable until this step passes, so the touch is a new step.

```slate
slate 1
allow pay

object vendor is "Example Tip Jar"

pay vendor L$5 reason "tip"
expect give "Example Thank You" from vendor
expect rez name "Example Balloon" description "left" from vendor as left
expect rez name "Example Balloon" description "right" from vendor as right

touch left button text "Pop"
expect say "pop" on public from object left
```

If the grid does not answer the payment, the failure text says whether the L$ moved, and the payment is not sent again. Balloons and the item are left. A second run still passes, because the new copy has a different item id.

### Sit, the click action, stand

A chair that starts as click-to-sit is expected to become click-to-touch once the avatar sits. `becomes touch` passes only when the click action reads touch after an earlier reading in the step's window did not, so it proves the change. `stand` is its own step because the runner never stands the avatar up on its own.

```slate
slate 1

object chair is "Example Chair"

sit chair
expect click chair becomes touch within 10s

stand
```

If the click expectation times out after a successful sit, the `seated:` line names the chair and the avatar is still on it.

### Drag a slider

The expectation is the offset the product sets, not the drag's own coordinates.

```slate
slate 1

object slider is "Example Slider"

drag slider face 0 from 0.1 0.5 to 0.9 0.5 over 500ms
expect offset slider face 0 is 0.4 0 within 10s
```

### Move and resize a HUD by its glass

[ExampleHUD](https://github.com/quark-idlemind/ExampleHUD) is a HUD that moves and resizes with the mouse. Its background is dragged to move it, and its glass, a transparent prim that grows over the screen when pressed, keeps the cursor on the HUD. The coordinates below are invented for the example.

```slate
slate 1
timeout 20s

item hud_item is "ExampleHUD" in "Objects"

before each {
  wear hud_item on "HUD centre 2" as hud
  expect attached hud on "HUD centre 2" within 10s
}

after each {
  take off hud
  expect attached hud off within 5s
}

test "moves and resizes" {
  # Grab the background low in the middle and take it 300 pixels right and
  # 200 down; the glass grows when pressed, so wait for it.
  drag hud on screen from face 0 at 0.5 0.9 by 300 200 over 800ms settle
  expect position hud changes within 10s

  # The resize corner is a point on the screen, here in the default
  # 1920x1025 view.
  drag hud on screen from 1480 640 by -150 100 over 800ms settle
  expect size hud becomes 0.8146 0.4073 0.1629 within 10s
}
```

The test passes when both drags are sent and released and the HUD has moved and then reached the size below.

The move can only be `changes`. The HUD moves by the drag in metres, 300/1025 m to the right and 200/1025 m down, which is 0.2927 and 0.1951 m ([Where a worn HUD is on the screen](hud-screen.md#a-drag)), but where it ends is that plus where ExampleHUD stood when it was worn, and the example does not know that. Slate has no arithmetic to add the two, and an `is` or `becomes` with a made-up start would be a number nobody measured.

The resize is derived, and it is only as good as the derivation. ExampleHUD's script scales the root by `1 + 2*|delta|/|<0.5,0.25>|`, with delta the pointer's change in metres; that is the formula in ExampleHUD's own script, and a drag of 150 and 100 pixels in a 2050-pixel view was measured scaling it by 1.3146, what the formula gives for that distance ([A drag](hud-screen.md#a-drag)); its root is 0.5 x 0.25 x 0.1 m as built at commit f174c9e. A drag of 150 and 100 pixels in a 1025-pixel view is a delta of 0.14634 and 0.09756 m, whose length is 0.17588, so the factor is 1.62925 and the size is 0.81462 x 0.40731 x 0.16292, written to four places inside the millimetre the reading is compared within. The press at `1480 640` is invented: it holds only if that point is on the HUD's resize corner. A different build, view height or press changes the numbers; a script that cannot say them writes `changes`. The `within 10s` is there because a drag with `settle` can take its duration plus the 5 s settle, and the step's budget is its longest `within` ([Static checks](#static-checks)).

### Anywhere, a link and a face

The first two says may come from any prim of the panel's linkset. The child answers, and `from object panel link 3` requires that one prim. This file has a probe on the panel, so the probe's report numbers link 3; without the `probe` line the store would, and the file would be the same.

```slate
slate 1

object panel is "Example Panel"
probe panel

touch panel anywhere
expect say "root" on public from object panel

touch panel face 2 at 0.9 0.5
expect say "face" on public from object panel

touch panel link 3
expect say "child" on public from object panel link 3
```

### A suite with before and after

One HUD and one sign, three tests. `before each` resets the sign, `after each` resets it again, and the sequence is the part all three tests share. Each test runs as: reset, its own steps, reset. The reset texture is the one `before each` waits for, so every test starts from it.

```slate
slate 1
timeout 10s

object hud is "Test HUD"
object sign is "Example Sign"

before each {
  touch hud button text "Reset"
  expect texture sign face 0 is 38487e57-7e57-c0de-623d-4ed9063f984b within 8s
}

after each {
  touch hud button text "Reset"
}

sequence press-open {
  touch hud button text "Open"
  expect texture sign face 0 becomes 47227e57-7e57-c0de-1611-3fe69d620af1 within 8s
}

test "open changes the texture" {
  do press-open
}

test "open moves the offset" {
  do press-open
  expect offset sign face 0 is 0.25 0 within 8s
}

test "close undoes open" {
  do press-open
  touch hud button text "Close"
  expect texture sign face 0 becomes 38487e57-7e57-c0de-623d-4ed9063f984b within 8s
}
```

In the third test the steps are numbered 1 to 4: the reset in `before each` (1), the `do press-open` step (2), the close (3) and the reset in `after each` (4). If the close fails, the block names `test "close undoes open" step 3`, the `after each` reset still runs, and the next test (if there were one) still starts. If the sign never shows the reset texture, step 1 of every test fails and the rest of each test is skipped, so a bad starting state is reported on all three tests.

### A toggle

A plain-steps file, so its one test is named after the file; call it `toggle.slate`. The first touch must change the texture, whatever it is. The second must bring it back to what it was when the test began.

```slate
slate 1

object hud is "Test HUD"
object sign is "Example Sign"

touch hud button text "Toggle"
expect texture sign face 0 changes within 8s

touch hud button text "Toggle"
expect texture sign face 0 becomes original within 8s
```

The runner reads face 0 of the sign from the start of the test, so the baseline of step 1 is the reading before the first touch, and `original` is that same reading. Step 2's baseline is the changed texture. A sign that never changed fails step 1; a sign that changed and then stayed changed fails step 2.

### A dialog button, chosen and then said

The product offers a list whose entries are generated, so the test cannot know a label. `button 1` is the first button in list order, and the named group binds its label as `$first`. `choose $first` presses that button, and the sign must say the same text. `count 4` says the dialog has four buttons. The capture is not usable in the step that binds it, so the `choose` is a new step.

```slate
slate 1

object hud is "Test HUD"
object sign is "Example Sign"

touch hud button text "List" box
expect dialog from hud button 1 matching "^(?P<first>.+)$" count 4 within 8s

choose $first on hud
expect say matching "^Now showing " on public from object hud
expect say $first on public from object sign
```

If the first button's label is empty after trimming, the pattern `.+` does not match it and the dialog is unmatched. A capture is the label trimmed, so a label with a trailing space is pressed and said as its trimmed text.

### A tile found by its picture

A palette keeps one picture on each of several prims. `is any` reads the texture that link 4 shows, whichever it is, and binds it as `$tile`. `touch hud showing $tile` then touches the one face of the HUD's linkset that shows it, and the sign must come to show the same texture. `becomes $tile` proves the sign changed. The probe here numbers link 4; without it the store would, and `showing` needs neither.

```slate
slate 1

object hud is "Test HUD"
object sign is "Example Sign"
probe hud

expect texture hud link 4 face 0 is any within 8s as $tile

touch hud showing $tile
expect texture sign face 0 becomes $tile within 8s
```

If two faces of the HUD show that texture, the touch is not sent and the step fails with the list of both.

### A full-bright toggle on every face

A button turns full-bright on for the whole sign, and a second press restores it. `face all` makes the reading every face of the sign, and `becomes on` holds only when every face is on and an earlier reading in the window was not. `original` is the tuple the sign had when the test began, which is all faces off if the test starts from a reset.

```slate
slate 1

object hud is "Test HUD"
object sign is "Example Sign"

touch hud button text "Bright"
expect fullbright sign face all becomes on within 8s

touch hud button text "Bright"
expect fullbright sign face all becomes original within 8s
```

### A HUD worn for each test

`before each` wears the HUD and waits for the product's own word that it is on; `after each` takes it off, since the runner never does. The HUD's script says `ready` about 2 s after `Wear` returns, so the `within 10s` of the first step covers it. `hud` is usable by the expectations of the step that binds it, and by the test and by `after each`. The item is not an object: it is named only in `wear`. The `expect attached hud off` is in the same step as the take off, the one place the name is still usable.

```slate
slate 1

item hat is "Example Hat" in "Objects"
object sign is "Example Sign"

before each {
  wear hat on "HUD Top Left" as hud
  expect attached hud on "HUD Top Left" within 10s
  expect say "ready" on public from object hud within 10s
}

after each {
  take off hud
  expect attached hud off within 5s
}

test "open lights the sign" {
  touch hud button text "Open"
  expect texture sign face 0 becomes 47227e57-7e57-c0de-1611-3fe69d620af1 within 8s
}
```

If an earlier run was killed with the hat still worn, step 1 of every test fails with `"Example Hat" is already worn on HUD top; take it off first`. Take it off by hand and run again.

### A button that goes and another that appears

A HUD swaps its `Open` label for `Close` in one texture change, and back. Both expectations are in the step that has the stimulus, so the baseline of each is the picture before the touch. `becomes gone` needs `Open` to have been seen first, so a label the finder cannot read fails the step instead of passing it. In the second step the baselines are the pictures after the first swap.

```slate
slate 1

object hud is "Test HUD"

touch hud button text "Open" box
expect button hud text "Open" box becomes gone within 8s
expect button hud text "Close" box becomes shown within 8s

touch hud button text "Close" box
expect button hud text "Close" box becomes gone within 8s
expect button hud text "Open" box becomes shown within 8s
```

### A guarded touch that converges on a known state

Each test must start with the HUD showing `Open`, whatever the last test left. `before each` presses `Close` only if it is there, and then requires `Open`, which is the state it is driving towards. If the HUD already shows `Open`, nothing is sent. If the `Open` label is misspelt, the step after the guard fails on every run, where without the rule it would be skipped.

```slate
slate 1

object hud is "Test HUD"
object sign is "Example Sign"

before each {
  touch hud button text "Close" box if shown
  then expect button hud text "Open" box is shown within 8s
}

test "open changes the sign" {
  touch hud button text "Open" box
  expect texture sign face 0 becomes 47227e57-7e57-c0de-1611-3fe69d620af1 within 8s
}
```

### A sorted dialog

The product lists the numbers a player can pick, between `Prev` and `Next`. The labels `n9`, `n10` and `n11` are in order as numbers, which the group of the pattern says, and would not be as text. `Prev` must come before `Next`, and `count 5` is the whole dialog. The `choose` is a new step, and it presses a label by its text.

```slate
slate 1

object hud is "Test HUD"

touch hud button text "Pick"
expect dialog from hud text "Pick a number" button "Prev" button "Next" ordered count 5 sorted matching "^n([0-9]+)$" within 8s

choose "n9" on hud
expect say "nine" on public from object hud
```

### Two objects of one name

Two signs are both called `Example Sign`. Their descriptions tell them apart at setup, one exactly and one by pattern, and a step then speaks of `north` and `south`.

```slate
slate 1

object north is "Example Sign" description "north"
object south is "Example Sign" description matching "^south"
object hud is "Test HUD"

touch hud button text "North"
expect texture north face 0 becomes 47227e57-7e57-c0de-1611-3fe69d620af1 within 8s
expect no texture south face 0 changes within 2s
```

If a third sign of that name has a description that matches neither, it is not bound and is ignored. If two signs match `north`, setup fails and lists both.

### Patterns

The vendor thanks the tipper by a name the test does not know, and must not say anything that looks like an error in the next two seconds. The pattern `^Thanks, .+!$` matches the first; `(?i)error` is unanchored and ignores case, so `ERROR` and `an error occurred` both trip the negative. The second step reads a balance that changes from run to run; `L\\$` in the file is the pattern `L\$`, a literal dollar sign.

```slate
slate 1

object vendor is "Example Tip Jar"

touch vendor button text "Tip"
expect say matching "^Thanks, .+!$" on public from object vendor
expect no say matching "(?i)error" on public from object vendor within 2s

touch vendor button text "Balance"
expect say matching "^Balance: L\\$[0-9]+$" on public from object vendor
```

### A combination button

A text label inside an outlined box, with a right arrow inside a circle, all four overlapping. The click is the centre of the intersection.

```slate
slate 1

object hud is "Test HUD"
object sign is "Example Sign"

touch hud button text "Next" box symbol "right arrow" circle
expect say "next" on public from object sign
```

### A text box

```slate
slate 1

object board is "Example Guest Book"

touch board button text "Sign"
expect textbox from board text "Name yourself"

answer "Example Resident" on board
expect say "Hello, Example Resident" on public from object board
```

### A link message, both directions

The root is asked to send 7, `ready`, to every other prim; the first child to report passes the first expectation. The second expects the product's answer: link 3 sends 9 back, and the root's probe reports it.

```slate
slate 1

object vendor is "Example Tip Jar"
probe vendor

send on vendor from link 1 to link others num 7 text "ready"
expect link on vendor from link 1 num 7 text "ready"

then
expect link on vendor from link 3 num 9 text "go" heard by 1
```

The two reports come from two different prims, and the grid does not preserve their relative order. If the product replies immediately, the `go` can reach the runner before the `ready`, and the `then` form, which wants it after, times out. The unordered form does not have that problem and is the one to use then:

```slate
send on vendor from link 1 to link others num 7 text "ready"
expect link on vendor from link 1 num 7 text "ready"
expect link on vendor from link 3 num 9 text "go" heard by 1
```

### A step that times out

```slate
slate 1

object sign is "Example Sign"

say "ping" on 1 as tester
expect say "pong" on public from object sign within 1s
```

The sign says nothing. The test of a plain-steps file is named after the file, here `timeout-pong`. After a little over one second the runner prints this and exits 1.

```text
slate: test "timeout-pong"
slate: fail timeout-pong.slate test "timeout-pong" step 1 lines 5-6
  stimulus: say "ping" on 1 as tester
    sent on channel 1 as the tester
  expectations:
    unmatched say "pong" on public from object sign within 1s
  waited: 1s
  heard during the step:
    (none)
  seated: no
  dialog left unanswered: no
  payment: none
slate: failed 1 of 1 tests
```

### A negative check

After the menu closes, the vendor must not announce an error in the next two seconds.

```slate
slate 1

object vendor is "Example Tip Jar"

say "menu" on -7 as tester
expect dialog from vendor text "Choose a colour" button "Red" button "Blue"
choose "Red" on vendor
expect say "red" on public from object vendor

then
expect no say "error" on public from object vendor within 2s
```

The `then` step waits the full two seconds. The word `error` from another speaker does not fail it; from the vendor it fails at once and quotes the line.

### A channel the viewer does not deliver

`listen 1` is what lets the bridge hear it. `on 1` is not public chat; the bridge forwards the line and the runner matches the raw tail.

```slate
slate 1

object vendor is "Example Tip Jar"
listen 1

say "ping" on 1 as tester
expect say "pong" on 1 from object vendor within 1s
```


## Known limitations

Each is discussed under [Open questions](slate-runner.md#open-questions).

- One binding is one prim found by its name, and told apart from objects of the same name only by a description. It cannot be qualified by owner or by distance, or resolved to a root.
- `image` and `oval` buttons are accepted and always fail, in a touch and in a button reading.
- The finder reads labels in the style it was tuned on, dark type on a lighter button: a word inside a dark outlined frame is not read, and a solid rectangle is not a `box` ([Limits found drawing test pictures for buttons](imgfind.md#limits-found-drawing-test-pictures-for-buttons)).
- A button reading is the finder's count. A label the finder never reads is a count of 0, which is why `becomes gone` is the form to write. A planar or animated face is no reading.
- A guarded touch takes no `button N`: two tuples fail the step, and it is not a way to choose one.
- `wear` takes an item from a top-level folder by name. The runner does not take off what a test wore.
- A worn HUD's position and size are read (`expect position`, `expect size`), as an offset in the HUD's own frame and a scale. Where it is on the screen is not: an expectation such as `expect position hud on screen at X Y` is not built, and the author works the offset out from the drag and the world view's height, as [the worked example](#move-and-resize-a-hud-by-its-glass) does.
- `touch ... button` does not use `Pick` yet: a button hidden behind another prim of a worn HUD is not reported as hidden, because the finder reads the picture of the face and does not ask what the viewer would press at that point.
- A child prim is named with `link N`, whose numbers come from the object store when there is no probe. The store cannot give the order of a set that several prims joined in one update while it watched, nor one it took from another agent, and `link N` on such a set fails with `the link order of "<name>" is not known; a probe, or taking and rezzing it, gives it`. A probe gives the order, but only in a product the tester owns; taking the object and rezzing it again makes the store know it ([Link numbers](objects.md#link-numbers)). A daemon older than the link numbers reports every order as not known. The link messages `send` and `expect link` always need a probe.
- One avatar is driven. Any wait is capped at 120 s.
- The bridge item is made once where the tester may build (`slate -make-bridge`).
- `face all` has no face count for a sculpt, a mesh, or a prim nothing has described. For every other prim the count comes from its shape ([How many faces a prim has](objects.md#how-many-faces-a-prim-has)), but a sculpt or a mesh sends no shape that gives one. The tuple then runs to the last face that differs from the entry's default and takes one more, the default itself, so a sculpt whose faces are split between two values, neither of them the default, can read with a phantom default face at the end, and `face all is X` then fails although every real face is X. The transcript says `slate: step N: the face count of "<name>" is not known (sculpt, mesh or not described); face all reads the faces its texture entry names`, once for each name in a test.
- `touch ... showing` has the same blindness for a sculpt or a mesh. A face that the entry does not name shows the entry's default texture, and the search decodes faces only as far as the last one the entry names (the same note is printed). Asking for the default texture can find several faces, so that the step fails as ambiguous, or none. Ask for a texture that a script put on a face.
