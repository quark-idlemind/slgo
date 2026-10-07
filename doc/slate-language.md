# Slate 1: the language

Slate is a language for testing Second Life products. A `.slate` file names the prims it will drive, then holds one test or several, each a list of steps. A step is one stimulus (a touch, a chat line, a payment, a sit, a drag, a dialog answer, a link message) and the effects that stimulus must produce. The effect is often on a different prim from the one that was touched: a button on a HUD changes a face on a sign, and the test says so.

This page is the reference for someone writing a test by hand. How a runner implements it is in [the runner design](slate-runner.md). Where a runner and this page disagree, the runner is wrong until the page is revised. The runner design's [Step lifecycle](slate-runner.md#step-lifecycle) states the same timing rules precisely, and the two must agree.

A runner takes one avatar, the tester, from an already-running slgod, and any second avatars the run is given ([A second avatar](#a-second-avatar)). It finds each declared prim by name, performs the stimuli as the tester (a few as a second avatar, when the file says so), watches chat, dialogs, messages, object changes and inventory, and prints a transcript and an exit code. A missing effect fails the test when its deadline passes. The run does not hang.

Link messages do not leave a linkset. A HUD that changes another object is observed as an effect on that other object (a texture, a chat line, a rez, a give), never as a link message crossing from the HUD to the sign. The only link messages a test can see are those a probe script inside the linkset reports.

## Goals and non-goals

A person can write a test after reading this page, with no Go and no pixel coordinates for an ordinary button. One stimulus can require several effects, on the prim touched or on another, and newly rezzed objects can be named for the rest of the file. A test can wear a HUD or another attachment from the tester's inventory, rez an item from it, watch a button appear and disappear, and take the attachment off again. Every wait has a deadline.

Slate drives the tester, and a second avatar only when the run is given one explicitly ([A second avatar](#a-second-avatar)); it does not pick other avatars for itself, drive the camera, walk or teleport, check particles or the product's inventory, edit the product's scripts, or delete objects (except those its own `rez` steps made), refund payments or remove items a run produced (except the copies its own `drop` steps put into an object). A permission request is printed and refused with no bits granted, so the product script is not left waiting, unless the file names the permission and the object with `allow permission` ([Pay](#stimuli) has the paragraph); `debit` is never one of them. `allow permission` covers the tester's requests only: a request to a second avatar is always refused. It does read the animations the tester is playing, which a product that animates its wearer changes: `expect animation` says whether one is playing and which object started it ([Animation](#expectations)). It never plays or stops one, and it does not read an animation's contents, its priority or its joints. It also reads the sounds objects play, which a product that rings, hums or clicks makes: `expect sound` says whether one was heard, from which object and how loud, and whether a loop is running ([Sound](#expectations)). Sounds are read, never played: it plays none, stops none, and it does not read a sound's contents, its length or what it is like to hear. A file that does not start with `slate 1` is rejected.

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

No word is reserved. The words below have a meaning, and each has it only where the grammar expects that word; anywhere else it is an ordinary word and can be a name. Within one production a word position is either a fixed word or a name, never a choice between the two, so the parser needs no list of forbidden names. An object binding, a probe, a linkmap, a sequence name, a `do` target and the name a rez binds with `as` each take any word: `object button is "Test HUD"` and `touch button button text "Open"` are both legal. A step begins with a stimulus word, `expect`, `then` or `do`, and the file begins with `slate 1`. An error message names the word that was expected. The in-world name is a string, so a prim may be called anything. `{` and `}` are tokens, not words. `null` is the UUID `00000000-0000-0000-0000-000000000000`. The words with a meaning are:

```text
slate timeout allow pay permission object avatar is probe linkmap listen
touch anywhere link face at button text pattern symbol image box circle oval
drag from to over press dwell say on as tester owner of avatar anyone
public debug direct sit stand wait choose answer send num key
expect no within then dialog textbox texture offset repeats rotation
position size turn click give rez name description heard by reason only null
substance stone metal glass wood flesh plastic rubber
light projector intensity radius falloff fov focus ambiance
all others children this root
buy play open media zoom disabled none
matching becomes changes original
test before after each sequence do
showing count any fullbright glow colour alpha on off
alphamode default blend mask emissive
normalmap specularmap glossiness environment
gltf override material emissive metallic roughness cutoff doublesided opaque
basetexture normaltexture ormtexture emissivetexture
baserepeats baseoffset baserotation normalrepeats normaloffset normalrotation
ormrepeats ormoffset ormrotation emissiverepeats emissiveoffset emissiverotation
item wear rez take attached animation sound looping stopped gain
ordered sorted shown gone if in near percent
folder holding
```

A capture is a `$` and a name: `$first`, `$tile_2`. It is its own token and not a word, so `$button` and an object called `button` never meet. `$` is a capture only before a letter. `L$5` and `L$ 5` keep their meaning because `L$` is tried first, and a `$` inside a string is an ordinary character, so `"L$5"` and the pattern `"L\\$[0-9]+"` are unchanged. A bare `$` is an illegal byte.

An illegal byte (including `;`) is a lexical error at that byte. Slate has no significant indentation.

## Syntactic grammar

This is a PEG. Alternatives are tried in the order written. Repetition is greedy and stops when the repeated element does not match. That is what makes a stimulus or `then` start a new step instead of being swallowed by the previous step's expectations.

```ebnf
script      = "slate" "1" header* ( suite / body )
header      = timeout / allow / objectDecl / avatarDecl / itemDecl / probe / linkmap / listen
timeout     = "timeout" duration
allow       = "allow" ( "pay" / "permission" permName+ "from" IDENT )
permName    = "take-controls" / "trigger-animation" / "attach" / "change-links"
            / "track-camera" / "control-camera" / "teleport" / "override-animations"
objectDecl  = "object" ident "is" string ( "description" text )?
avatarDecl  = "avatar" ident   (* a second avatar; no in-world name, the run is given its profile *)
itemDecl    = "item" ident "is" string "in" string   (* item name, top-level folder *)
probe       = "probe" ident
linkmap     = "linkmap" ident
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
            / wear / rezitem / takeoff / delete / drop / setgroup

touch       = "touch" binding target guard? asavatar?
asavatar    = "as" ident   (* a second avatar's binding *)
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
              "from" number number "to" number number dragtimes asavatar?
            / "drag" binding "on" "screen" "from" screenpoint
              ("to" / "by") number number dragtimes "settle"?
dragtimes   = ("over" duration)? ("press" duration)? ("dwell" duration)?
screenpoint = number number
            / ("link" integer)? "face" integer "at" number number
say         = "say" string "on" integer ("as" stimspeaker)?
stimspeaker = "tester" / "owner" "of" binding / "avatar" string / avatar
pay         = "pay" binding amount ("reason" string)?
amount      = "L$"? integer
sit         = "sit" binding
stand       = "stand"
wait        = "wait" duration
choose      = "choose" chooselabel "on" binding asavatar?
chooselabel = string / "matching" string / "button" integer / capture
answer      = "answer" string "on" binding asavatar?
send        = "send" "on" binding "from" "link" integer
              "to" "link" linktarget "num" integer "text" ( string / capture ) ("key" key)?
linktarget  = integer / "all" / "others" / "children" / "this" / "root"
key         = uuid / "null" / capture
wear        = "wear" ident "on" string "as" ident   (* item, attach point, new binding *)
rezitem     = "rez" ident ("at" / "by") number number number "as" ident   (* item, region position or offset from the tester, new binding *)
takeoff     = "take" "off" binding
delete      = "delete" binding   (* a name a rez expectation or a rez step bound *)
drop        = "drop" ident ( "into" binding link? / "onto" binding link? "face" integer )   (* item *)
setgroup    = "group" avatar ( string / "none" )

expectation = "expect" "no"? expectbody tolerance? ("within" duration)? ("as" capture)?
tolerance   = "near" number "percent"?
expectbody  = sayexp / dialogexp / boxexp / textureexp / offsetexp
            / repeatsexp / rotationexp / positionexp / sizeexp / turnexp / clickexp / substanceexp
            / lightexp / projectorexp / textexp / fullbrightexp / glowexp / colourexp / alphaexp / alphamodeexp / materialexp
            / gltfexp
            / buttonexp / attachexp / animationexp / soundexp
            / giveexp / rezexp / linkexp

text        = string / "matching" string / capture

sayexp      = "say" text "on" expchan "from" expspeaker
expchan     = integer / "public" / "owner" / "debug" / "direct"
expspeaker  = "tester" / "owner" "of" binding / "avatar" string / "avatar" avatar
            / "object" binding link? / "anyone"
avatar      = ident   (* a second avatar's binding *)
toavatar    = "to" avatar
dialogexp   = "dialog" "from" binding link? toavatar? ("text" text)? dbutton*
              "only"? "ordered"? ("count" integer)? sorted?
dbutton     = "button" integer? text
sorted      = "sorted" ( "matching" string )?
boxexp      = "textbox" "from" binding link? toavatar? "text" text
faceall     = "face" ( integer / "all" )
textureexp  = "texture" binding link? faceall ( "changes" / state uuidval )
offsetexp   = "offset" binding link? faceall ( "changes" / state pairval )
repeatsexp  = "repeats" binding link? faceall ( "changes" / state pairval )
rotationexp = "rotation" binding link? faceall ( "changes" / state numval )
positionexp = "position" binding link? ( "changes" / state vecval )
sizeexp     = "size" binding link? ( "changes" / state vecval )
turnexp     = "turn" binding link? ( "changes" / state vecval )   (* vecval is Euler degrees X Y Z *)
clickexp    = "click" binding link? ( "changes" / state clickval )
substanceexp = "substance" binding link? ( "changes" / state substanceval )
lightexp    = "light" binding link? ( "changes" / state onoff
                                    / lightprop ( "changes" / state ( numval / triple ) ) )
lightprop   = "colour" / "intensity" / "radius" / "falloff"   (* colour takes a triple, the rest a number *)
projectorexp = "projector" binding link? ( "changes" / state ( "off" / uuidval )
                                         / projprop ( "changes" / state numval ) )
projprop    = "fov" / "focus" / "ambiance"
textexp     = "text" binding link? ( "changes" / state textval )
textval     = "original" / "any" / text      (* any only after is, with as *)
fullbrightexp = "fullbright" binding link? faceall ( "changes" / state onoff )
glowexp     = "glow" binding link? faceall ( "changes" / state numval )
colourexp   = "colour" binding link? faceall ( "changes" / state triple )
alphaexp    = "alpha" binding link? faceall ( "changes" / state numval )
alphamodeexp = "alphamode" binding link? face ( "changes" / state modeval )
materialexp = ( "normalmap" / "specularmap" ) binding link? faceall ( "changes" / state mapval )
            / ( "glossiness" / "environment" ) binding link? faceall ( "changes" / state numval )
mapval      = "none" / uuidval       (* none is the null key: no such map *)
gltfexp     = "gltf" ( "override" binding link? face ( "changes" / state onoff )
                     / ( "material" / gltftex ) binding link? face ( "changes" / state ( "none" / uuidval ) )
                     / ( "colour" / "emissive" ) binding link? face ( "changes" / state ( "none" / triple ) )
                     / ( "alpha" / "metallic" / "roughness" / "cutoff" ) binding link? face ( "changes" / state ( "none" / numval ) )
                     / "alphamode" binding link? face ( "changes" / state ( "none" / gltfmode ) )
                     / gltfslot ( "repeats" / "offset" ) binding link? face ( "changes" / state ( "none" / pairval ) )
                     / gltfslot "rotation" binding link? face ( "changes" / state ( "none" / numval ) )
                     / "doublesided" binding link? face ( "changes" / state ( "none" / onoff ) ) )
                                                  (* one face, never "all"; none is a field the override does not set *)
gltftex     = "basetexture" / "normaltexture" / "ormtexture" / "emissivetexture"
gltfslot    = "base" / "normal" / "orm" / "emissive"
                                                  (* written as one word: baserepeats, normaloffset, ormrotation ... ;
                                                     a rotation is a fraction of a turn, -1 to 1, as a face's is *)
gltfmode    = "opaque" / "blend" / "mask"
buttonexp   = "button" binding link? part+ face? ( "changes" / state buttonval )
buttonval   = "shown" / "gone" / "count" integer / "original"
attachexp   = "attached" binding ( "on" string / "off" )
animationexp = "animation" uuid ( "changes" / state ( "on" / "off" ) ) ( "from" binding )?
soundexp    = "sound" uuid ( soundstate / soundheard )
soundstate  = ( "changes" / state ( "looping" / "stopped" ) ) ( "from" binding )?
soundheard  = ( "from" binding )? ( "gain" number )?
state       = "is" / "becomes"
named       = "original" / "any" / capture
uuidval     = uuid / named
pairval     = number number / named
numval      = number / named
clickval    = clickname / named
substanceval = substancename / named
substancename = "stone" / "metal" / "glass" / "wood" / "flesh" / "plastic" / "rubber" / "light"
onoff       = "on" / "off" / named
triple      = number number number / named
vecval      = number number number / named
clickname   = "touch" / "none" / "sit" / "buy" / "pay" / "open"
            / "play" / "media" / "zoom" / "disabled"
giveexp     = "give" text "from" binding toavatar?
            / "give" "folder" text "from" binding ("holding" text+)?
rezexp      = "rez" "name" text ("description" text)? "from" binding ("as" ident)?
linkexp     = "link" "on" binding "from" "link" integer "num" integer
              "text" text ("key" key)? ("heard" "by" integer)?

link        = "link" integer
binding     = ident
number      = float / integer
```

A stimulus not listed with `asavatar` or `stimspeaker` (`pay`, `sit`, `stand`, `wait`, `send`, `wear`, `rez`, `take off`, `delete`, `drop`, `group` and `drag ... on screen`) is read with an optional trailing `as ident` as well, so that [the static check](#static-checks) can say that it stays the tester's and not leave a grammar error at the word. In `stimspeaker`, `avatar` is a name written bare (`say "hi" on 0 as visitor`), and `avatar string` is the in-world name of the tester.

The in-world name appears only in `objectDecl` and `itemDecl`; a step refers to a prim by `binding`, one identifier, and to an item only in `wear`, `rezitem` and `drop`. The word `object` in `from object sign` is not a use of either. A stimulus and the `expect` lines right after it are one step, so the step after a guarded touch is written `then expect ...`. `button` in `buttonexp` reads the parts of a touch. The parser accepts a number before them, as a touch has, so that the static check can say it is not legal there. A file may open with a step that has no stimulus (the third `step` alternative); its failure block prints `stimulus: (none)`. A bare `then` is rejected. A `call` is a step of its own and takes no expectations: an expectation after `do NAME` starts a new step.

`named` is a value that is not written out: `original` is the reading when the test began, `any` is a reading whatever it is, and a capture is a value an earlier step bound. The grammar lets all three stand wherever a value goes, and [Static checks](#static-checks) says where each is legal: `any` only after `is` and only with `as`, a capture only where its type fits. A rez names the object it finds with `as` followed by a name, before `within`; `as` followed by a `$` name, after `within`, is the capture of any other positive expectation. The parser reads `face`, `link` and `button` beside `showing` so that the static check can name the clash, and `showing` takes a UUID or a capture only.

A file is either a suite (top-level `before each`, `after each`, `sequence` and `test` blocks) or plain steps. A file that has both is a parse error. A plain-steps file is one test, named after the file; the PEG tries `suite` first, so a file that starts with a block and then has a bare step stops at the step and is rejected there. In `from object sign link 3` and in `dialog from vendor link 2`, `link 3` and `link 2` are the `link` production, not a stimulus. `number` accepts an integer as its exact value (`1` is `1.0`); a face, link, num or channel does not accept a float.

## Static checks

Static checks run after the parse, before anything is dialled. A failure is exit code 2 and prints `script:line:column:` and the reason. No region call has been made and nothing has been paid.

| Check | Rule |
|---|---|
| Shape | The first statement is `slate 1`. Every header precedes the first stimulus, expectation, `then`, `do` or top-level block. A file with no step and no test is an error. A plain-steps file cannot also have `before each`, `after each`, `sequence` or `test`; mixing the two forms is a parse error. |
| Timeout | At most one `timeout`. Absent means 10s. Any duration is at least 100ms (a shorter deadline could pass between the send and the first read) and at most 120s (so a typo cannot become an hour). |
| Pay gate | At most one `allow pay`. A `pay` without it is an error. |
| Permissions | Each name of an `allow permission` is one of the eight words; any other is `X is not a permission a file can name (one of ...)`, and `debit` is `a permission never grants debit; pay with allow pay and --pay`. `from` names an `object` header or an `item` header, else `X is not an object or an item`; the header may come before the one it names. The same name twice for one object, or in several headers, is harmless: the allowed bits are added. |
| Objects | Binding identifiers are unique across objects, items and the names `wear` and `rez` bind, and any word may be one. In-world name strings are non-empty. One in-world name may be written on several headers only when every one of them has a `description` and no two are written the same: `description "x"` and `description matching "x"` are different, two of either are not. Otherwise the error is `in-world name "N" is already used; objects of one name need a description each`, or `... is already used with that description`. |
| Descriptions | A description is a string or `matching "RE"`; the pattern compiles. A capture is refused, because a description is read at setup, before any step. The empty string is legal. |
| Avatars | An `avatar` binding is neither an object nor an item and shares their names: a name bound twice (`X is already bound`), by an `avatar`, an `object`, an `item`, or by the name `wear`, `rez` or a rez expectation binds, is refused. An avatar is used only after `as` on `touch`, a face `drag`, `say`, `choose` and `answer`, after `to` on `dialog`, `textbox` and `give` expectations, after `from avatar` on `say`, and as the first name of `group`. A name that is not an avatar there is `X is not an avatar; declare it with avatar X and give it with --avatar`, an object there is `X is an object, not an avatar`, and an avatar where an object is wanted is `X is an avatar, not an object`. `as NAME` on `pay`, `sit`, `stand`, `wait`, `send`, `take off`, `delete`, `drop` and `drag ... on screen` is `<stimulus> stays the tester's: a second avatar only touches, drags a face, says, chooses and answers (a second avatar never pays)`; `wear` and `rez` take `as` for the new object, and an avatar's name there is `X is a second avatar, and a second avatar never wears` (`rezzes`). The check does not know whether the run is given the avatar: that is a setup error ([A second avatar](#a-second-avatar)). |
| Items | An item binding is not an object: it is used only by `wear`, `rez` and `drop`, and any other use is `X is an item; only wear, rez and drop use an item`. The item name and the folder name are non-empty after trimming. |
| Drop | `drop` takes an item and an object (`X is an object; drop takes an item`, or `X is not an item`; the object is checked as any reference is). `link N` is 0 or more and `face N` is 0 or more, and each fits an integer. A drop is the tester's alone: `drop ... as NAME` is `drop stays the tester's: a second avatar only touches, ...`. |
| Substance | The word after `substance` is one of the eight names (`stone`, `metal`, `glass`, `wood`, `flesh`, `plastic`, `rubber`, `light`), `original`, `any` (after `is`, with `as`) or a capture of a substance reading; any other word, a number and a capital letter are refused at the parse, `expected a material name (...) or original, found ...`. It takes no `face`, and no `near`. |
| Near | `near` is read after any expectation, and only a state expectation of a number takes one: position, size, turn, offset, repeats, rotation, glow, colour, alpha, glossiness, environment, the `gain` of a sound heard, the numbers of a light and a projector, and the colours, factors and texture transforms of a GLTF override. On any other, `say`, `dialog`, `textbox`, `give`, `rez`, `link`, `attached`, `animation`, `sound` with no `gain`, `button`, `texture`, `click`, `substance`, `text`, `fullbright`, `alphamode`, `normalmap`, `specularmap`, a `light` with no property word or a `projector` with none, a `gltf` with `override`, `material`, `alphamode`, `doublesided` or a texture, it is refused with `<word> takes no near: near gives a number a margin, and only position, size, turn, offset, repeats, rotation, glow, colour, alpha, glossiness, environment, a sound's gain, the numbers of a light or a projector and the numbers of a GLTF override are numbers a reading can be off by`, and beside `is any` with `is any takes no near: it matches whatever the reading is`. The number is exact and above 0 (`near 0 is not above 0; leave near out to compare as the reading is quantised`); with `percent` it is at most 100 (`near 100.5 percent is above 100`). A rotation takes no `percent`, and a GLTF slot's rotation is one (`a rotation takes near in turns, not percent: an angle has no size to be a share of`), and neither does a turn (`a turn takes near in degrees, not percent: an angle has no size to be a share of`). |
| Group | `group` names a second avatar, declared with an `avatar` header (`X is not an avatar; ...`, `X is an object, not an avatar`). `group tester ...` is refused: `group stays off the tester: its active group decides where it may build, and a test does not change it; group takes a second avatar`. The group is a non-empty string or the word `none`; a quoted `"none"` is a group of that name. An `as` after it is `group names the avatar whose group is set (group NAME "Group Name"); an as does not follow it`. Whether the avatar has joined the group is not known here: that fails the step. |
| Probes | The identifier is an object binding, with at most one probe per object. A probe has no channels; the runner picks them. |
| Linkmaps | The identifier is a header object, not a name a step binds with `as`, `wear` or `rez` (`X is not an object; linkmap names a header object, ...`), at most one `linkmap` per object (`X already has a linkmap`), with no `probe` on it, in either order (`X has a probe, whose hello already gives every link number; ...`), and some step addresses it with `link N` (`no step addresses X with link N, ...`): a header that would drop a script for nothing is refused. |
| Listens | Each `listen` channel is a 32-bit integer, not 0 and not 2147483647. Duplicates are an error, and there are at most 63 of them. The bridge script opens one listen for its own control channel and one per `listen` channel; LSL allows 65 in one script (published, not measured here), and one is kept spare. |
| References | Every object in a step is a header binding, a name bound with `as` on a positive rez expectation in an earlier step of the same test, or a name bound by `wear` or by a `rez` step. A name bound in a step is not usable in that same step, except a name `wear` or `rez` binds, which that step's own expectations may use. A name `take off` ends is not usable from the next step on: `h was taken off at line N; it cannot be used again in this test`. `delete NAME` takes only a name a positive rez expectation or a `rez` step bound in this test (`X is an object header; delete takes a name a rez expectation (expect rez ... as NAME) or a rez step bound`, `X is an item`, `X is not bound by a rez in this test`), in the same way as other references: a name bound in the same step is `X is bound in this step`. In `after each` it may also name one bound in some test's body, which the references rule otherwise hides; see [Delete](#stimuli). A capture is a different thing and has its own rows below. |
| Rez names | A positive rez expectation has `as`, a negative one does not. The identifier is not already a binding and is not bound twice in one test. |
| `as` scope | A name bound in `before each`, by a rez expectation, by `wear` or by a `rez` step, is visible in the test and in `after each`. A name bound in a test body is not visible in `after each`, because the test may have failed before binding it. A name bound in a sequence is visible to the caller after the `do`. Each test is checked on its expanded steps (`before each`, the test with every `do` inlined, `after each`). |
| Speakers | A stimulus speaks `as tester` (the default), `as owner of` an object, `as avatar "Name"` (the tester's own name), or `as NAME` for a second avatar (the bare name; an expectation writes `from avatar NAME`). `as object` and `anyone` are expectation-only. |
| Channels on `say` | A stimulus takes an integer. `public`, `owner`, `debug` and `direct` belong to expectations. An expectation on an integer other than 0 or 2147483647 requires a `listen` for it. `on 0` and `on public` are the same channel, as are `on 2147483647` and `on debug`. |
| Buttons | At least one part. `nth` is 1 or more. `pattern` is a regular expression in Go's RE2 syntax and must compile. A literal `text` is non-empty after trimming; a capture used as `text` is checked at the step. |
| Button readings | `expect button` takes the parts of a touch and they are checked as a touch's are, but not `nth`: `button N is not legal in an expectation: a reading is the count of the buttons found, not one of them`. `count` is 0 or more. `link N` needs no probe, as on every expectation ([Objects](#objects-names-and-link-numbers)). A reading takes `as $x` when it is positive, and the capture is a number. `image` and `oval` parts pass and fail at run time. |
| Guarded touch | `if shown` is legal only on a touch whose target is `button` (`if shown guards a touch of a button`), never with `button N` (the guard counts the buttons found, not one of them), and only in `before each` and `after each` (a test is one path, and a guard that may skip its touch makes two). A sequence is checked in the block that calls it. A guarded step has no expectations of its own, and the next step of the same block has a positive expectation, so that a label that is misspelt fails there and is not skipped on every run. |
| Link stimulus | `send` requires a probe on that object. A touch or drag that names `link` does not: the store numbers the links when there is no probe. |
| Link on an expectation | `link N` on a touch, a drag or an expectation (`from object OBJ link N`, `dialog from`, `textbox from`, and the state expectations) needs no probe, and N is 0 or more. A probe is needed only where the probe itself is the mechanism, which is the link messages: `send on OBJ ...` (and so its `from link A`) and `expect link on OBJ ...`; each is refused with `<what> OBJ needs a probe`. |
| Tests | A `test` name is a non-empty string, unique in the file, and there is at least one test in a suite. At most one `before each` and at most one `after each`. Top-level items may come in any order. |
| Sequences | Sequence names are unique. A `do` names a defined sequence (it may be defined after its use). A sequence may `do` another but not cyclically; a cycle or an undefined name is an error. The other checks run on each test's expanded steps, so a sequence no test calls is checked only for its `do` calls. |
| Matching | A `matching` pattern is a regular expression in Go's RE2 syntax and must compile. `matching` is legal only where the grammar writes `text`: `say`, `dialog` and `textbox` message, a `dialog` button clause, `give`, `rez` name and description, link text, a floating text expectation, and an object's `description`; and in `choose matching` and `sorted matching`, which take a string. |
| State words | `becomes`, `changes`, `original` and `any` are legal only on the state expectations (texture, offset, repeats, rotation, position, size, turn, light, projector, click, substance, text, fullbright, glow, colour, alpha, alphamode, normalmap, specularmap, glossiness, environment, gltf, and the button reading, which has no `any`; alphamode has no `any` either). `changes` takes no value; `is` and `becomes` require one. |
| Length | A `say` on a negative channel is at most 254 bytes (it travels as a dialog reply). A `say` on any other channel is at most 1023 bytes, so the chat field does not cut it. An `answer` body is at most 254 bytes. A `send` text, once quoted, must leave the whole relayed control line within 1023 bytes, because the tester sends the command to the worn bridge as one chat line on a positive channel (measured: a 1000-byte line arrived whole). `choose` has no length check; a label the dialog did not offer fails at the step. A capture used in `send` text is checked at the step, before anything is sent. |
| Link text | A `send` text, and a link expectation's literal text, is printable ASCII (0x20 to 0x7E) plus tab and newline, written `\t` and `\n`. The probe's length check is then a character count, and any other character is a static error rather than a mangled report. A capture used as link text is checked at the step. A `matching` pattern on a link expectation is not limited to that character set, because only a literal is put on the wire. |
| Floats | An offset or rotation literal lies in [−1, 1]. Repeats are unrestricted. `original`, `any` and a capture are not literals and are not range-checked. |
| Position and size | A `position` or `size` literal is three numbers. A position may be any number, negative or zero. Each of the three numbers of a size is above 0: `size 0 is not above 0`. `link N` is checked as on every expectation, and neither has a `face`. |
| Turn | A `turn` literal is three numbers, Euler degrees X Y Z, each any number, negative, zero or past 360. `link N` is checked as on every expectation, and there is no `face`. |
| Light and projector | `light` and `projector` take no `face`, and `link N` is checked as on every expectation. A property word of a light is `colour`, `intensity`, `radius` or `falloff`, and of a projector `fov`, `focus` or `ambiance`; the other kind's word is `expected is, becomes, or changes`. A light's literal is `on` or `off`, and a projector's is a texture id or `off`. A light colour is three numbers and an intensity one, each in 0 to 1 (`light colour 1.5 is outside 0 to 1`); a radius, falloff, field of view, focus and ambiance are any exact number. A capture has the type of what is read: on or off, a triple, a number, a texture id. Only a property takes `near`. |
| GLTF materials | A `gltf` expectation names one prop, one object and one face: `face all` is refused (`gltf reads one face's material at a time; there is no face all`), as is a face past 44 (`face 45 is past the 45 faces a prim has`), and another word for the prop is `expected override, material, colour, ...`. `override` takes `on` or `off`; every other prop takes the word `none` too. A colour or emissive literal is three numbers and every other number one, each in 0 to 1 (`gltf metallic 2 is outside 0 to 1`). A material and a texture are ids, and `none` is not a value to bind: `as $x` on a literal `none` is refused (`none is no value to bind`). A capture has the type of what is read: on or off for `override` and `doublesided`, a triple for the colours, a number for the factors, text for `alphamode` and a texture id for the rest. A slot's repeats and offset are two numbers, any (an offset is not held to 0 to 1 here, as the override carries what the script gave), and its rotation is one number in -1 to 1 (`gltf baserotation 1.5 is outside -1 to 1`), in turns. Only the colours, the factors and the transforms take `near`, and a rotation takes no `percent`. A capture is a pair for repeats and offset and a number for a rotation. |
| Floating text | A `text` expectation takes a string, `matching "RE"`, `original`, `any` (after `is`, with `as`) or a capture. A pattern must compile, and its named groups bind text as in any other `matching` clause. A capture must be text: one bound by `say`, `dialog`, `textbox`, `give` or another `text`, and a text capture is usable here and wherever text is. `link N` is checked as on every expectation, and there is no `face`. |
| Material maps | A `normalmap` or `specularmap` takes a uuid, `none`, `original`, `any` (after `is`, with `as`) or a uuid capture; another word is `expected a UUID, none or original`. A `glossiness` or `environment` takes a number, `original`, `any` or a number capture. A capture of the one kind is refused where the other is wanted (`capture type mismatch`), and a `face all` capture is for `face all`, as for every face property. `near` is refused on a map and allowed on a level. |
| Alpha mode | An `alphamode` expectation names one face: `face all` is refused (`alphamode reads one face's material at a time; there is no face all`). Its value is `default`, `none`, `blend`, `mask`, `emissive` or `original`; `any` and a capture are parse errors as its value, since a mode is a word; `as $x` after it binds the mode as text ([Captures](#expectations)), and a text capture is usable wherever text is, a `say` expectation's text for one, and is refused where a uuid, a number or any other type is wanted. `link N` is checked as on every expectation. |
| Levels | A glow, alpha or colour literal lies in [0, 1]; each of the three numbers of a colour is checked alone. The error names the property and the number: `glow 1.5 is outside 0 to 1`. A glossiness or environment literal is a whole number from 0 to 255: `glossiness 256 is outside 0 to 255`, `glossiness 12.5 is not a whole number: it is a level from 0 to 255`. |
| Origin | `at 0 0` is an error (on `face` and on `showing` alike), and so is a drag whose `from` or `to` is `0 0`, whether written `0` or `0.0`. The error is `at 0 0 is the middle of the face; placeTouches treats a zero ST as not given (sl/touch.go)`. The touch at 0 0 is treated as the middle of the face, so it is rejected. `at 0 0.5` is legal. |
| Drag | A `drag … over D` must fit its step's budget: `D` is at most the longest `within` in the step, or the `timeout` when it has none, because the drag blocks for `D` and the step gives a blocking stimulus no more than that. With `press` or `dwell` the three together must fit (`over` counting 500 ms when omitted): `drag a face 0 from 0.1 0.5 to 0.9 0.5 over 4s press 4s dwell 4s` in a step that allows 10 s is refused with `drag over 4s, press 4s, dwell 4s can take 12s, which is longer than the step's budget of 10s, ...`. |
| Wait | `wait D` is a duration in the usual range, and must fit its step's budget as a drag's `over` does: `wait 12s` in a step that allows 10 s is refused with `wait 12s is longer than the step's budget of 10s, ...`. |
| Drag on the screen | The same budget holds for `drag OBJ on screen`, and `settle` adds the longest it may wait for the HUD to change, `Options.HUDChangeTimeout`, which is 5 s unless a session is given another: `D` (500 ms when `over` is omitted), any `press` and `dwell`, plus 5 s must fit. `drag h on screen ... over 6s settle` in a step that allows 8 s is refused, with `drag over 6s and settle (up to 5s) can take 11s, which is longer than the step's budget of 8s`. The origin rule does not apply: 0 0 is a corner of the screen, and `at 0 0` a corner of a face. |
| Binding on a HUD point | `drag OBJ on screen` needs an object worn on a HUD point (attachment points 31 to 38). Where the file says how the binding was put on, that is refused when it is checked: a binding `wear` put on a point that is not a HUD one (`h is worn on chest; drag on screen needs an object worn on a HUD point`), and one a `rez` expectation bound (`made is rezzed in the world; ...`). A header binding, which may be worn or not, is judged when the step runs ([Drag on the screen](#stimuli)). |
| Money | A pay amount is an integer of at least 1. A reason is at most 127 bytes. |
| Dialog buttons | A `button N` in a dialog expectation, and in `choose button N`, is 1 to 12, the most buttons `llDialog` accepts (published, not measured). `count` is 1 to 12. When `count` is written, the number of `button` clauses is at most `count`, since each clause needs a button of its own. Two clauses may not be pinned to the same `N`. With `only` and `count N` there are exactly N clauses, because `only` gives every button to a clause. |
| Ordered and sorted | `ordered` needs at least two button clauses. A `sorted matching` pattern compiles and has at most one capturing group, the text to compare. A named group of a `sorted` pattern binds nothing. |
| Dialog shape | `choose` and `answer` are not checked against a preceding dialog. That depends on the region and fails at run time; only the range of `button N` and the pattern of `matching` are checked. |
| Animation | The id is a UUID that is not the null key (`the null key names no animation`). The value is `on` or `off`: `original`, `any` and a capture are refused (`an animation is on or off and has no original; write on or off`, `expected on or off`), and `changes` takes none. `from` names an object binding. `near` is refused, and so is `as` (`this expectation has no reading to bind`). |
| Sound | The id is a UUID that is not the null key (`the null key names no sound`). `gain` is from 0 to 1 (`a sound's gain is from 0 to 1`) and is written only on a sound heard, not beside `is`, `becomes` or `changes` (the parser stops at it). The value of a state is `looping` or `stopped`: `original`, `any` and a capture are refused (`a loop is looping or stopped and has no original; write looping or stopped`, `expected looping or stopped`), and `changes` takes none. `from` names an object binding. `near` is refused unless there is a `gain` for it to widen, and so is `as` (`this expectation has no reading to bind`). |
| Wear | The first name of `wear` is an item (`X is an object; wear takes an item`, or `X is not an item`). The attach point, in `wear` and in `attached ... on`, is a name `sl` knows, in any case and with the viewer's spelling or `sl`'s; a name it does not know is refused, not guessed at. The name after `as` is new, and unique among objects, items and the other names `wear` and `rez` bind. A `take off` and an `attached` name an object binding, which is a header object or a name `wear` bound. |
| Rez | The first name of `rez` is an item (`X is an object; rez takes an item`, or `X is not an item`). It has exactly one of `at` and `by`, then three numbers, each exact (a number that does not fit a float is `number is not an exact float`), and `as` with a new name, unique as `wear`'s is (`X is already bound`). The name is an object binding like a header's, and is rezzed in the world: `drag ... on screen` refuses it. A step's first word `rez` is this stimulus; after `expect` it is the rez expectation, and the parser tells them apart by that position alone. |
| Capture types | A capture has the type of the place that binds it, and a use must be of the same type. The types are text (a line, a message, an item name, a group, a button label, a floating text), uuid (a texture), pair (offset, repeats), number (rotation, glow, alpha, the count of a button reading), click, substance (a physical material), colour triple, vector (position, size; a position capture can be used by a size and the other way round) and on or off (fullbright). Text is never accepted where a UUID is expected, even when it looks like one. `key` and `showing` take a uuid capture; `text`, `choose`, `send` text and a dialog button take a text capture. `choose matching $x` is refused, because a capture is data and never a pattern, and a capture is not usable inside a `matching` pattern. |
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

- A step that begins with a stimulus arms just before the stimulus is sent; for a `wear`, before the request to wear, and for a `rez`, before the request to rez. The first step of each test arms when that test starts, so events from an earlier test are never eligible.
- The one exception is `wait`. Every other stimulus (`touch`, `say`, `drag`, `pay`, `sit`, `stand`, `choose`, `answer`, `send`, `wear`, `rez`, `take off`, `delete`, `drop`, `group`) has an effect that is the point, and the product may answer before the stimulus has returned, so the step arms before it is sent and nothing the product does is missed. A `wait`'s effect is time, and what a test says after one is about the state it ended in, so its step arms when the wait returns: the readings are taken again then, and the baseline of `becomes` and `changes`, the reading a plain `is` may count as already so, and the value `is any as $x` binds are the state at the wait's end. A line, dialog or other event heard during the wait is not the step's, and a change that happened during it is not a change after it: a test that wants it writes the expectation in the step before the wait, or in a step of its own.
- A `then` step, or an expectation-only step after another step, arms at the latest of: the previous step's arm point, the time its stimulus returned, and the observation time of every event its positive expectations matched. It starts when the previous step passed. An event that arrives while the previous step is still in a negative window, a rez settle or a click wait is therefore not lost.
- An expectation-only first step arms when its test started (for a plain-steps file, when setup finished).

**`then` and ordering.** `then` means "after what the previous set matched". It can order only events whose order the grid preserves. Reports from two different prims have no guaranteed relative order. A texture update and a chat line from a script are not ordered by any promise the region makes either. Write `then` only for a real sequence; otherwise put the expectations in one set.

**Touches close together.** Slate keeps 90 ms between a release of a linkset and the next press of any prim of it, so two touches of one object in a row are both delivered and a test needs no `wait` for it ([a second touch waits for the first to be over](touch-spacing.md)).

**Timeouts.** The default is 10 seconds, from the `timeout` header or the built-in value. `within D` replaces it for that expectation only. A step's deadline is its start time, plus the time any blocking stimulus actually blocked on the way to success, plus the longest expectation duration in the step. A positive expectation still unmatched when its own duration has elapsed fails the step at that moment, even if another has time left. A negative expectation fails the step the moment the forbidden event is seen.

**Stimulus budget.** A blocking stimulus (`drag`, `sit`, `stand`, `wait`, `pay`, `wear`, `rez`, `take off`, `delete`, `drop`, `group`) may take the longest `within` in its step, or the default if there is none; a step with no expectations passes when the stimulus returns inside that budget. If the stimulus fails, the step fails at once. Setup, the tester-position read and the 30 s click wait are not on the step's deadline; setup budgets are under [Reading the result](#reading-the-result).

**When a step passes.** With only positive expectations, as soon as all have matched. With any negative expectation, after the positives have matched and every negative window has elapsed. A step with no expectations passes when the stimulus succeeds; one with only negative expectations passes when its windows elapse. A step with a positive `rez` waits one more 250 ms object poll before passing, and a second prim that matches the same claim in that wait fails the step. A click expectation on a name bound with `as` makes the rez step wait up to 30 s to read the click action; see [Step lifecycle](slate-runner.md#step-lifecycle).

**Dialog hold.** A dialog matched by `expect dialog` or `expect textbox` is held for that object, and for whom it came to (the tester, or a second avatar named by `to`), until a `choose` or `answer` on that object, and as the same avatar, consumes it, a later matched dialog from the same object to the same avatar replaces it, or its test ends. The tester's dialog from a sign and a second avatar's dialog from the same sign are two holds, and neither replaces the other. It is not dropped when the step that matched it ends, so a `choose` in the next step finds it. At the end of a test an unconsumed hold is forgotten and reported on that test's `dialog left unanswered:` line.

## Tests, sequences, before and after

A file is either plain steps or a suite. Plain steps are one test, named after the file's base name without `.slate`. A suite holds named tests and the blocks that support them. The two forms cannot be mixed. Timing inside any test is as under [Steps and timing](#steps-and-timing).

```slate
sequence NAME { ... }
before each { ... }
after each { ... }
test "NAME" { ... }
```

**Tests.** A `test "name" { steps }` block is one test. Names are non-empty and unique in the file, and a suite has at least one. Top-level items may come in any order. Tests run in file order.

**Before each and after each.** At most one of each. Every test runs `before each`'s steps, then its own, then `after each`'s, as one sequence of steps numbered from 1; the transcript's `pass step N` counts through all three. `before each` should put the world into the state each test needs, and `after each` should undo what the tests do (for example `stand`, or `take off` for what `wear` put on). The runner itself never stands, answers, retries a payment or takes off, and deletes only what its own `rez` steps made, at the end of each test, after `after each` ([Rez](#stimuli)). An object a product rezzed is deleted when a test says so with `delete`, and `after each` may do it for a name the test body bound ([Delete](#stimuli)).

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

The runner drives the tester, and a second avatar for each `avatar` header the run is given.

| Phrase | On a stimulus | On an expectation |
|---|---|---|
| `tester` | The default. The avatar speaks or touches. | The speaker is the tester. |
| `NAME` (`as NAME`; `from avatar NAME`) | `say "..." on N as NAME`: the second avatar `NAME` speaks, on its own session. Written without `avatar`. | The speaker is the second avatar `NAME`, matched by its id and not by its name. The tester hears it as it hears anyone, so it must be within earshot. |
| `owner of OBJ` | Legal only when the tester owns `OBJ`; the tester then speaks, being the owner. Otherwise the step fails before anything is said. A group-deeded object fails this check; write `as tester`. If the owner is not known the step fails with `the owner of OBJ is not known`. | The speaker is `OBJ`'s owner. |
| `avatar "Name"` | Legal in the file. At the step, the name is compared with the tester's displayed name, whole string, ignoring case. If it is not the tester, the step fails with `this runner drives only the tester` and does not speak (to speak as another avatar, declare it with `avatar NAME` and write `as NAME`). | The speaker's whole name equals it, ignoring case. A prefix or one half of a display name does not match. |
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

A touch on a face sends both of a viewer's coordinates. `at S T` is the point on the face (`llDetectedTouchST`), and the touch's UV (`llDetectedTouchUV`) is worked out from it with the face's texture repeats, offset and rotation, as the viewer does (`LLPickInfo::getSurfaceInfo` fills `mUVCoords` with `LLFace::surfaceToTexture`, `llviewerwindow.cpp:7718`, Firestorm 885631b93a). A face with the default mapping sends UV equal to ST; a planar face, a face under a texture animation and a face the object does not have send ST for UV. A product that reads UV on a prim whose texture is scaled, offset or turned is therefore touched where `S T` says. Measured on 5 October 2026 on a HUD prim whose script moves its own texture as it is touched: a plain touch arrived with the UV the face's mapping gave at that moment, not with UV equal to ST; once the script had moved the texture, a plain touch and a click on the screen at the same point arrived with the same UV, a different one from before. A product that moves its own texture moves the UV of a point with it, so a test reads UV-dependent behaviour from the state it set up. The same mapping is applied to a plain touch and to a drag on the screen ([Where S and T lie](hud-screen.md#where-s-and-t-lie)).

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

The end is `to X Y`, a point in the same pixels, or `by DX DY`, a distance from the start, so that "move it 300 pixels right" needs no pixels at all. A negative number is a move left or up. `settle` waits after the press, up to `Options.HUDChangeTimeout` (5 s), for the region to say the prim pressed has changed, before the cursor moves: a HUD that grows a transparent prim over the screen when pressed to keep the cursor on it needs it, and without it the cursor leaves the prim at once. A HUD that does not change fails the step after that wait. The release is always sent. A start or an end off the world view fails the step, before anything is sent, with `the drag would start at 390,3745, off the 1920x1025 view; nothing was sent` (or `end`): a mouse held down cannot leave the window, and the end is refused rather than clamped ([A drag](hud-screen.md#a-drag)). After the release, with or without `settle`, the step does not finish until the prim pressed has kept one size for a second (at once if it is at its size from the press), which is what a HUD that grows a transparent prim for the drag does a moment after the release, so that a drag right after it, placed from a face, starts where it says; a prim that never changed costs no wait, and one still changing after `Options.HUDChangeTimeout` fails the step with `the HUD was still changing`. That wait is inside the step's time budget.

The pixels are those of a virtual world view, which the run states and not the file: `--screen WxH` (1920x1025 unless said, a 1920x1080 window less Firestorm's menu bar) and `--hud-zoom Z` (1 unless said) ([Package and command](slate-runner.md#package-and-command)). Pixels, and not fractions of the screen, because where a HUD sits depends on the world view's shape ([The world view](hud-screen.md#the-world-view)), so a fraction would not make a test independent of the screen; a stated size of screen is honest, and it is what a screenshot and the measurements are in. The face form is for a test that wants none of it. `OBJ` must be worn on a HUD point; one that is not fails the step with `slate: step N: "<name>" is worn on chest; a drag on the screen needs an object worn on a HUD point, and nothing was sent`, or `is not worn`. A face the prim cannot show, or a shape the runner cannot place (only a plain box or cylinder can be), fails it with a sentence too, and nothing is sent.

**Pay.** `pay OBJ L$5 reason "tip"` pays the object. The amount is an integer of at least 1. Omitting `reason` sends the object's name, as the viewer does. Before paying the runner prints `pay L$<amount> to "<name>" <uuid> reason "<reason>"`. A payment the balance cannot cover is refused before it is sent. A refused or unconfirmed payment fails the step with that message, which says whether the balance moved, and the expectations do not run. A payment is never sent twice.

Both `allow pay` in the file and the `--pay` flag on the process are required. The header without the flag fails at the pay step with `paying is off`. The flag without the header is the static error. A copied script does not pay because the process was started with the flag, and a process started without the flag does not pay because the script asked.

**Permissions.** `allow permission NAME [NAME...] from OBJ` lets a permission request from `OBJ` be granted, and only what is named. `OBJ` is an `object` header, and then a request from any prim of its linkset counts, or an `item` header, and then one from any object the run wore or rezzed from that item, with `wear ITEM ... as X` or `rez ITEM ... as X`, counts (an object a product's script rezzed is not the run's, and is not matched). The object is matched by its id and never by its name, which can be anybody's. Any number of headers may name the same `OBJ`; they add up. The words are exactly `take-controls`, `trigger-animation`, `attach`, `change-links`, `track-camera`, `control-camera`, `teleport` and `override-animations`; `attach` and `take-controls` are granted only when named and are not implied by anything. A request is answered once: the bits asked for and named are granted, the rest are refused in that same answer, and a request with nothing named is denied as before. `debit` cannot be named, because it spends L$ and paying stays behind `allow pay`, `--pay` and the profile's rules; a request that asks for it alongside named bits is granted the named bits without it. Not nameable, and always refused: silent estate management, return objects and privileged land access, which act on land and estates and not on what a product test needs; experience permissions, which reach beyond one script; and the bits the grid does not implement.

**Wait.** `wait D` sends nothing for `D`, and listens: it is for a product that ignores a touch coming too soon after the last one, a script that has to finish something first, or a run that is only to see what a product says. It is a blocking stimulus, so `D` must fit the step's budget, and expectations in its step are armed when it returns ([Arm point](#steps-and-timing)): `wait 4s` then `expect colour t face 2 is 1 1 1` reads the colour after the four seconds, and passes only if it is white then, not if it was white when the wait began. A wait says what it is for where `expect no ... within D` would read as a check.

*What a wait shows.* Every line the run hears is printed with the time it arrived, as it arrives, whether or not anything expects it, and so it is in a wait as in any other step. A wait needs no expectation, no speaker and no channel for that, and there is no `listen D` to write: `wait D` is the one word. What is printed:

| Heard | Printed as |
|---|---|
| Chat the simulator delivers: whisper, say and shout (all three are `public`), the debug channel, region chat, owner chat and direct chat, each as `chat <type> from <who>: "<text>"` | `chat public from hud: "..."`, `chat owner from hud: "..."`, `chat debug from hud: "..."`, `chat region from ...` |
| A line on a channel a `listen` header gave the bridge, which the viewer does not deliver | `chat channel N from <who>: "<raw tail>"` |
| A script dialog or text box, to the tester or to a second avatar | `dialog from hud: "..." buttons ...`, `textbox from ...` |
| A give offer | the line the `give` expectation prints for it |
| A permission request, which the runner answers as it does in any step | `permission denied from hud: ...`, or the grant |
| A link message a probe heard | `probe link ...` |

*Who* is the script's name for an object the file binds, a second avatar's binding name, the tester's name for the tester, and for anybody else the displayed name, with an object's marked `[Object]` so that no object's name can pass for a binding. A line of the bridge's own protocol (its ready, the hellos, the probes' reports) is not chat and is never printed as one. Instant messages that are not give offers are logged and not printed, in a wait and in any other step.

A wait shows at least what `expect no say matching "^$" on owner from object X within D` has heard, which was the way to see a product talk before: that expectation matches one speaker on one channel, and prints only the lines it matches, where a wait prints every line of every speaker on every channel, region chat, which no `say` expectation matches, included.

*Printed as it is heard.* A wait takes in what arrives while it lasts and prints it then, not when it ends, and a line arriving after the wait is printed by the step that follows. (Before this was so, the lines of a wait were stamped with their arrival and printed when it ended, and a product that said more than the chat subscription's 256 lines in a long wait stopped the run with `the chat subscription dropped N lines`.) Lines of one kind are printed in the order they arrived; chat comes before the dialogs and permission requests that arrived in the same moment.

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

**Wear.** `wear ITEM on "POINT" as NAME` puts an inventory item on the tester. `ITEM` is an `item` header ([Objects](#objects-names-and-link-numbers)), and `POINT` is an attachment point by name, such as `"HUD Top Left"` or `"Chest"`: the whole name, in either case. A name that only begins several points' names, such as `"HUD centre"`, is refused at check time with the points it could be (`"HUD centre" is HUD centre 1 or HUD centre 2; say which`), and one that begins only one is refused naming it. The item is added to the point and does not replace what is there. It is a blocking stimulus under the stimulus budget.

- An item that is already worn fails the step before anything is sent: `slate: step N: "Example Hat" is already worn on HUD top; take it off first`. A run that was killed can leave the product worn, so the next run's `wear` fails with this sentence.
- `NAME` is bound to the worn root when the stimulus returns, with its children as its linkset. It is usable by the same step's expectations. That is the one exception to the rule that a name bound in a step is usable only from the next step.
- The step arms before the request. `Wear` returns after about 0.2 s, and the product's own reaction to being worn, its `on_rez` and `attach()`, arrives about 2 s after `Wear` returns (measured, [Eighth round](slate-runner.md#eighth-round)). Give the step a `within` that covers it: the default 10 s does, and `within 1s` does not.
- The runner does not take off what a test wore. Put the `take off` in `after each`, since the test may have failed before its own.

**Rez.** `rez ITEM (at X Y Z | by DX DY DZ) as NAME` rezzes an inventory item in the region, so that what a product does when it is rezzed (a rename text box, a prompt to a controller) can be tested, and so can a test that needs a fresh copy. `ITEM` is an `item` header, as for `wear`. `at` is a region position; `by` is an offset from the tester's own position, read as a `say` reads it ([Tester position](slate-runner.md#tester-position)); a tester whose position is not exact fails the step with `slate: step N: tester position is not exact; the rez was not sent`. Exactly one of the two is written. It is a blocking stimulus under the stimulus budget, and is done when the region has described the object. The tester's active group is sent as the object's group, because a parcel usually lets a group build and refuses the avatar who has no group.

- **The word.** A step that starts with `rez` is this stimulus. `expect rez name "..." from OBJ` is the expectation for an object the product rezzed, and is read only after `expect`; a name may still be `rez`.
- **The name.** `NAME` is bound to the rezzed root when the stimulus returns, with its children as its linkset, and is usable like a header object (`touch`, expectations, `link N`) and by the same step's expectations, as `wear`'s is. It is scoped as `wear`'s is: bound in `before each` it is made again for each test and seen by the test and `after each`; bound in a test body it is not seen by `after each`, with one exception: `delete NAME` in `after each` may name it, so that the object is deleted whatever the outcome ([Delete](#stimuli)).
- **Deleted afterwards.** Whatever the outcome, at the end of each test, after `after each`, the runner deletes every object a `rez` step made in that test, to the tester's Trash, and prints `slate: deleted NAME ("<item>") to the Trash` for each. It deletes nothing else: an object a product rezzed is deleted only by a `delete` step ([Delete](#stimuli)), and an object a `delete` step took is not deleted a second time here. A delete that fails prints `slate: delete failed: NAME ("<item>"): <error>`, fails the test (exit 1, `slate: fail test "<test>": could not delete NAME` when nothing else had failed it), and the object is left where it is. An object a rez step made and then failed to hand back is deleted too, when the session knows it.
- **A refusal is said at once.** Where the tester may not build, the grid makes no object and sends an alert about 1.2 s after the request, of the shape `Can't rez object '<item>' at { <x>, <y>, <z> } on parcel '<parcel>' in region <region> because the owner of this land does not allow it.  Use the land tool to see land ownership.` (measured on 2026-10-03, rezzing an invented box as the test avatar with no active group on a parcel that lets a group build). `Session.RezFromInventory` recognises an alert of that shape ("Can't rez object" ... "because") heard after its request and ends at once with an error quoting it; before that it waited out its whole timeout, 15 s in the measurement. The step fails with that sentence as its stimulus error, nothing was made, and so nothing is deleted. The transcript prints the alert as the grid sent it.
- **How long it takes.** Measured on the same date, a rez the grid allowed (a parcel that lets the avatar's group build, the avatar acting as that group) returned after about 2.1 s, which is `RezFromInventory`'s one-second poll finding the object described. The stimulus budget's default of 10 s is enough.

**Take off.** `take off NAME` takes off the item `NAME` is worn from, which is a name `wear` bound or an object the tester is wearing. A binding that is not worn fails the step with `NAME is not worn`. It is a blocking stimulus, and it completes when the store no longer lists the worn root, about 0.1 s after the request (measured, [Eighth round](slate-runner.md#eighth-round)), or fails with `"<item>" is still in the store <budget> after the take off`. `NAME` is not usable after this step, and this step's own expectations, such as `expect attached NAME off`, are what it is for.

**Delete.** `delete NAME` sends an object to the tester's Trash, so that a test can clean up what a product rezzed, which the runner does not do by itself. `NAME` is a name a rez expectation bound with `as` (`expect rez name "..." from OBJ as NAME`) or a `rez` step bound; anything else is a static error with a sentence ([References and scope](#static-checks)). It is a blocking stimulus under the stimulus budget, and it completes when the store no longer lists the object, read at the object cadence, or fails with `"<name>" is still in the store <budget> after the delete`; any other failure is the error `sl` returned. It prints `slate: deleted NAME ("<name>") to the Trash`, with the object's name as the region gave it in the object's properties (the updates carry none). **Measured** on 2026-10-06 with the test avatar: a worn HUD's button rezzed an object from the HUD's contents, owned by the tester; a test claimed it with `expect rez ... as`, and `delete` in `after each` took it to the tester's Trash, where it then was, and out of the region; the other tests of the file printed the "was not bound" line and passed.

- **Only the tester's own.** The owner is what the region said in the `ObjectUpdate` that first described the root when it carried one, and otherwise what `ObjectProperties` says, asked for at the step when `delete` is reached, before anything is sent. A `rez` step's object is the tester's by construction. An object whose owner is not the tester, a group's too, fails the step before anything is sent: `slate: step N: NAME ("<name>") is not the tester's; it was not deleted` (the owner's id is not printed: it is another resident's, and a transcript is pasted into notes and logs), and one whose owner cannot be read fails it with `slate: step N: NAME ("<name>") was not deleted: the owner of NAME is not known (...)`. Nothing is deleted without knowing whose it is. It goes to the tester's Trash and nowhere else.
- **Already gone is not a failure.** A product that deletes or `llDie()`s its object, or a temp-on-rez object that expired, is not in the store when `delete` is reached: the step passes and prints `slate: NAME ("<name>") is already gone; nothing deleted`.
- **In `after each`.** The scope rule says a name bound in a test body is not seen by `after each`, because the test may have failed before binding it. `delete` is the one exception, since `after each` is where a test author puts it to run whatever the outcome. The check needs the name to be bound by a rez expectation or a `rez` step in at least one test of the file. At run time, if the test that just ran did not bind it (it failed before the expectation was met, or only another test binds it), the step passes and prints `slate: NAME was not bound in this test; nothing deleted`. No other stimulus or expectation sees such a name.
- **A `rez` step's object.** Deleting it with `delete` is allowed, and the object is then not deleted again when the test ends: the end-of-test deletion of [Rez](#stimuli) is unchanged for the objects a `rez` step made and that no `delete` took, and a `delete` that did not complete leaves it for that deletion.
- **Nothing is deleted automatically for a rez expectation.** The runner does not delete what a product rezzed unless the test says so. A runner that cleaned up whatever a product made would hide a product that forgot to clean up after itself, or one that is meant to leave its object in the world (a rezzer whose object is supposed to stay), and deleting only what a test names is the safer default in somebody else's region. A test that wants a clean region writes `delete NAME` for each claimed object, in `after each` when it must happen whatever the outcome.
- **After the step.** `NAME` is still a name, for a second `delete` (which says the object is already gone) and for `after each`, but it names an object that is no longer there.

**Drop into.** `drop ITEM into OBJ [link N]` puts a copy of an inventory item into a prim's contents, as a viewer does when an item is dragged onto an object. `ITEM` is an `item` header, as for `wear` and `rez`. It sends what `Session.PutInObject` sends: one `UpdateTaskInventory` for the prim (key 0, which selects the object's own contents) -- or, for a script, one `RezScript` with `Enabled` set, as the viewer drops a script, so that the script is asked to run in the prim ([Dropping a script into an object](scripts.md#dropping-a-script-into-an-object)); the root prim of a linkset is the one it names -- carrying the item as the viewer builds it: the prim as its folder, its masks, name, type, creation date and the checksum the simulator expects. Nothing answers it. The step is blocking, under the stimulus budget: it reads the prim's contents until the copy shows, and fails with `"<item>" did not show in the contents of OBJ within <budget> of the drop; the prim may not take it, or something took it out again` when it does not. A product sees `changed()` with `CHANGED_INVENTORY`. The copy is the entry that was not in the contents before, of the item's type and, where the contents show an asset, of its asset, whose name is the item's or, when the prim already held one of that name, the name, a space and a number (a prim renames a newcomer: measured for a script put in twice, inferred for other kinds, [Names](names.md#measured); the runner accepts either). Nothing else is taken for it: an item a product puts in as the drop arrives, named like the drop with more after it, is the product's. `drop` is the tester's alone; a non-owner's drop into an object that has `llAllowInventoryDrop` on is a later extension ([Known limitations](#known-limitations)).

- **A no-copy item is refused.** A viewer's drag of an item the avatar may not copy moves it: it leaves the tester's inventory for the object, and removing it from the object afterwards deletes it. The run could not give it back, so the step fails before anything is sent: `slate: step N: "<item>" may not be copied, so dropping it would move it out of the tester's inventory and the run could not give it back; the drop was not sent`. (Allowing it would leave the tester without the item after every run; a drop of a no-copy item is a test for a person to set up by hand.)
- **Removed afterwards.** Whatever the outcome, at the end of each test, after `after each`, the runner removes from the prim every copy a `drop` in that test put there, newest first, and prints `slate: removed "<item>" from OBJ` for each (the name the prim gave the copy, and the binding, with its link when one was named). It finds the copy by its id, which it noted when the copy showed, and reads the contents until it is gone. It removes nothing else: what the prim held before, and what the product put in or took out meanwhile, stays. A removal that fails prints `slate: remove failed: "<item>" from OBJ: <error>`, fails the test (exit 1, `slate: fail test "<test>": could not put back OBJ` when nothing else had failed it), and the copy is left where it is. A copy that shows only after its step gave up is looked for once more at the end of the test, by the same rule, and removed the same way; if it is still not there the runner prints `slate: the copy of "<item>" never showed in OBJ; nothing removed`. The removal is itself a change of the contents, which a product may react to as it did to the drop.

**Drop onto.** `drop ITEM onto OBJ [link N] face N` puts a texture on one face, as a viewer does when a texture is dragged onto a face. `ITEM` must be a texture, else the step fails before anything is sent: `slate: step N: "<item>" is not a texture; drop onto a face needs one, and the drop was not sent`. What it does follows the viewer (`LLToolDragAndDrop::dropTextureOneFace`, `handleDropMaterialProtections`), in this order:

| The prim's contents and the item | The step does |
|---|---|
| The prim already holds an item of that asset | Sets the face. Nothing goes in. |
| The tester may copy it and transfer it | Sets the face. Nothing goes in. |
| The tester may copy it and not transfer it | Puts a copy into the prim as `drop ... into` does, then sets the face. |
| The tester may not copy it | Fails: `"<item>" may not be copied, so putting it into OBJ would move it out of the tester's inventory and the run could not give it back; the face was not changed`. |

The face is set with `Session.SetFace`, which reads the prim's faces and sends them all back with the texture of that one changed (`ObjectImage`), so the face keeps its colour, glow, tiling and the rest. A face the prim does not have fails the step (`OBJ has 6 faces, so there is no face 7`). It is a blocking stimulus. The stimulus line says what happened (`dropped "<item>" onto OBJ face N; nothing went into the prim`, `...; a copy went into the prim first`, `...; the prim already held it`).

- **Put back afterwards.** At the end of each test, after `after each`, the runner sets the face back to the texture it had before the drop, and prints `slate: restored OBJ face N to texture <uuid>`, then removes a copy the drop put into the prim, printing `slate: removed ...` as above. A face that already had the texture is not set again. Both are newest first across the test's drops. A failure to set the face back prints `slate: restore failed: OBJ face N: <error>` and fails the test as a failed removal does. Setting the texture back is itself a texture change: a product that reacts to its face's texture changing reacts to the restoration as it did to the drop, after `after each` has run, and the test should not be written to watch for the absence of that.

**Group.** `group NAME "Group Name"` sets the active group of the second avatar `NAME` ([A second avatar](#a-second-avatar)) and `group NAME none` sets it to no group. A product's group mode admits an avatar only while it wears the group's tag, and this is how a test puts the second avatar in or out of it. The group is found in the avatar's own list of the groups it has joined, by name and ignoring case, as slsh's `group` finds one; the step is done when the avatar's active group is that one (`ActivateGroup` waits for the simulator to say so, within the stimulus budget). If the group is already the active one nothing is sent. The tester's group is not a target: `group tester ...` is a static error, because the group the tester acts as decides where it may build and a test does not change it.

A group the avatar has not joined fails the step with a sentence that names the binding and not the avatar or its profile: `visitor has not joined a group called "Example Club"`. A list that has not arrived says `visitor has no groups that are known yet, so "..." matches none; the list arrives on its own after login`, and two groups of one name `visitor is in 2 groups called "..." , and a step cannot say which`.

- **Put back at the end of the run.** At setup, before anything runs, the runner reads the active group of each second avatar that a `group` step names. When the run ends, after the last test and before the verdict, each of those whose group a step changed is set back to the group it had, and the runner prints `slate: cleanup: put the group of NAME back to what it was`. This is the end of the run and not of each test, because a test that changes a group is followed by one that may rely on it, and a lent avatar is only to be left as found. A failure to put it back is a cleanup warning, `slate: cleanup: warning: the group of NAME was not put back: <error>`, as the probe's is: it changes neither the exit code nor an earlier failure, and the avatar is left in the group the run gave it. The transcript never prints the avatar's name, only its binding.

## The tester is left as found

At the end of each test, after `after each` and the put-backs, the runner leaves the tester's waiting list as the run found it, for what arrived during the run:

- An **inventory offer an object made** to the tester that is still waiting is **declined**, as the viewer's Decline button does, and printed as `slate: declined offer "Example Thank You" from vendor`. The decline is the instant message the offer's own dialog plus two (`IM_TASK_INVENTORY_DECLINED`), quoting the offer's transaction id, sent to the giver on the tester's session; a give of a folder is declined the same way.
- An **inventory offer from a person** is **left waiting**: the tester is somebody's avatar, and a friend's give during a run is theirs to answer, not the run's. It is printed as `slate: left offer "Example Thank You" from Kerra Yule waiting; a person's offer is not the run's to answer`, and nothing is sent to the person. The giver is the binding that names the object, else its name marked `[Object]`. An offer the tester has already stopped holding costs nothing. An offer an `expect give` accepted is not waiting any more and is not declined, and the item it delivered stays in inventory: removing it is not done.
- A **script dialog or text box** to the tester that is still waiting, whether or not a step matched it, is **ignored** and printed as `slate: left dialog from vendor "Pick one" unanswered` (`textbox` for a text box). The viewer's Ignore button sends nothing, so nothing is sent to the grid and the script is not told; what the runner does is tell slgod that the dialog has been dealt with (`ignored`), so slgod stops listing it to the next program that attaches. Without that, slgod keeps an unanswered dialog for an hour, 32 of them at most, and `slsh waiting` shows each as from before the shell. (`slsh no` and `slsh ignore` on a dialog only drop it from that shell's list.)

Only what arrived after the run began is touched; an offer or a dialog that was already waiting belongs to nobody here and is left. A decline or an ignore that fails is printed (`slate: the offer "..." from ... could not be declined: <error>`) and does not fail the test. A second avatar's offers are declined the moment they are seen and its dialogs are left to expire, as [A second avatar](#a-second-avatar) says.

## A second avatar

A product with a public, a group and a private mode needs a second avatar's touch, chat and dialog answers in the same test. A file declares one with a header, and the run is told who it is:

```slate
slate 1
object sign is "Example Sign"
avatar visitor

touch sign anywhere as visitor
expect dialog from sign to visitor text "Pick one" button "Red" within 5s
choose "Red" on sign as visitor
expect say "red" on public from object sign
```

`avatar NAME` holds no in-world name. It is a binding for an avatar the runner is **given**, on the command line, with `--avatar NAME=PROFILE`: the slgod profile that drives it ([the command line](slate-runner.md#second-avatars)). Any number may be declared and each must be given. The runner dials a session for each, as it dials the tester's, so the second avatar acts on its own session.

**Explicit only.** The runner uses an avatar for a second one only because a flag named it, for that binding. It never picks one from what slgod holds, never offers one, and never falls back to one when a flag is missing. The avatars a daemon holds for others are lent: they are the owner's to allow, one at a time, by giving the flag. A declared avatar with no flag, a flag for an avatar the file does not declare, a profile that is the tester's own, and a profile the daemon does not hold are each a setup error, exit 3, and the sentence names the binding and never the profile:

```text
slate: setup: visitor is declared but no --avatar visitor=... was given
slate: setup: an --avatar was given for visitor, which the file does not declare
slate: setup: the profile given for visitor is the tester's own
slate: setup: the profile given for visitor is not held by the daemon (dial: ...)
```

**What it can do.** A second avatar touches (`touch OBJ ... as NAME`, with any target the tester's touch has), drags on a face (`drag OBJ face ... as NAME`; `drag ... on screen` is about the tester's own HUD and has no `as`), says (`say "..." on N as NAME`), and answers the dialogs that came to it (`choose ... on OBJ as NAME`, `answer "..." on OBJ as NAME`). Its active group is set by `group NAME "Group Name"` or `group NAME none` ([Stimuli](#stimuli)), the one stimulus that names an avatar and not an object. It does nothing else: `pay`, `sit`, `stand`, `wear`, `take off`, `delete`, `rez`, `send` and `drag ... on screen` are the tester's, and `as NAME` on them is a static error. It never pays. The second avatar must be in the same region as the tester and near what it touches; the runner does not move it.

**What reaches it.** `expect dialog from OBJ to NAME ...`, `expect textbox from OBJ to NAME ...` and `expect give TEXT from OBJ to NAME` are what an object sent to the second avatar; without `to` they are the tester's, as before. What the second avatar says is heard by the tester, and is matched by `expect say ... from avatar NAME`, by the second avatar's id and not by its name. A dialog is held for the object and for whom it came to, so `choose "A" on sign` answers the tester's dialog from the sign and `choose "A" on sign as visitor` the second avatar's, and neither replaces the other. Both go to the same event log, with the same arming and consumption rules as any other event.

**Left as found.** The second avatar must come out of a run as it went in. An inventory offer to it is **declined** the moment the runner sees it, after the log has it for an `expect give ... to NAME`, and never accepted, so its inventory does not change. A permission request to it is **refused**, whatever `allow permission` names: `allow permission` covers the tester's requests only. A dialog or text box to it that no step answered is left to expire; the tester's are ignored at the end of each test ([The tester is left as found](#the-tester-is-left-as-found)). A group a `group` step set is put back at the end of the run, to the one it had when the run began.

**Transcript.** An event for the second avatar is labelled with its binding: `dialog to visitor from sign: "Pick one" buttons "Red"`, `textbox to visitor from sign: ...`, `give to visitor from sign: "..."`, `give to visitor declined, transaction ...`, `permission denied to visitor from sign: ...`, and a chat line it spoke as `chat public from visitor: "..."`. Neither its displayed name nor its profile appears in the output, in the failure block, or in an error.

## Expectations

**Text and patterns.** Wherever an expectation compares product text, the author writes either a string, which is exact and case-sensitive, or `matching "RE"`, which is a pattern.

| Where | Exact | Pattern |
|---|---|---|
| say text | `expect say "red" on public ...` | `expect say matching "^Thanks, .+!$" on public ...` |
| dialog message | `expect dialog from v text "Choose"` | `expect dialog from v text matching "^Choose"` |
| dialog button | `expect dialog from v button "Red"` | `expect dialog from v button matching "^Re"` |
| text box message | `expect textbox from b text "Name?"` | `expect textbox from b text matching "name"` |
| give item name | `expect give "Example Red Swatch" from v` | `expect give matching "Swatch$" from v` |
| give folder name | `expect give folder "Example Starter Folder" from v` | `expect give folder matching "^Example Starter" from v` |
| give folder, an item it holds | `expect give folder "F" from v holding "Example Red Swatch"` | `expect give folder "F" from v holding matching "Swatch$"` |
| rez name, description | `rez name "B" description "left" ...` | `rez name matching "^B" description matching "left" ...` |
| link text | `... text "go"` | `... text matching "^go"` |

A pattern is Go RE2 syntax and is unanchored, as a button `pattern` is; write `^` and `$` to anchor it, and `(?i)` to ignore case. It is compiled at static check, and one that does not compile is exit 2. In a Slate string a backslash is doubled, so the pattern `L\$[0-9]+` is written `"L\\$[0-9]+"`. Wherever the Exact column takes a string, a capture that holds text takes its place and is compared as that string would be ([Captures](#expectations)); a capture is never a pattern. The `choose` forms are under [Stimuli](#stimuli). `answer` and the text of the stimulus `say` take strings only, and the text of `send` takes a string or a capture. With `give matching`, two new items whose names match are ambiguous, as two new items of one name are, and so are two new folders with `give folder matching`. A pattern after `holding` binds nothing: a named group in it is refused at static check.

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

A text box does not match `expect dialog`. The matching dialog is held for the binding ([Dialog hold](#steps-and-timing)), so `choose` and `answer` on `OBJ` use the hold of any dialog matched for `OBJ`. The author never writes the dialog's channel, and does not expect the reply `choose` sends; the product's next chat line, give or texture change is the expectation. A dialog offered to another avatar is invisible, unless the run is given that avatar and the expectation says `to NAME`. There is no decline, so an unanswered dialog is left to expire. When no dialog meets the clauses, the `unmatched` line says why the last dialog from that object with that message did not ([Reading the result](#reading-the-result)).

**Text box.** The same as a dialog, for a text box, with a message that is a string or a pattern, no button list, and the same linkset and `link N` rules. Answer it with `answer`, not `choose`.

**State expectations.** Texture, offset, repeats, rotation, position, size, turn, light, projector, click, substance, text, fullbright, glow, colour, alpha, alphamode, normalmap, specularmap, glossiness, environment and gltf are readings of state. Each takes one of three forms, and a value may be written `original`, as a capture (`$x`), or, after `is` only and with `as $x`, as `any`:

| Form | Passes when |
|---|---|
| `is X` | A reading equals X, including a reading that already did at the arm point (after a `wait`, the end of the wait). It is a plain reading and cannot by itself prove that the stimulus changed anything. `is any as $x` passes on the first reading, whatever it is, and binds it. |
| `becomes X` | A reading equals X and an earlier reading in the same step's window, the baseline counted, did not. If the baseline already equals X, the value must leave X and come back. |
| `changes` | A reading differs from the baseline. It takes no value. |

`expect texture sign face 0 becomes 47227e57-7e57-c0de-1611-3fe69d620af1` is the way to show that a stimulus changed a texture. `link N` after the binding (`expect texture sign link 3 face 0 ...`, `expect click chair link 2 is touch`, `expect substance box link 2 is wood`) selects that prim of the binding's linkset, found at each poll ([Objects](#objects-names-and-link-numbers)). Without `link`, the expectation reads the bound prim itself.

**Baseline.** A step's baseline for an expectation (taken again, afresh, when a `wait` in its step returns) is the latest reading of that face (or click byte) observed at or before the step's arm point, taken from the event log. The runner reads every prim and face that any state expectation in the test names, from the start of the test. If no reading exists at the arm point, the first reading after it is the baseline, and the transcript says so with a line `baseline for <object> face <n> taken after the arm point`.

**Tolerances.** `changes` and `becomes` compare as `is` does: offset within 2/32767, repeats within 1e-4, rotation within 2/32768 of a turn, position and size within 0.001 m on each axis, turn within 0.007 degrees of angle, glow, colour and alpha within 1/255, a light's colour and intensity within 1/255, a light's radius and falloff and a projector's field of view, focus and ambiance as the float32 they are, a GLTF override's colours, factors, texture repeats and offsets within a millionth, a GLTF texture rotation within ten millionths of a turn, and texture, click, substance, fullbright, alphamode, normalmap, specularmap, glossiness, environment, whether a light is on and a projector's texture exactly. A reading that differs from the baseline by less than the tolerance is not a change.

**Near.** A file can widen the tolerance of one expectation with `near`, written after the value and before `within`:

```slate
expect position hud becomes original near 0.005 within 10s
expect size hud link 3 is 0.8 0.4 0.016 near 2 percent within 5s
expect position hud changes near 0.005 within 5s
expect no alpha hud face 0 changes near 0.1 within 2s
```

`near N` is an amount in the reading's own unit: metres for position and size, the same fraction of a face for offset and repeats, a fraction of a turn for rotation, degrees of angle for turn, and 0 to 1 for glow, colour and alpha, and a step of the 0 to 255 level for glossiness and environment, and for a light's colour and intensity, and a GLTF override's colours and factors, and the repeats and offset of a GLTF texture slot (the same numbers a face's are) and its rotation in turns, and the number's own unit for a light's radius and falloff (metres for the radius) and a projector's field of view (radians), focus and ambiance. `near N percent` is a share of the wanted value, of each component by itself, and is refused on a turn, and on a rotation: a share of an angle depends on which of its equal spellings it is taken from (1 percent of 0.9972 is 0.01 turns, 1 percent of -0.0028 almost nothing), so a rotation takes `near N` in turns only. For a size, `size ... is 0.8 0.4 0.016 near 2 percent` allows 0.016, 0.008 and 0.00032. A resize that comes back about 1 percent off in each side is that share at any size, where an amount in metres says it for one size only. The word is `near` because `within` is time; it is one word, and `percent` is a word beside the number because `%` is not a character a file has. A share is of the value the reading is compared with, so it is no help for a component at or near 0: a percentage of 0 is nothing, and the component is compared with its quantisation. Use an amount for a position.

- **Where it applies.** `is`, `becomes`, `changes`, the negative forms of each, and `original` or a capture as the value. With `changes`, a reading is a change when it is further from the baseline than the tolerance, so `changes near 0.005` says that a HUD moved by more than 5 mm. With `becomes`, the value must be left and reached again as before, both judged by the tolerance, so `becomes original near 0.005` checks that a HUD dragged away and back is back. With `face all`, each face is compared by itself.
- **The norm.** Each component is compared by itself: every one has to be within the tolerance, and they are not combined. A position 4 mm out on each of three axes is within `near 0.005`. A turn is the exception: it is one angle, the angle of the rotation between the reading and the wanted value, and `near 1` allows one degree of it, however the error is spread over the three axes.
- **Never tighter than the reading.** The tolerance of the kind, in the paragraph above, is the least there is, since it is the quantisation of what the grid reports. A `near` below it is not an error: the expectation is judged by the kind's own tolerance, and its transcript line says so, `(tolerance 0.001, raised from 0.0001: the least position is read to)`.
- **Static refusal.** Only the numbers take `near`. A texture, a normal or specular map, a click, a `substance`, `fullbright`, a text and an alphamode are exact, so `near` on them, and on `say`, `dialog`, `textbox`, `give`, `rez`, `link`, `attached`, `animation` and `button` (a count is whole), is refused ([Static checks](#static-checks)). So is a `near` of 0 or less, of more than 100 percent, and beside `is any`.
- **In the transcript.** The line of an expectation that has `near` ends with the tolerance used, `(tolerance 0.005)` or `(tolerance 2 percent of each wanted component, and at least 0.001)`, in the failure block and in the line printed when it matches, which also says how far off the matching reading was: `matched position hud becomes original near 0.005 within 10s (tolerance 0.005): position hud 0.503 0.25 0 (0.003 off, at most 0.005 allowed)`. An unmatched one says how far the nearest reading was, `; the nearest reading was 0.006 off, and at most 0.005 is allowed: position hud 0.506 0.25 0`, and for `changes`, `; no reading was more than 0.004 from the baseline, and a change needs more than 0.005: the furthest was 0.004: position hud 1.004 1 1`.

**Original.** `original` means the reading of that face or click byte when the test's own steps begin, after `before each` has run, so a reset in `before each` is what `original` refers to. It is meant for toggles: touch once and the value `changes`; touch again and it `becomes original`. If no reading exists at that point, `original` is the first reading after it. `original` is legal wherever a value is: a texture, the two numbers of offset or repeats, the one number of rotation, glow or alpha, the three of a colour, a position or a size, `on` or `off`, a click name, a substance name. With `face all` it is the tuple the test began with.

**Texture.** Face N of the bound prim shows the texture UUID. A reading can lag a script's change by several seconds, so the runner asks the region again about once a second. A reading that still shows the old texture does not fail the step early; only the deadline does. A planar face or a face with a running texture animation does not stop a texture comparison.

**Offset, repeats, rotation.** Offset matches within 2/32767 of each literal, since the stored value is quantised to about one part in 32767. Repeats match within 1e-4 each; negative repeats are a flip and are legal. Rotation is a fraction of a turn, not degrees or radians: `0.25` is a quarter turn and `0.5` a half turn, matched within 2/32768 of a turn. The comparison goes the short way round: the difference of two rotations is taken modulo one turn into [-0.5, 0.5) and its absolute value is held against the tolerance, in `is`, `becomes`, `changes`, the negative forms, `original`, a capture, each face of `face all` and `near`, and in the figure a failure gives for how far off a reading was. The region keeps a face's rotation as it was set, in either sign and past half a turn, so one angle has readings a whole turn apart. Measured on a live region, a face set with `slsh texture -f 5 --rot D` and read with `expect rotation OBJ face 5 is any as $r`:

| set (degrees) | read (turns) |
|---|---|
| 359 | 0.997222900390625 |
| -1 | -0.002777099609375 |
| 1 | 0.002777099609375 |
| 0 | 0 |

So 359 and -1 degrees are the same angle, `1` and `-1` are two degrees (0.0056 of a turn) apart, and a `changes` from 359 to -1 degrees is no change. The transcript prints the reading as the region sent it, not wrapped. A rotation literal is still checked against [-1, 1].

**Position and size.** `expect position OBJ [link N] (is X Y Z | becomes X Y Z | changes)` and `expect size OBJ [link N] (is X Y Z | becomes X Y Z | changes)` read the prim's position and its scale, in metres, as the object store holds them. They take no `face`, a value is three numbers, and a value may be `original`, a capture or, after `is` with `as`, `any`, as every state expectation does. What a position is depends on the prim, because the store keeps the position of the last update as the region sent it and does not compose it:

| The prim | `position` is |
|---|---|
| A worn root | Its offset from the attach point, so a HUD that has not been moved reads `0 0 0` |
| A child (`link N`, N of 2 or more) | Its position relative to its root, in the root's frame |
| A rezzed root | Its position in the region |

This is the `Position` of the prim's `sl.Seen`, and `Seen.Scale` for `size`; a poll reads both with no request of its own. Both compare within 0.001 m on each axis. The region reports what a script set exactly, so the tolerance is for the author, who writes rounded numbers. A millimetre is about one pixel of the default 1025-pixel world view at HUD zoom 1, since the view is one metre tall ([Where a worn HUD is on the screen](hud-screen.md#the-world-view)). Measurements are under [Position and size](slate-runner.md#position-and-size) in the runner document. The reading is a position in the HUD's own frame, not a place on the screen: `at X Y` on the screen is not built ([Known limitations](#known-limitations)).

**Turn.** `expect turn OBJ [link N] (is X Y Z | becomes X Y Z | changes)` reads the prim's own rotation, which `rotation` is not: `rotation` is a face's texture rotation, and `turn` is how the prim itself is turned. The value is Euler degrees X Y Z, the three numbers the build tool shows for the prim, and the ones a script sets with `llEuler2Rot(<x, y, z> * DEG_TO_RAD)`. It takes no `face`, and a value may be `original`, a capture or, after `is` with `as`, `any`, as every state expectation does. What a rotation is depends on the prim, as a position does: a rezzed root's is in the region's frame, a child's (`link N`, N of 2 or more) is relative to its root, as the region reports it and without the root's turn composed into it, and a worn root's is relative to its attachment point. Those are read from the code and the protocol, as the child's position is, and not measured here ([Turn](slate-runner.md#turn)).

The comparison is of rotations and not of the three numbers, because two triples can be one rotation: `180 0 0` and `0 180 180` are the same turn, and either one matches a prim set to the other. The reading and the wanted value are compared by the angle of the rotation that takes one to the other, in degrees. That angle is within 0.007 degrees, which is twice the most the region's quantisation of a rotation can lose, and `near N` widens it to N degrees: `expect turn lid link 2 becomes 0 45 0 near 0.5` allows half a degree of angle, however it is spread over the axes. A turn takes `near` in degrees and never `percent`. The transcript line is `turn <name> X Y Z`, the Euler degrees of the reading rounded to a thousandth of a degree, and `as $x` binds the numbers as that line prints them, which another `turn` can use. A capture rounded that way is within the default tolerance of the rotation it came from.

**Light and projector.** A prim's point light and its projector are two of its extra parameters, and `light` and `projector` read them from the object store, as `turn` reads the rotation, with no request of their own.

```text
expect light OBJ [link N] (is on|off | becomes on|off | changes)
expect light OBJ [link N] colour (is R G B | becomes R G B | changes)
expect light OBJ [link N] intensity|radius|falloff (is N | becomes N | changes)
expect projector OBJ [link N] (is TEXTURE|off | becomes TEXTURE|off | changes)
expect projector OBJ [link N] fov|focus|ambiance (is N | becomes N | changes)
```

Each is one reading, so each takes `original`, a capture, `any` after `is` with `as`, `near` and `expect no`, as every state expectation does, and each has its own transcript line and its own capture type. They are separate words and not one expectation with several values (`is on colour 1 0 0 radius 5`) because a reading here is one value that is compared, remembered as a baseline and bound as a capture, and a combination would have to say which part of it `changes` or `becomes` is about. A test that wants the whole lamp writes a line for each part; the cost is that a state of two parts is two readings.

A light is on while its block is in the prim's updates and off when an update has none: an update with no block is how the viewer learns a light is off, so that is what is read, and a block with zero intensity is a light that is on (read from the viewer, [Lights](lights.md#the-light); what the region sends when a script switches a light off was not seen). A projector is off when its block is not there, and a block with the null texture is off as well. The numbers of a light that is off, or of a prim with no projector, have no reading: nothing says what they are, so `expect light OBJ radius is 5` waits, and fails with no reading shown, until the light is on, and `expect no light OBJ radius ...` cannot pass on a prim that has none. A test that means "the light went out" says `light OBJ becomes off`.

A colour is the three bytes the region sends over 255, which is the linear colour the viewer feeds its shaders, written as the 0 to 1 a script gives `PRIM_POINT_LIGHT`, and intensity is the fourth byte over 255. Both are compared within 1/255, one step of the byte. Radius, falloff, field of view, focus and ambiance are float32 on the wire and read whole: a literal is made a float32 as written and compared exactly, so `near` is for a number a script worked out. The field of view is in radians. That `PRIM_POINT_LIGHT`'s colour is the linear value, and that the region sends what a script set without changing it, are what the viewer and the protocol say and were not measured here ([Lights](lights.md#the-light)); `expect light OBJ colour` against a script's own number is the test of that, and a colour that comes back off by a curve says the region converts. The cutoff of a light is kept by the store and not read here: `PRIM_POINT_LIGHT` has no cutoff.

The transcript lines are `light <name> on|off`, `light colour <name> R G B`, `light intensity|radius|falloff <name> N`, `projector <name> TEXTURE|off`, `projector fov|focus|ambiance <name> N`. An object is any binding, and a word of this production is a fixed word wherever it is written, so a prim may be called `light` or `radius`.

**GLTF materials.** A face can have a GLTF (PBR) material, and a script can override single fields of it for that face (`PRIM_GLTF_BASE_COLOR`, `PRIM_GLTF_METALLIC_ROUGHNESS`, `PRIM_GLTF_EMISSIVE` and the rest). The region keeps them and says them to the session, and `gltf` reads them from the object store as `light` reads a prim's, with no request of its own ([GLTF materials](gltf.md)).

```text
expect gltf override OBJ [link N] face N (is on|off | becomes on|off | changes)
expect gltf material OBJ [link N] face N (is UUID|none | becomes UUID|none | changes)
expect gltf colour|emissive OBJ [link N] face N (is R G B|none | becomes R G B|none | changes)
expect gltf alpha|metallic|roughness|cutoff OBJ [link N] face N (is N|none | becomes N|none | changes)
expect gltf alphamode OBJ [link N] face N (is opaque|blend|mask|none | becomes ... | changes)
expect gltf doublesided OBJ [link N] face N (is on|off|none | becomes ... | changes)
expect gltf basetexture|normaltexture|ormtexture|emissivetexture OBJ [link N] face N (is UUID|none | becomes ... | changes)
expect gltf SLOTrepeats|SLOToffset OBJ [link N] face N (is S T|none | becomes S T|none | changes)
expect gltf SLOTrotation OBJ [link N] face N (is TURNS|none | becomes TURNS|none | changes)
```

where `SLOT` is `base`, `normal`, `orm` or `emissive`, so that `baserepeats`, `normaloffset` and `emissiverotation` are props, twelve in all.

```text
```

Each is one reading, so each takes `original`, a capture, `any` after `is` with `as`, `near` for the numbers and `expect no`, as every state expectation does, and each has its own transcript line, `gltf <prop> <name> face <n> <value>`, and its own capture type. The grammar follows the face expectations: the word that names the reading first (`gltf`, then the prop, as `light` takes `colour` and `radius`), then the object, `link`, `face` and the state. It is a word to a reading, and not one expectation with several parts, for the reason a light is ([Light and projector](#expectations)): a reading here is one value that is compared, remembered as a baseline and bound. There is one face and no `face all`, since an override belongs to a face and the faces' overrides have no common shape. The colour is one reading of three numbers and the alpha another, so that each fits a capture type; a script that sets all four of `PRIM_GLTF_BASE_COLOR` writes two lines.

What is read, and when it is `none`:

- `override` is `on` while the region has said the face has an override that sets something, and `off` for a face with none. `expect gltf override OBJ face N is off` is the form of "the face has no override", and `expect no gltf override OBJ face N is on within 2s` that none arrives.
- `material` is the id of the face's GLTF material, from the render material block of the object's update, and `none` for a face with none. A face takes an override only once it has a material, so a script's `PRIM_GLTF_*` on a face without one is accepted and ignored, and the test `expect gltf metallic OBJ face N is 0.25` fails with `gltf metallic OBJ face N none` on it. Either `expect gltf material OBJ face N is any as $m` or a literal says the face has one first ([GLTF materials](gltf.md#a-face-needs-a-gltf-material)).
- Every other prop is the value the face's override sets, and `none` when the override does not set it, or the face has none. A zero is a value and `none` is not: a metallic factor of 0 is an override, and `is 0` and `is none` do not match each other. The alpha is the base colour's fourth number and `colour` its first three, so a script that sets one of them sets the colour whole and `none` for both when it sets neither. `alphamode` is `opaque`, `blend` or `mask`, the viewer's names for 0, 1 and 2, which are not the words of `alphamode` (the legacy material's `none`, `default`, `emissive`). A texture is the id of the slot: `basetexture`, `normaltexture`, `ormtexture` (metallic and roughness, the viewer's occlusion-roughness-metallic slot) and `emissivetexture`, and the id `ffffffff-ffff-ffff-ffff-ffffffffffff` is a slot overridden to no texture, which is what the region says for one ([GLTF materials](gltf.md#the-message)). A slot's `repeats`, `offset` and `rotation` are the transform the override sets on that slot's texture, and each is `none` when the override sets none of that field for the slot. They are named after the slot's texture prop (`basetexture`, `baserepeats`) and after the face's own words (`repeats`, `offset`, `rotation`), the slot word first because that is the order the texture props have and the slot is what a script names (`GLTF_Base`, `GLTF_Normal`). The units are the face's own ([Offset, repeats, rotation](#expectations)): repeats and offset as numbers, and a rotation as a fraction of a turn, `0.25` a quarter turn, where a script gives degrees (`PRIM_GLTF_*` rotations are in degrees) and the wire carries radians. Measured on 2026-10-07 on a product's `GLTF_Base` override with repeats `<2, 3>`, offset `<0.25, 0.5>` and rotation 90, as the script was given it: the override message carried `'ti':[{'o':[r0.25,r0.5],'r':r1.5708,'s':[r2,r3]}]`, so the repeats are `s`, the offset `o`, the transform is on the base slot (slot 0), the other slots had no `ti`, and the rotation is in radians, 90 degrees as 1.5708. So `baserepeats` reads `2 3`, `baseoffset` `0.25 0.5` and `baserotation` `0.25`. The rotation is the wire's radians divided by 2 pi, and it is compared the short way round, as a face's `rotation` is: the difference of two rotations is taken modulo one turn into [-0.5, 0.5), so `0`, `1` and the wire's `6.2832` are one angle, and `0.75` and `-0.25` are. A literal is checked against -1 to 1, so `90` is refused and `0.25` is the way to say it. The tolerances are a millionth for repeats and offset, as the override's factors have (they travel as the same printed reals), and ten millionths of a turn for a rotation, because the region printed the radians to five figures (`1.5708` for a quarter turn, under a millionth of a turn short of exact), which is inferred from that one message and is why the floor is not a millionth; `near` widens each, in the unit of the reading.
- `becomes none` is how a clear is shown: an override that is removed reads `none` for every field and `off`.

The colours and factors are the float32 the region sent, written as the 0 to 1 a script gives, and are compared within a millionth, which is only the printing; `near` is for a number a script worked out. That the region sends a script's number without changing it is inferred from the two it was seen to send (0.25 and 0.75) and was not measured for others.

A `gltf` expectation needs the session to hold `ModifyMaterialParams`, which the region sends overrides for and for no one else: a run against a session without it stops at that step, exit 3, `slate: setup: this session holds no ModifyMaterialParams capability, which gltf reads a face's GLTF material overrides from; slgod is likely older than this slate, or the session logged in before it was upgraded: restart it from the same release`, whatever the face is. Without it a face would read as having no override, which is not true, so the run does not guess.

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

**Substance.** `expect substance OBJ [link N] (is NAME | becomes NAME | changes)` reads a prim's physical material, the one a script sets with `PRIM_MATERIAL` and the build tool's General tab calls Material. `NAME` is one of LSL's eight, in LSL's own order and with its own values:

| Name | Value | Name | Value |
|---|---|---|---|
| `stone` | 0 | `plastic` | 5 |
| `metal` | 1 | `rubber` | 6 |
| `glass` | 2 | `light` | 7 |
| `wood` | 3 | | |
| `flesh` | 4 | | |

These are Linden's published `PRIM_MATERIAL_*` constants (`keywords_lsl_default.xml`, `lllslconstants.h` and `material_codes.h` of Firestorm 885631b93a agree); each was read back as a script set it on 2026-10-07 for wood, glass, rubber and stone ([Substance](slate-runner.md#substance)), and the others are the constants' values, not measured. The word is `substance`, not `material`, because `material` already names a GLTF material in `expect gltf OBJ material ...`, and a prim's legacy materials are the `normalmap`, `specularmap`, `glossiness` and `environment` readings: three things the viewer's build tool calls a material. A name that is none of the eight is a parse error (`expected a material name (stone, metal, glass, wood, flesh, plastic, rubber or light) or original`), and so is a number or a capital: a file names what it expects, and cannot write a byte outside them. A prim whose byte is none of the eight reads as that number, a line `substance sign 9`, compares unequal to every name, is a reading `changes` and a capture hold, and is kept whole (the viewer keeps the byte whole). It takes no `face`, `near` or `percent`; `original`, `any` with `as $x`, a capture of its own type (a click's or a text's is refused, `capture type mismatch`), `link N`, the baseline and `expect no` are those of every state expectation, and a prim no update has described reads nothing, so it is not read as stone. The reading is `Seen.Material` of the prim, with `MaterialKnown` ([the physical material](objects.md#the-physical-material)); a poll reads it with no request of its own, and an unmatched step asks the region to describe the prim again, as for any reading. **Unmeasured:** that a script's `llSetPrimitiveParams` with `PRIM_MATERIAL` reaches the store, and how soon, has not been watched on the grid; nothing here says a window shorter than the default of 10 s is enough ([Substance](slate-runner.md#substance)).

**Face properties.** Four more readings of a face, from the same texture entry as a texture and on the same cadence, so a change is seen as soon as a texture change would be:

| Expectation | Value | The reading | Tolerance |
|---|---|---|---|
| `fullbright OBJ face N is on` | `on` or `off` | The face's full-bright flag, bit 0x20 of its bump byte | Exact |
| `glow OBJ face N is 0.5` | A number in [0, 1] | The glow byte over 255 | 1/255 |
| `colour OBJ face N is 0.25 0.5 0.75` | Three numbers in [0, 1]: red, green, blue | Each of the three colour bytes over 255 | 1/255 each |
| `alpha OBJ face N is 0.4` | A number in [0, 1] | The fourth colour byte over 255 | 1/255 |

Glow, each colour channel and alpha travel as one byte, `round(value × 255)`. That was measured on 2026-10-01 on the grid, with a script setting face 0 and the session reading the store: glow 0.5, 1.0, 0.25 and 0.01 read as the bytes 128, 255, 64 and 3, and the colour `<0.25, 0.5, 0.75>` with alpha 0.4 read as 64, 128, 191 and 102 ([Fifth round](slate-runner.md#fifth-round)). A literal is therefore matched within one step of the byte, so `glow 0.5` accepts the byte 128. The state words, `link N`, `original`, the negative forms and `as $x` are those of every state expectation. The values print as the transcript lines of [Reading the result](#reading-the-result), rounded to four places.

**Alpha mode.** `expect alphamode OBJ [link N] face N (is | becomes) (default | none | blend | mask | emissive)` and `expect alphamode OBJ [link N] face N changes` read how a face is drawn where its texture has alpha. Unlike the readings above it is not in the texture entry: the entry names the face's material by id, and the mode is in the material, which the region gives out on its `RenderMaterials` capability ([Materials](materials.md)). The runner reads each material once and remembers it, since a material's id changes when any of it does. The five values:

| Value | Means |
|---|---|
| `none` | The material says opaque: the texture's alpha is ignored. |
| `blend` | The material says blended. |
| `mask` | The material says masked: a pixel below its cutoff is dropped. `is mask` takes any cutoff; the transcript line says which. |
| `emissive` | The material says the texture's alpha is how much the face glows. |
| `default` | The face has no material at all, so there is nothing to read and the viewer decides from the texture: blended if it has an alpha channel, opaque if not. It is its own value, and is not `blend`: the two are not the same fact. |

A face a script has given the mode blend and nothing else has no material (measured, [Materials](materials.md#a-face-with-no-material)), so it reads `default` and never `blend`; setting none, mask or emissive does make a material. A test of a script that sets blend therefore expects `default`, or `blend` only where something else on the face makes the material. `changes` compares the mode and, for a mask, the cutoff, so a mask at a new cutoff is a change; `is mask` does not compare the cutoff. `original` is the mode and cutoff the test began with. A negative, `link N` and `within` are those of every state expectation. It takes no `any` and no `face all`; `as $x` binds the mode as text, `"mask 128"` for a mask and the bare word for the rest, in the form of the transcript line. The reading needs the `RenderMaterials` capability: a run against a session that does not hold it stops at that step, exit 3, `slate: setup: this session holds no RenderMaterials capability, which alphamode reads a face's material from; slgod is likely older than this slate, or the session logged in before it was upgraded: restart it from the same release`, whatever the face is.

**Material maps.** `expect normalmap OBJ [link N] face N|all (is | becomes) (UUID | none)` and `expect specularmap ...` the same way read the id of the face's normal map and of its specular map, and `expect glossiness OBJ [link N] face N|all (is | becomes) N` and `expect environment ...` read its glossiness and its environment intensity. Each also takes `changes`, `original`, a capture (`$x`) and `is any as $x`, and the negative forms. They are read from the material, as `alphamode` is ([Materials](materials.md)), by the same capability, once for each material, and the same cases stop the run: a session without `RenderMaterials` is exit 3 at that step, with the word of the expectation in the message (`which glossiness reads a face's material from`).

- **None.** A face with no material has no maps, and a material need not have both. Either reads the null key, which is written `none` in a test and in the transcript; the uuid `00000000-0000-0000-0000-000000000000` is the same value and is accepted too. A capture of it prints that uuid, since a capture is a uuid and not a word. An invented example: `expect normalmap sign face 3 is none` holds of a face a script has not given a normal map to.
- **Units.** A level is the whole number the material holds, 0 to 255, which is what LSL's `PRIM_SPECULAR` takes for its glossiness and its environment, so a test writes what a script set. `glossiness` is the material's `SpecExp`, the specular exponent, and `environment` its `EnvIntensity`; the viewer reads both from the material as `U8` (`indra/llprimitive/llmaterial.cpp:403-404`, Firestorm 885631b93a) and its build floater shows the same two numbers (`indra/newview/llpanelface.cpp:338-339`). A face with no material reads 0 for both, which is a statement about the reading and not the viewer's own default for a new material (51 and 0, `indra/llprimitive/llmaterial.h:55-57`): a material that was made for a normal map alone carries the material's defaults, glossiness 51 and environment 0 (measured). **Measured** on 5 October 2026, on a box whose script set its faces on command: a face given a normal map alone read `glossiness 51`, `environment 0` and `specularmap none`; faces given a specular map with 200 and 100, and with 10 and 250, read those numbers back unchanged; a face with no material read 0 for both.
- **Tolerance.** All four are exact; the two levels take `near`, in steps of the level (`near 5` is five of 255), and a map takes none.
- **Face all.** `face all` reads the material of every face, as the other face properties do, and its tuple is the value of each face from face 0: `glossiness sign face all 0, 200, 51, 0, 0, 0`. A capture of it holds every face.
- **Why not one word.** A script sets the specular map, glossiness and environment together in one `PRIM_SPECULAR`, but a test usually cares about one of them, and the three differ in kind: an id, and two levels that no one writes unless they mean to. A combined `specular ... is UUID GLOSS ENV` would need a form for a value left open (`any` in two places), and a capture of three kinds of value at once. Four words each compare and capture one thing, as every other property here does.

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
| `as $x` after `give` | The item's name; after `give folder`, the folder's | text |
| `as $x` after a state expectation | The reading that matched; with `face all`, the tuple. After `turn`, the Euler degrees the transcript prints; after `light` or `projector`, the value the line prints (on or off, a triple, a number, a texture id); after `gltf`, the value of the field the override sets (a slot's repeats or offset as its two numbers and its rotation as turns; `any` takes only a field that is set, so there is never a `none` to bind) | its own type |
| `as $x` after `normalmap` or `specularmap` | The map's id, the null key for a face with none; with `face all`, the tuple | uuid |
| `as $x` after `glossiness` or `environment` | The level, 0 to 255; with `face all`, the tuple | number |
| `as $x` after `alphamode` | The mode as the transcript writes it: `default`, `none`, `blend`, `emissive`, or `mask 128` with the cutoff for a mask. It has no `face all`, so there is no tuple | text |
| `as $x` after a button reading | The count | number |

The types, the scope and the rule that a capture is bound once per test are in [Static checks](#static-checks). In short, a capture is usable from the next step on, never in the step that binds it, and only a positive expectation binds one. A group that took part in the match binds its text, which may be empty. A group that did not take part (inside an alternative that was not taken, or a `?`) fails the step with `$name did not take part in the match`, and nothing from that match is bound.

```slate
expect dialog from hud button 1 matching "^(?P<first>.+)$"
choose $first on hud
expect say $first on public from object sign
```

A capture is data. It is never parsed as Slate, never compiled as a pattern and never spliced into a string, and it is not usable inside a `matching` pattern. Each use is the value whole, as a literal in that position: exact for a `say` and a `give`, folded for `choose` and a dialog button, as the text of a button part for a `text` part. The checks that are static for a literal are made at the step for a capture, before the stimulus is sent: non-empty after trimming for a button part, and the link-text character rule and the length of the control line for a `send` text. A capture that breaks one fails the step with a sentence that names it and quotes its value, as under [Reading the result](#reading-the-result).

**Give.** Passes when the tester's inventory holds a non-folder item of that name whose id was not there at the arm point, and the offer's `FromName` equals the name of any prim of the binding's linkset. A give stays on the linkset: it takes no `link N`, because the give offer does not say which prim sent it. The name is a string (exact and case-sensitive) or a `matching` pattern. An older item of the same name does not count and does not block the pass. Seeing the offer alone is not enough. With `to NAME` the offer is one made to a second avatar, which the runner declines and never accepts ([A second avatar](#a-second-avatar)); the expectation passes when the offer is seen. Otherwise the runner accepts the offer, and says so in the transcript: `give accept sent to <uuid> transaction <uuid> into <folder>`. The item's name is read out of the offer text; [Give](slate-runner.md#give) says how. Two offers that match, or two new items of that name, fail the step as ambiguous and name both. If an accept was sent and the count of items of that name has not grown, the unmatched line says so:

```text
unmatched give "Example Thank You" from vendor within 10s; accept was sent and inventory still has 1 item of that name
```

**A folder given.** An object's script can give a folder (`llGiveInventoryList` to its owner), which arrives as an offer of a category. `expect give` refuses that offer, and `expect give folder` takes it: `expect give folder "Example Starter Folder" from vendor holding "Example Red Swatch" "Example Blue Swatch" within 10s`. The name is a string or a `matching` pattern, as for an item, and the offer is the one whose object is a prim of the binding's linkset, as for an item. The runner accepts it into the root of the inventory, as the viewer accepts one ([A folder given](slate-runner.md#a-folder-given)), and says so with the same `give accept sent` line, `into My Inventory`. The expectation passes when the inventory holds a folder of that name whose id was not there at the arm point; an older folder of the name does not count and does not block. With `holding`, each text, a string or a `matching` pattern, must also match the name of an item directly in that new folder (a link is not an item, and a folder inside it is not looked into). The items are listed after the folder, so a folder that is there and does not yet hold them is looked at again until the deadline. `as $x` binds the folder's name, and a named group in the folder's own pattern binds as for an item. Two new folders of the name are ambiguous. `folder` and `holding` are words only where the grammar expects them. `to NAME` is not allowed with `folder`: a second avatar's offer is declined as it is heard, so there is no folder to hold.

The two forms refuse each other's offer. An `expect give` meeting an offer of a folder, and an `expect give folder` meeting an offer of an item, does not accept it, and says so on the unmatched line: `; the offer of that name is a folder, and expect give folder takes it`, and `; the offer of that name is an item, and expect give without folder takes it`. What neither takes is declined at the end of the test with any other offer. When the accept was sent and a new folder of the name is there that does not hold what was asked, the line says what it held, `; the new folder holds "Example Blue Swatch", "Example Red Swatch"` (names in sorted order), or `; the new folder holds nothing`; when no new folder came, `; accept was sent and inventory still has 1 folder of that name`. The runner does not delete the folder afterwards, as it does not delete an item. Not measured: no folder given by a script has been accepted on the grid yet, so the offer text of a folder, taken to be the item's form (the viewer's own comment on it says so), and where it lands are read from the viewer's source.

The runner does not decline an offer, and does not delete the item afterwards. A later run passes on a newer id.

**Rez.** A new root prim: its id was not in the region at the arm point, it has no parent and it is a prim. Child prims of a new linkset and avatars are not rezzes. The region does not say which object rezzed a prim, so `from OBJ` is the substitute and is required. A prim is claimed when:

- its position is within 10 metres of some prim of `OBJ`'s linkset, measured from the root of each prim's linkset (child offsets ignored; a worn HUD is measured at the avatar, a seated wearer at the seat). The 10 m is the historical `llRezObject` limit used as a heuristic. It is not a claim that the region enforces it, and it is not measured here; and
- if both owners are known and non-zero, they are equal.

`name` is exact and case-sensitive, or a `matching` pattern; `description`, when written, is the same. If the linkset's position is unknown the claim does not match and the rejection reason is `position unknown`. With zero matches at the deadline, the report lists every new root seen and the reason each was rejected. A second root that matches the same claim fails the step at once and binds neither; the report lists both. With several claims, each matching root goes to the earliest unmatched claim, in source order, that it satisfies. Roots seen in the same poll are ordered by local id, which is not the order of the product's rez calls, so write `description` or a distinct `name` when it matters which is which. A new root that matches `from` and no claim fails the step. A root outside the radius, or with another owner, does not.

The `as` name is bound when the claim is assigned and refers to that root prim in every later step. If the step fails, the name is not usable. A claimed object stays in the region when the test ends: the runner deletes only what its own `rez` steps made. A test deletes a claimed object, if it is the tester's, with `delete NAME` ([Delete](#stimuli)), usually in `after each`, where it may name a binding the test body made. Bindings made with `as` are not renames; the prim keeps whatever name the product gave it.

**Link.** A probe report for this object's linkset with the sender, number, text (a string, or a `matching` pattern) and key given (`key` defaults to `null`). `heard by N` also requires that the prim that received it reported link N. Without `heard by`, the first report from any prim in the linkset passes, and further reports of an `all` delivery are not failures.

**Negative expectations.** `expect no BODY within D` passes when BODY does not match at any time from the arm point through `D`, and fails at once, quoting the event, if it does. The forms are the positive ones, except that a negative rez has no `as`, and a negative state expectation still needs a real reading: no reading is a failure at the deadline, not a pass. For state:

- `expect no texture sign face 0 changes within 2s` passes when no reading differs from the baseline during the window.
- `expect no ... becomes X` passes when there is no transition to X during the window.
- `expect no ... is X` passes when no reading equals X, and a reading must exist.

These are the rules of the button reading and of `attached` too.

A negative `say matching` is the way to rule out a family of lines, as in `expect no say matching "(?i)error" ...`. Silence is the pass for say, dialog, give, rez and link. `expect no` is the only negative form. There is no negative that spans the rest of the file. To require a quiet second after a success, write a later step whose only expectation is the `expect no`, with its own `within`.

**Animation.** `expect animation UUID is on`, `is off`, `becomes on`, `becomes off` and `changes`, each with an optional `from OBJ`, read whether the tester is playing an animation, as the region says in `AvatarAnimation`. The id is the animation's asset id: a built-in's constant, or the asset of an animation in an inventory or in an object, and never the id of an inventory item. They are state expectations with the words of the others: `is on` passes when a reading says it is playing, including one that already did at the arm point; `becomes on`, the start, passes when a reading says it is not and a later one says it is, and `becomes off`, the stop, the other way; `changes` passes on either. `expect no animation UUID becomes on from OBJ within 5s` is the negative, and needs a reading as any state does. The words are those of a state, and not `starts` and `stops`, so that an animation already playing when a step begins is `is on`, and the same step does not pass by mistake on a start it did not see.

`from OBJ` says the animation was started by `OBJ`: the simulator names, for each animation, the object whose script started it, and `from` holds when that object is any prim of `OBJ`'s linkset, found by its id and never by its name. Without `from`, whoever started it, and an animation the avatar plays by itself, a stand or a walk, counts. With `from`, an animation the simulator names no object for is not from `OBJ`. The id is written out: there is no `original`, `any` or capture, because an animation has nothing to compare with but on and off and no value to hold. A test author learns an animation's id from the transcript: every animation a binding named in a `from` starts is printed, `animation <uuid> started from <binding>`, whether or not an expectation names it, so a first run with a made up id shows the real one. The id of a built-in is in `agent.BuiltinAnimations` and in `animate --list`; the asset id of an animation inside a product is not read from the product's contents by anything here, and the transcript is how it is learned. An expectation by an item's name is not built.

What the tester plays comes from the list the region sends about every three seconds and whenever it changes, so a reading is as old as the last list and a start is seen about when the region says it. A run that has an `expect animation` asks the daemon for `AvatarAnimation` for the length of the run and waits up to 10 s, at setup, for the first list; it is exit 3, `no AvatarAnimation was heard in 10s`, when none comes. The reading is kept when the list changes and is stamped when the list was heard, so a start that came before a step's arm point is not a start of that step. An animation that starts and stops between two lists is never seen. The run does not stop an animation it saw start, and it has no stimulus to: the permission was the object's, and what happens to an object's animation when the tester asks for it to stop is not measured here ([Animations](slate-runner.md#animations)). A test of a timed animation expects its stop (`becomes off`), and the next test's baseline is what the avatar is playing then.

**Sound.** `expect sound UUID` is two expectations in one word, a sound heard and a loop's state, and the words after the id say which. The id is the sound's asset id: a Linden built-in, or the asset of a sound uploaded or held in an object, and never the id of an inventory item.

`expect sound UUID [from OBJ] [gain N [near T]] within D` says the sound was heard to play after the step's arm point: a `SoundTrigger` (`llTriggerSound`, a one-shot at a place) or an `AttachedSound` with a sound (`llPlaySound`, `llLoopSound`, which a loop is, too). It is an event, as a line of chat is, and not a state: a sound has no value to be on or off, and a one-shot has no end the region reports. It is found by its id; `from OBJ` holds when the prim that played it is any prim of `OBJ`'s linkset, by id and never by name; `gain N` holds when the gain the region sent is within a margin of N (0.001, or `near`, in the unit of the gain, or `near T percent` of N). The gain is the one the viewer plays at, 0 to 1. The sound is matched once: a step's expectation takes the first sound that fits it, and the next step does not see it again, as with chat. `expect no sound UUID from OBJ within 5s` is the negative: nothing like it was heard for the window. An `AttachedSound` that asks for a loop that is already running is not heard to play again, as the viewer ignores it.

`expect sound UUID is looping`, `is stopped`, `becomes looping`, `becomes stopped` and `changes`, with an optional `from OBJ`, are states of a loop, with the words of the other state expectations: `is looping` passes when a reading says one is running, including one that already was at the arm point; `becomes looping` is the start, `becomes stopped` the stop, and `changes` either. A loop is running from an `AttachedSound` with the loop flag until the region sends a null sound for the prim, a sound that is not a loop for it, a loop of another sound, or kills the prim. **A loop at gain 0 is still looping**: a gain change does not end one. `stopped` is no loop of that sound, so it is true of a sound that never played and of a one-shot, which is never `looping`. The words are `looping` and `stopped` and not `playing` because a one-shot has no end the region reports: it can be heard (`expect sound ...`) but is not a state to be in.

A test author learns a sound's id from the transcript: every sound a bound object plays is printed with its id, expected or not, and so is a sound an expectation names whoever plays it, so a first run with a made up id shows the real one. The id of a Linden built-in is in the viewer's settings and `tools/known-uuids`; the id of a sound inside a product is not read from the product's contents, and the transcript is how it is learned. A sound heard from an object nothing in the file binds, and names no expectation, is not printed, since a region has many.

A run with an `expect sound` in a selected test asks the daemon for the four sound messages for the length of the run, and keeps what it hears; there is no wait at setup, because the region sends a sound when it plays and not on a period. A loop that was already running when the test began is known from the prim's own update, which the session always hears, and is the first reading. What the region sends the avatar is what the viewer would play: a sound from an object out of range, or a loop that began and ended before the session listened, is not seen (inferred from the viewer's code, not measured; [Sounds](slate-runner.md#sounds)). The run plays no sound and stops none.

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

**Link numbers** are what `llGetLinkNumber` reports: 0 for an unlinked prim, which still receives messages addressed to link 1, 1 for a linked root, and 2 and up for children. They come from the store, or from a probe when there is one, which wins. With no probe on the binding the runner asks the session for the linkset of the binding's root ([Link numbers](objects.md#link-numbers)), so a child prim of a product the tester does not own can be named: `touch hud link 3`, `expect texture hud link 2 face 0 ...`, `from object hud link 2`. With a probe, the hello map is the answer exactly as before, because it is the script's own truth; the store is not asked. Nothing is fixed at setup, since a product can relink itself: a touch or drag finds its prim when its step is prepared, a reading on each poll that reads that prim, and a speaker or dialog match when the line or dialog arrives. A reading therefore follows the number to the new prim after a relink. A set whose order the store cannot vouch for, or a link the set does not have, fails with a sentence ([Reading the result](#reading-the-result)). A worn linkset is numbered under its root in the same way. The store's reading is what a `link N` uses unless the file asks for the region's own count with a `linkmap` header, and the transcript says which for each object a step addresses with `link N` ([Link order from the object's own script](slate-runner.md#link-order-from-the-objects-own-script)). Setup says, for an object with a `linkmap`, what its own script made of the store's reading, and for any other object with no probe, once, that the store's reading stands:

```
slate: link order of vendor confirmed by its own script (3 links)
slate: link order of hud corrected by its own script: the store had other prims at links 2 and 3 (7 links)
slate: link order of box is the store's best reading from the packets (no linkmap header for it)
```

**Linkmap.** `linkmap vendor` has the object's own script say its link numbers at setup, for an object the file addresses with `link N` and that has no probe. A small script is dropped into the root, says each link's number, key and name to the owner, and removes itself, after which the store takes the numbers over its reading. **That is two changes of the object's inventory, the script going in and the script going out, and every script in the object gets `changed()` with `CHANGED_INVENTORY` for each.** A product that reloads its configuration, resets, or re-reads its notecards on that event is then tested from a state the file did not ask for, and no step is there to show it. So nothing is dropped unless the file says `linkmap`; with it, the author has chosen to risk that for the certainty. It is not offered for a binding a step makes with `wear` or `rez`, which no header can name, and the object must be one the tester owns and may modify and on land that runs scripts: a `linkmap` object the tester may not modify, or on land that runs none, is a setup error (exit 3), `slate: setup: linkmap vendor: the tester may not modify it, ...`, and not a guess that goes on. A `linkmap` and a `probe` on one object is refused: the probe's hello map is already the script's own count of every link, and costs the object nothing more.

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
| 3 | Setup failed: the dial (`slate: dial: <error>`, on standard error), a second avatar the run was not given as the file declares it (`slate: setup: visitor is declared but no --avatar visitor=... was given`, and the other sentences in [A second avatar](#a-second-avatar)), object lookup, item lookup, a `linkmap` object the tester may not modify, probe install, hello, the bridge. Also a click action still unknown when its 30 s wait ends. Exit 3 outranks 1. For a name bound with `as`, that exit is printed while the rez step is still open, before any later step. |
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
HH:MM:SS.mmm give from <who>: folder "<name>"
HH:MM:SS.mmm give accept sent to <uuid> transaction <uuid> into <folder>
HH:MM:SS.mmm dialog to <avatar> from <who>: "<message>" buttons <labels>
HH:MM:SS.mmm textbox to <avatar> from <who>: "<message>"
HH:MM:SS.mmm give to <avatar> from <who>: "<item>"
HH:MM:SS.mmm give to <avatar> declined, transaction <uuid>
HH:MM:SS.mmm permission denied to <avatar> from <who>: <mask>
HH:MM:SS.mmm rez <uuid> name "<name>" at <x> <y> <z>
HH:MM:SS.mmm texture <object> face <n> <uuid>
HH:MM:SS.mmm fullbright <object> face <n> <on or off>
HH:MM:SS.mmm glow <object> face <n> <level>
HH:MM:SS.mmm colour <object> face <n> <red> <green> <blue>
HH:MM:SS.mmm alpha <object> face <n> <level>
HH:MM:SS.mmm alphamode <object> face <n> <mode>[ <cutoff>]
HH:MM:SS.mmm normalmap <object> face <n> <uuid or none>
HH:MM:SS.mmm specularmap <object> face <n> <uuid or none>
HH:MM:SS.mmm glossiness <object> face <n> <level>
HH:MM:SS.mmm environment <object> face <n> <level>
HH:MM:SS.mmm button <object> <parts as written> <n> (faces <list>)
HH:MM:SS.mmm attached <object> <point or off>
HH:MM:SS.mmm animation <uuid> <started or stopped or playing>[ from <object>]
HH:MM:SS.mmm sound <uuid> <triggered or played or looping or stopped>[ from <object>][ at gain <g>]
HH:MM:SS.mmm capture $<name> = <value> (step <N>)
HH:MM:SS.mmm position <object> <x> <y> <z>
HH:MM:SS.mmm size <object> <x> <y> <z>
HH:MM:SS.mmm turn <object> <x> <y> <z>
HH:MM:SS.mmm light <object> on|off
HH:MM:SS.mmm light colour <object> <r> <g> <b>
HH:MM:SS.mmm light intensity|radius|falloff <object> <n>
HH:MM:SS.mmm projector <object> <texture>|off
HH:MM:SS.mmm projector fov|focus|ambiance <object> <n>
HH:MM:SS.mmm gltf <prop> <object> face <n> <value>|none
HH:MM:SS.mmm click <object> <byte>
HH:MM:SS.mmm pay L$<amount> to "<name>" <uuid> reason "<reason>"
HH:MM:SS.mmm probe link <object> heard-by <n> from <sender> num <num> [key <uuid>] "<text>"
HH:MM:SS.mmm probe overflow <object> heard-by <n> bytes <n>
HH:MM:SS.mmm probe bad <object> link <n>
HH:MM:SS.mmm probe fwd-overflow channel <n> bytes <n>
HH:MM:SS.mmm chat channel <n> from <who>: "<tail>"
HH:MM:SS.mmm permission denied from <who>: <mask>
HH:MM:SS.mmm permission granted to <who>: <granted>[; refused: <rest>]
```

A line for a second avatar names it by its binding, `<avatar>`, in `dialog to visitor from sign: ...`, and never by its in-world name or its profile; a chat line it spoke is `chat public from visitor: "..."`. `permission denied to <avatar>` is the refusal of a request to a second avatar, which no `allow permission` covers. `permission granted to` is the answer to a request an `allow permission` header covers: the permissions granted, then, when part of the request was refused, `; refused:` and those. `<how>` is `public`, `owner`, `debug`, `direct`, `region`, or `channel <n>`, which is a line the worn bridge forwarded from a `listen` channel; it is product chat, printed as `chat channel <n> from <who>: "<tail>"` with the raw tail. `<who>` is the script's name for a bound prim, otherwise the displayed name. The lines that begin `probe` are protocol, never `chat`. `probe link` carries ` key <uuid>` after the num only when the key the message carried is not the null key. `probe overflow` is a report the probe could not send whole; `probe bad` is a command the probe could not parse, on the link the probe is in; `probe fwd-overflow` is a forward the bridge could not send whole, on a `listen` channel. The pay line prints before the payment, including when it then fails. A texture line prints whenever a reading differs from the last one printed, stale readings included; the offset, repeats and rotation expectations print it too. A fullbright, glow, colour, alpha, alphamode, normalmap, specularmap, glossiness or environment line prints the same way, and each is compared only with the last line of its own kind for that object and face. A map line says the id, or `none` for a face that has no such map, and a level line the whole number: `normalmap sign face 1 <uuid>`, `specularmap sign face 1 none`, `glossiness sign face 1 200`; the reading of a material that cannot be read prints no line, as for an alphamode. An alphamode line says the mode, one of `default`, `none`, `blend`, `mask` and `emissive`, and for a mask the cutoff after it: `alphamode sign face 1 mask 128`; a reading that cannot be taken, because the material could not be read, prints no line, and the failure block's `unmatched` line says why. A level is rounded to four places. With `face all` the face is written `all` and the line lists the value of each face from face 0, separated by commas: `fullbright sign face all on, off`. A position or size line prints the same way, `position hud 0 0 0` or `size hud 0.5 0.25 0.1`, and is compared with the last line of its own kind for that object. A substance line is the word, the object and the material, `substance box wood`, or its number when it is none of the eight, `substance box 9`, and prints the same way. A floating text line prints whenever the text differs from the last one printed for that object, `text sign "Controlling Example Chair"`, with the text quoted as Go quotes a string, as a chat line is, so a newline in it shows as `\n` and a quote as `\"` and neither can start a line of its own. A button line prints whenever the count differs from the last one printed for that prim and those parts, and ends with the faces the buttons were found on, `faces 0, 2`, or `faces none` when the count is 0. With a `link` the object is written `<object> link <n>`. An animation line prints when the list the region sent differs from the last one printed: `animation <uuid> started from sign`, `animation <uuid> stopped from sign`, and `playing` in the place of `started` for what was playing when the test began; the `from <object>` is the binding named in a `from` of the file, which is how a started animation is attributed, and a line for an animation named without `from` has none. An attached line prints when the reading differs from the last one printed for that name, `attached hud HUD centre 2` or `attached hud off`, and the point is written as `sl` names it. A capture prints when its expectation matches, once, with the step that bound it. Its value is a text quoted as Go quotes a string (a floating text included), a UUID, the two numbers of a pair, one number, a click byte as a number, a physical material as its name or, when it is none of the eight, its number, the three of a colour, the three of a position or size, `on` or `off`, or, for a `face all`, `face all` and the comma-separated values: `capture $first = "Example Sign" (step 3)`, `capture $all = face all on, off (step 1)`. When a step's baseline is the first reading after the arm point, the runner also prints `baseline for <object> face <n> taken after the arm point` (`face all` stands for the face of a `face all`; for a click byte it is `baseline for <object> click taken after the arm point`, and `position`, `size` or `substance` in the place of `click` for those readings, `text` for a floating text, and `baseline for <object> button taken after the arm point` for a button reading). If chat or instant messages arrive faster than the runner reads them, the run fails with exit 1, `the chat subscription dropped <n> lines`, rather than looking like a timeout.

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
slate: step N: the link order of "Example Tip Jar" is not known; bind the prim by its own name instead of link N, give the object a probe (the tester must own it), or take it and rez or wear it again
slate: step N: "Example Tip Jar" has no link 9; it has 3 prims
```

The first is a set the store cannot put in order ([Link numbers](objects.md#link-numbers)): one that several prims joined in one update while it watched, one with a child the region described only as an answer to a request, or one a daemon older than the link numbers reports. A number the store does give is its best reading of the order the region sent the set in, not the script's own count: only a probe is that ([When a set is known](objects.md#when-a-set-is-known)). The second is a number the set does not have; a set of one prim has link 0 only, and a larger set has 1 up to its prim count. A touch or drag fails at once, in its prepare step, and the sentence is printed as a line of its own and under `stimulus:`, with nothing sent. An expectation has no reading to fail on, and fails at its deadline with the sentence after its `unmatched` line, as a button reading's reason is, with the step's number:

```text
unmatched texture vendor link 2 face 0 is 6b5e7e57-7e57-c0de-5117-87121399d48f within 300ms; slate: step 1: the link order of "Example Tip Jar" is not known; bind the prim by its own name instead of link N, give the object a probe (the tester must own it), or take it and rez or wear it again
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

**Rez sentences.** The stimulus line of a rez step that succeeded, the lines the end of the test prints, and the refusal, which is the stimulus error:

```text
rezzed "Example Box" at <130, 128, 25> as made
slate: deleted made ("Example Box") to the Trash
slate: delete failed: made ("Example Box"): <error>
slate: fail test "<test>": could not delete made
sl: "Example Box" was not rezzed: the simulator said "Can't rez object 'Example Box' at { 130, 128, 25 } on parcel 'Example Parcel' in region Test Region because the owner of this land does not allow it.  Use the land tool to see land ownership."
```

The position is the one sent, with a `by` worked out from the tester. The delete lines come after the test's failure block, if any, and before `slate: pass test`.

**Delete sentences.** What a `delete` step prints, and the refusals, which are lines of their own:

```text
slate: deleted balloon ("Example Balloon") to the Trash
slate: balloon ("Example Balloon") is already gone; nothing deleted
slate: balloon was not bound in this test; nothing deleted
slate: step N: balloon ("Example Balloon") is not the tester's; it was not deleted
"Example Balloon" is still in the store 10s after the delete
```

In order: a delete that completed; an object already gone; a name `after each` was given that the test did not bind, which is not a failure; an object that is not the tester's, which fails before anything is sent; and a store that still lists the object when the stimulus budget ends.

**Drop and group sentences.** The stimulus line of a drop and of a group step that succeeded, the refusals that are lines of their own, and the lines the end of the test and the end of the run print:

```text
dropped "Example Red Swatch" into sign
dropped "Example Red Swatch" onto sign face 2; nothing went into the prim
dropped "Example Red Swatch" onto sign face 2; a copy went into the prim first
dropped "Example Red Swatch" onto sign face 2; the prim already held it
slate: step N: "Example Red Swatch" may not be copied, so dropping it would move it out of the tester's inventory and the run could not give it back; the drop was not sent
slate: step N: "Example Red Swatch" is not a texture; drop onto a face needs one, and the drop was not sent
slate: restored sign face 2 to texture <uuid>
slate: restore failed: sign face 2: <error>
slate: removed "Example Red Swatch" from sign
slate: remove failed: "Example Red Swatch" from sign: <error>
slate: fail test "<test>": could not put back sign
set the group of visitor to "Example Group"
the group of visitor was already "Example Group"
visitor has not joined a group called "Example Club"
slate: cleanup: put the group of visitor back to what it was
slate: cleanup: warning: the group of visitor was not put back: <error>
```

The restore and remove lines come after the test's failure block, if any, and before the delete lines of `rez`, and before `slate: pass test`. The group line comes at the end of the run, before the verdict.

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

**What a run leaves behind.** When a step fails, the test stops and its remaining steps are skipped; `after each` still runs, and the next test runs. A setup failure, an unknown click byte, an `alphamode`, `normalmap`, `specularmap`, `glossiness` or `environment` expectation without the `RenderMaterials` capability, a `gltf` expectation without the `ModifyMaterialParams` capability, or a dropped subscription stops the whole run. The runner does not stand the avatar up, answer a dialog it has not answered (it only ignores it, [The tester is left as found](#the-tester-is-left-as-found)), or send a payment again. A sit that succeeded leaves the avatar seated and the transcript says so. A `wear` leaves the item worn until a `take off`, and the runner does not take it off. A `rez` step's object is deleted to the Trash when its test ends, whatever the outcome ([Rez](#stimuli)). What a `drop` step put into an object, or a face it textured, is put back when its test ends ([Drop](#stimuli)), and a second avatar's group a `group` step changed is put back when the run ends ([Group](#stimuli)); a put back that fails fails the test (a drop) or is a cleanup warning (a group). An inventory offer an object made to the tester that no step accepted is declined and printed, one from a person is left waiting and printed, and an unanswered dialog is ignored and printed, when its test ends ([The tester is left as found](#the-tester-is-left-as-found)). A pending permission request is always answered and printed: denied (`permission denied`) unless an `allow permission` header names bits for the requesting object, which are granted (`permission granted to`) and the rest refused. On every exit after setup started, the runner removes the `slate probe` scripts and takes the bridge off, keeping it in inventory for the next run, printing `slate: cleanup: removed the probe from <n> prims, settling 6s after each` first (`1 prim` for one), and that line comes before the verdict, so the run's last line is its result; a 20-prim object can spend about two minutes settling after the result is known. A cleanup error is a warning and changes neither the exit code nor an earlier failure. Objects the product rezzed are deleted only by a `delete` step ([Delete](#stimuli)); a received item and a payment are not undone. The runner itself leaves nothing in the region, apart from an object a `rez` step made and whose delete failed, which the transcript names, and what a product rezzed and no `delete` step took. A run killed mid-way can leave the bridge worn and probes in the product; the next run reuses or replaces them.

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

### A folder given

A vendor that gives a folder when the tester asks for the kit. The folder is expected by name and by two of the items it holds; the folder and the items are invented. The offer is accepted into the root of the inventory, and `$kit` holds the folder's name for a later step.

```slate
slate 1

object vendor is "Example Tip Jar"

say "kit" on 0
expect give folder "Example Starter Folder" from vendor holding "Example Red Swatch" matching "Blue Swatch$" within 15s as $kit
```

If the vendor gives the folder as a plain item offer, or the folder is short of the blue swatch, the step fails and the unmatched line says which, and what the folder held.

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

### A vendor's balloon, deleted afterwards

A vendor owned by the tester rezzes a balloon when it hears `balloon`, and the balloon says `pop` when touched. The test claims the balloon, touches it, and deletes it in `after each`, so that the balloon is gone from the region when the test passes and when it fails, and a second run does not find the first one's balloon. The vendor must be the tester's: `delete` takes only an object the tester owns.

```slate
slate 1

object vendor is "Example Tip Jar"

test "a balloon pops" {
  say "balloon" on 0
  expect rez name "Example Balloon" from vendor as balloon within 5s

  touch balloon anywhere
  expect say "pop" on public from object balloon within 3s
}

after each {
  delete balloon
}
```

If the first step fails, `balloon` was never bound, and `after each` prints `slate: balloon was not bound in this test; nothing deleted` and carries on. If the vendor `llDie()`s the balloon after a while, the delete says the balloon is already gone and the test still passes.

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

The move can only be `changes` (or `changes near N`, to say by how much). The HUD moves by the drag in metres, 300/1025 m to the right and 200/1025 m down, which is 0.2927 and 0.1951 m ([Where a worn HUD is on the screen](hud-screen.md#a-drag)), but where it ends is that plus where ExampleHUD stood when it was worn, and the example does not know that. Slate has no arithmetic to add the two, and an `is` or `becomes` with a made-up start would be a number nobody measured.

The resize is derived, and it is only as good as the derivation. ExampleHUD's script scales the root by `1 + 2*|delta|/|<0.5,0.25>|`, with delta the pointer's change in metres; that is the formula in ExampleHUD's own script, and a drag of 150 and 100 pixels in a 2050-pixel view was measured scaling it by 1.3146, what the formula gives for that distance ([A drag](hud-screen.md#a-drag)); its root is 0.5 x 0.25 x 0.1 m as built at commit f174c9e. A drag of 150 and 100 pixels in a 1025-pixel view is a delta of 0.14634 and 0.09756 m, whose length is 0.17588, so the factor is 1.62925 and the size is 0.81462 x 0.40731 x 0.16292, written to four places inside the millimetre the reading is compared within. The press at `1480 640` is invented: it holds only if that point is on the HUD's resize corner. A different build, view height or press changes the numbers; a script that cannot say them writes `changes`. The `within 10s` is there because a drag with `settle` can take its duration plus the 5 s settle, and the step's budget is its longest `within` ([Static checks](#static-checks)).

### A HUD dragged and dragged back

A drag on the screen puts a HUD back only to within a few millimetres of where it was. Measured on 2026-10-05 with a worn HUD that moves and resizes by the change in the touch position: dragged and dragged back by the same pixels, it came back exactly on one try and a few millimetres off on another; resized and resized back, it came to about 1 percent from its size on each side. The default tolerance is a millimetre, so `becomes original` fails on the second try, and `changes` alone does not check that it came back. `near` is the margin the test accepts. The HUD and the numbers in the file below are invented.

```slate
slate 1
timeout 20s

item hud_item is "Example Panel" in "Objects"

before each {
  wear hud_item on "HUD centre 2" as hud
  expect attached hud on "HUD centre 2" within 10s
}

after each {
  take off hud
  expect attached hud off within 5s
}

test "dragged and dragged back" {
  drag hud on screen from face 0 at 0.5 0.9 by 300 200 over 800ms settle
  expect position hud changes near 0.005 within 10s

  drag hud on screen from face 0 at 0.5 0.9 by -300 -200 over 800ms settle
  expect position hud becomes original near 0.005 within 10s
}

test "resized and resized back" {
  drag hud on screen from 1480 640 by -150 100 over 800ms settle
  expect size hud changes near 2 percent within 10s

  drag hud on screen from 1330 740 by 150 -100 over 800ms settle
  expect size hud becomes original near 2 percent within 10s
}
```

The first test passes when the HUD has moved by more than 5 mm on some axis and has then come back to within 5 mm of where the test began on each axis. The second compares each side of the size within 2 percent of its own original: for a HUD 0.8 by 0.4, 0.016 and 0.008, so 0.79 by 0.395 passes and 0.78 does not. The press points are invented, as in the example before. A HUD that came back 8 mm out would fail the first, saying `; the nearest reading was 0.008 off, and at most 0.005 is allowed: position hud ...`, so the test says how much slack it needed.

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

### A face's alpha mode

A script on the sign gives face 1 a masked alpha mode when it is touched, and a second touch takes the material away again. The face starts with no material, which the runner reads as `default`; a mask is a material, so it reads `mask`, and `changes` is true again when the cutoff moves. `original` is what the face was when the test began. `as $x` binds the mode, as text and as the transcript line writes it, `"mask 128"` here if the cutoff is 128, for a later step to say or compare.

```slate
slate 1

object sign is "Example Sign"

touch sign face 0
expect alphamode sign face 1 becomes mask within 8s

touch sign face 0
expect alphamode sign face 1 becomes original within 8s
```

### A face's normal and specular maps

A script in an invented sign sets two maps on face 1 when it is touched:

```lsl
default {
    touch_start(integer n) {
        llSetPrimitiveParams([
            PRIM_NORMAL, 1, "546a7e57-7e57-c0de-f9f4-0791f9ce2463", <1, 1, 0>, <0, 0, 0>, 0.0,
            PRIM_SPECULAR, 1, "ccd57e57-7e57-c0de-1294-b7668fd8666d", <1, 1, 0>, <0, 0, 0>, 0.0,
                <1, 1, 1>, 255, 200, 30
        ]);
    }
}
```

The face starts with no material, so before the touch it has no maps and both levels read 0; a test that waits for them says `becomes`. The last two numbers of `PRIM_SPECULAR` are the glossiness and the environment, and are what `glossiness` and `environment` read. `face 0` is not touched and keeps no material, so it reads `none`.

```slate
slate 1

object sign is "Example Sign"

expect normalmap sign face 1 is none
expect specularmap sign face 1 is none

touch sign face 0
expect normalmap sign face 1 becomes 546a7e57-7e57-c0de-f9f4-0791f9ce2463 within 8s
expect specularmap sign face 1 becomes ccd57e57-7e57-c0de-1294-b7668fd8666d within 8s
expect glossiness sign face 1 is 200 within 2s
expect environment sign face 1 is 30 within 2s
expect normalmap sign face 0 is none within 2s
```

### A shiny panel

An invented panel in a region has a script that gives face 2 a GLTF material when it is touched and overrides three of its fields: a base colour of `<1, 0.5, 0>`, a metallic factor of 0.25 and a roughness of 0.75. A second touch removes the override and leaves the material. Face 3 has the same material and no override.

```slate
slate 1

object panel is "Example Panel"

expect gltf override panel face 2 is off within 5s
expect gltf material panel face 2 is none
expect gltf override panel face 2 is any within 100ms as $before

touch panel anywhere
expect gltf material panel face 2 becomes e45c7e57-7e57-c0de-21e7-84f950a0da9f within 8s
expect gltf override panel face 2 becomes on within 8s
expect gltf colour panel face 2 is 1 0.5 0 within 2s
expect gltf alpha panel face 2 is none within 2s
expect gltf metallic panel face 2 is 0.25 within 2s
expect gltf roughness panel face 2 is 0.75 within 2s
expect gltf emissive panel face 2 is none within 2s
expect gltf override panel face 3 is off within 2s
expect no gltf metallic panel face 2 changes within 1s

touch panel anywhere
expect gltf override panel face 2 becomes off within 8s
expect gltf metallic panel face 2 becomes none within 2s
expect gltf material panel face 2 is e45c7e57-7e57-c0de-21e7-84f950a0da9f within 2s
```

The face starts with no material and so no override; a material and the override come in the same few seconds after the touch, the region saying the override about three seconds after the script's call (measured on a box, [GLTF materials](gltf.md#the-message)), so the `within 8s` has room. `gltf alpha` and `gltf emissive` are `none` because the script set neither, and a zero would be a value. `0.25` and `0.75` are what the script wrote and the region sent, and read back exactly; a number the script works out would take a `near`. The last three lines are the clear: the override is `off`, every field of it `none`, and the material stays.

### A hat that says hello

An invented hat, worn by the tester, plays the built-in `hello` animation on its wearer when touched, and stops it after four seconds. Its script asked for permission to trigger animations, which the file grants. The animation's id is Linden's constant for `hello`, and is what the tester's `animate hello` would send. `from hat` is the object's own prim or any prim of its linkset, so the stand and the walk the avatar plays in the meantime do not count.

```slate
slate 1

allow permission trigger-animation from hat
object hat is "Example Greeting Hat"

expect animation 9b29cd61-c45b-5689-ded2-91756b8d76a9 is off from hat within 5s

touch hat anywhere
expect animation 9b29cd61-c45b-5689-ded2-91756b8d76a9 becomes on from hat within 5s

then expect animation 9b29cd61-c45b-5689-ded2-91756b8d76a9 becomes off from hat within 8s
```

### A doorbell and a warning lamp

Two invented objects play Linden's built-in sounds. The doorbell plays the viewer's click sound once at full volume when touched. The warning lamp loops the viewer's alert sound when touched, and stops it when touched again. Neither sound is the product's: a built-in is a constant every viewer carries, which is why the ids are written here and are found in `tools/known-uuids`. `from` is the prim that plays it or any prim of its linkset, so a sound of another object in the same region does not count. The loop is a state, and the click is an event: the click has no end to wait for, and the loop at gain 0 would still be `looping`.

```slate
slate 1

object bell is "Example Doorbell"
object lamp is "Example Warning Lamp"

touch bell anywhere
expect sound 4c8c3c77-de8d-bde2-b9b8-32635e0fd4a6 from bell gain 1 within 3s
expect no sound ed124764-705d-d497-167a-182cd9fa2e6c within 1s

expect sound ed124764-705d-d497-167a-182cd9fa2e6c is stopped from lamp within 1s

touch lamp anywhere
expect sound ed124764-705d-d497-167a-182cd9fa2e6c becomes looping from lamp within 3s

touch lamp anywhere
expect sound ed124764-705d-d497-167a-182cd9fa2e6c becomes stopped from lamp within 3s
```

### A lid that turns

A script in an invented jar opens its lid when touched: the lid is the second prim of the linkset, and the script turns it 110 degrees about its Y axis, relative to the jar. A second touch closes it. The jar stands on the ground with a quarter turn about Z of its own, and that is not the lid's to carry: `turn` reads the lid as the region reports it, relative to its root. `becomes 0 110 0 near 0.5` allows half a degree of angle, because a script that sets a rotation with `llEuler2Rot` and a viewer that rounds what it shows rarely agree past that.

```slate
slate 1

object jar is "Example Jar"

expect turn jar is 0 0 90 within 5s
expect turn jar link 2 is 0 0 0 within 5s as $shut

touch jar link 2 anywhere
expect turn jar link 2 becomes 0 110 0 near 0.5 within 8s
expect no turn jar changes within 1s

touch jar link 2 anywhere
expect turn jar link 2 becomes $shut within 8s
```

`180 0 0` and `0 180 180` are one rotation, so a lid written either way is matched, and `$shut` is the three numbers of the first line as the transcript printed them.

### A lamp that lights

An invented lamp stands in a region with a script that lights it on a touch and puts it out on the next one. The lamp is the root; its shade is the second prim of the linkset and carries the point light. The script sets `PRIM_POINT_LIGHT` on the shade, `<1.0, 0.8, 0.4>` at full intensity and a radius of 8, and `PRIM_PROJECTOR` with a field of view of 1.2 radians. It starts off, and the next touch switches both off.

```slate
slate 1

object lamp is "Example Lamp"

expect light lamp link 2 is off within 5s
expect projector lamp link 2 is off within 5s
expect light lamp link 2 is any within 100ms as $before

touch lamp link 2 anywhere
expect light lamp link 2 becomes on within 5s
expect light lamp link 2 colour is 1 0.8 0.4 within 5s
expect light lamp link 2 intensity is 1 within 5s
expect light lamp link 2 radius is 8 within 5s
expect projector lamp link 2 fov becomes 1.2 near 0.01 within 5s
expect no light lamp link 2 becomes off within 1s

touch lamp link 2 anywhere
expect light lamp link 2 becomes $before within 5s
expect projector lamp link 2 becomes off within 5s
```

`0.8` and `0.4` are what the script wrote, and the region sends them as the bytes 204 and 102, which read back as the same two numbers within a step of the byte. `$before` is the reading of the first light line, which is the lamp off. The radius is the float32 8, so it is compared exactly, and the field of view is one a script works out from degrees, so it takes a `near`.

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

### Listening to what a product says

To see what a product says when it is touched, touch it and wait. There is nothing to expect and nobody to name: a wait prints every line the run hears while it lasts ([Wait](#stimuli)).

```slate
slate 1

object hud is "Test HUD"

touch hud button text "Scan"
wait 6s
```

The HUD, the script and this transcript are invented: the HUD answers its owner, complains on the debug channel, and a lamp nearby says something in public. Each line is stamped with when it came, and the step's own `pass` line is printed when the wait is over.

```text
slate: test "listen"
slate: pass step 1
10:20:31.402 chat owner from hud: "scanning"
10:20:33.118 chat debug from hud: "Test HUD: script run-time warning, cache is cold"
10:20:34.950 chat public from [Object] Example Lamp: "flicker"
10:20:35.427 chat owner from hud: "done, 3 found"
slate: pass step 2
slate: pass test "listen"
slate: passed 1 tests
```

The first `pass` is the touch's. A test that then wants to hold the product to what it said turns the lines it read into expectations, `expect say "done, 3 found" on owner from object hud within 8s` after the touch, and leaves the wait out.

### A channel the viewer does not deliver

`listen 1` is what lets the bridge hear it. `on 1` is not public chat; the bridge forwards the line and the runner matches the raw tail.

```slate
slate 1

object vendor is "Example Tip Jar"
listen 1

say "ping" on 1 as tester
expect say "pong" on 1 from object vendor within 1s
```


### An item dropped into an object, and a texture onto a face

A product that reacts to what is put into it or onto it. The first test drops the item in and expects the product's word that its contents changed; the second drops the same item onto face 2 and expects the product's word about the face. The `item` header names it once for both. After each test the runner takes out the copy it put in and sets face 2 back to the texture it had, and prints a line for each (`slate: removed "Example Red Swatch" from box`, `slate: restored panel face 2 to texture ...`), so the next run begins as this one did.

```slate
slate 1

object box is "Example Box"
object panel is "Example Panel"
item swatch is "Example Red Swatch" in "Objects"

test "an item dropped in is noticed" {
  drop swatch into box
  expect say "got it" on public from object box within 5s
}

test "a texture dropped on a face is noticed" {
  drop swatch onto panel face 2
  expect say "painted" on public from object panel within 5s
}
```

If the tester may copy the texture but not give it away, the second test puts a copy into `panel` first, as a viewer does, and the runner removes that copy at the end of the test too. A texture the tester may not copy fails the step: `slate: step 1: "Example Red Swatch" may not be copied, ...`.

### A second avatar in a group

A product that lets a visitor in only while the visitor acts as the product's group. `group visitor "Example Group"` makes the second avatar's active group that one and returns when the simulator agrees; the touch is then the visitor's. A second test sets no group: `none` is the avatar acting as nobody. At the end of the run the visitor is put back to the group it had when the run began, and the transcript says `slate: cleanup: put the group of visitor back to what it was`. The run is given the avatar as any second avatar is (`--avatar visitor=example-two`).

```slate
slate 1

object panel is "Example Panel"
avatar visitor

test "a member is let in" {
  group visitor "Example Group"
  touch panel anywhere as visitor
  expect say "welcome" on public from object panel within 5s
}

test "an avatar with no group is turned away" {
  group visitor none
  touch panel anywhere as visitor
  expect say "members only" on public from object panel within 5s
}
```

## Known limitations

Each is discussed under [Open questions](slate-runner.md#open-questions).

- One binding is one prim found by its name, and told apart from objects of the same name only by a description. It cannot be qualified by owner or by distance, or resolved to a root.
- `image` and `oval` buttons are accepted and always fail, in a touch and in a button reading.
- The finder reads labels in the style it was tuned on, dark type on a lighter button: a word inside a dark outlined frame is not read, and a solid rectangle is not a `box` ([Limits found drawing test pictures for buttons](imgfind.md#limits-found-drawing-test-pictures-for-buttons)).
- A button reading is the finder's count. A label the finder never reads is a count of 0, which is why `becomes gone` is the form to write. A planar or animated face is no reading.
- A guarded touch takes no `button N`: two tuples fail the step, and it is not a way to choose one.
- `wear` and `rez` take an item from a top-level folder by name. The runner does not take off what a test wore.
- `drop` takes an item from a top-level folder by name, as `wear` and `rez` do, and is the tester's alone. It refuses an item the tester may not copy, since the drop would move it out of inventory and the run could not give it back. A non-owner's drop into an object that has `llAllowInventoryDrop` on, which a product reacts to with `CHANGED_ALLOWED_DROP`, is a later extension: `drop ... as NAME` is a static error today. The copy a drop made is found by its id once it shows in the object's contents, and the step fails when it does not show, which includes a product that takes a dropped item out again faster than the contents are read.
- `drop ... onto` sets a texture on one face, and only a plain texture: a drop onto a face that has a PBR material is not modelled. Putting the face's texture back at the end of a test is itself a texture change.
- `group` sets a second avatar's active group and nothing else about it: not the group's title, not the avatar's role. The tester's group is never set. A group is put back at the end of the run, not between tests, and a put back that fails is a warning only.
- A worn HUD's position and size are read (`expect position`, `expect size`), as an offset in the HUD's own frame and a scale. Where it is on the screen is not: an expectation such as `expect position hud on screen at X Y` is not built, and the author works the offset out from the drag and the world view's height, as [the worked example](#move-and-resize-a-hud-by-its-glass) does.
- `touch ... button` does not use `Pick` yet: a button hidden behind another prim of a worn HUD is not reported as hidden, because the finder reads the picture of the face and does not ask what the viewer would press at that point.
- An object has no script dropped into it unless a `linkmap` header names it, because the drop and the script's removal are two `CHANGED_INVENTORY` events for the product under test. Without one, `link N` keeps the packet reading, which the transcript says once for the object ("the store's best reading from the packets"). A binding made by `wear`, `rez` or `expect rez` cannot be named by a `linkmap` and always keeps it.
- A child prim is named with `link N`, whose numbers come from the object store when there is no probe, made certain at setup by the object's own script when a `linkmap` header asks for it. The store numbers a set's children in the order the region sent them, by the packets that listed them, and says the order is known when the root was described and no child came as an answer to a request ([When a set is known](objects.md#when-a-set-is-known)); it is the store's best reading, since the root does not say how many children it has. It cannot give the order of a set that several prims joined in one update while it watched, one it took from another agent, or one with a child that was an answer to a request, and `link N` on such a set fails with `the link order of "<name>" is not known; bind the prim by its own name instead of link N, give the object a probe (the tester must own it), or take it and rez or wear it again`. A probe gives the order, but only in a product the tester owns; binding the prim by its own name needs no order; and taking the object and rezzing or wearing it again makes the store know it ([Link numbers](objects.md#link-numbers)). `slsh objects --how -c NAME` shows how each prim was described. A daemon older than the link numbers reports every order as not known. The link messages `send` and `expect link` always need a probe.
- The tester is driven, and a second avatar the run is given; no more is chosen for it. A second avatar's chat is heard by the tester and not by itself, so it cannot be asked what it heard, and its inventory and attachments are not read. Any wait is capped at 120 s.
- The bridge item is made once where the tester may build (`slate -make-bridge`).
- `face all` has no face count for a sculpt, a mesh, or a prim nothing has described. For every other prim the count comes from its shape ([How many faces a prim has](objects.md#how-many-faces-a-prim-has)), but a sculpt or a mesh sends no shape that gives one. The tuple then runs to the last face that differs from the entry's default and takes one more, the default itself, so a sculpt whose faces are split between two values, neither of them the default, can read with a phantom default face at the end, and `face all is X` then fails although every real face is X. The transcript says `slate: step N: the face count of "<name>" is not known (sculpt, mesh or not described); face all reads the faces its texture entry names`, once for each name in a test.
- `touch ... showing` has the same blindness for a sculpt or a mesh. A face that the entry does not name shows the entry's default texture, and the search decodes faces only as far as the last one the entry names (the same note is printed). Asking for the default texture can find several faces, so that the step fails as ambiguous, or none. Ask for a texture that a script put on a face.
