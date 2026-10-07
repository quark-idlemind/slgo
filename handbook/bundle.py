#!/usr/bin/env python3
"""Fold the handbook into one self-contained HTML file.

    python3 handbook/bundle.py [OUT]      (default: handbook-bundle.html here)
    python3 handbook/bundle.py --page NAME [OUT]   one page on its own
                                          (default: NAME-bundle.html here)
    python3 handbook/bundle.py --no-api [OUT]      without the Go packages

The one file also carries the documentation of every Go package in the
module, a page each, made by go doc from the source as it is built; see
apidoc.py.  That needs Go, and the first time it needs the network.

Every page is a file of its own with one <article class="page" id=NAME>.
The bundle holds every article, the stylesheet and the script inline;
handbook.js sees more than one article and shows one at a time.  Ids
inside a page become NAME--id so that two pages may use the same one,
and links between pages become #anchors:

    other.html          ->  #other
    other.html#part     ->  #other--part
    #part  (in NAME)    ->  #NAME--part

It also says which slgo the handbook is for, where the pages leave room
for it (class="hb-version"), in the words the programs' --version uses.
Read from a checkout, a page says it is for the slgo in that checkout.
"""

import html
import os
import re
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
# apidoc.py is beside this, and importing it would otherwise leave its
# compiled copy in the tree, which is one thing never to commit.
sys.dont_write_bytecode = True
sys.path.insert(0, HERE)
import apidoc  # noqa: E402

PAGES = [
    "index", "quickstart-local", "quickstart-remote", "quickstart-bot",
    "bot-llm", "slsh-guide", "how-llm", "own-bot", "api", "testing",
    "testing-recipes", "testable-products", "setups",
    "sl-host", "platforms", "security", "troubleshooting", "reference",
]

# What a link may name as page.html: the pages, and in the one file the
# Go packages' pages as well, which main adds.
KNOWN = set(PAGES)

# A relative link to a file that is not a page (the guide's link to
# doc/guide.md) has nowhere to go in one file, so it goes to the tree.
TREE = "https://github.com/quark-idlemind/slgo/blob/main/"

ARTICLE = re.compile(r'<article class="page"[^>]*>.*?</article>', re.S)


def rewrite(page, html):
    def ident(v):
        return v if v == page else page + "--" + v

    html = re.sub(r'\b(id|for)="([^"]+)"', lambda m: '%s="%s"' % (m.group(1), ident(m.group(2))), html)
    html = re.sub(r'\b(aria-labelledby|aria-describedby|aria-controls)="([^"]+)"',
                  lambda m: '%s="%s"' % (m.group(1), " ".join(ident(x) for x in m.group(2).split())), html)
    html = re.sub(r'url\(#([^)]+)\)', lambda m: "url(#%s)" % ident(m.group(1)), html)

    def link(m):
        target = m.group(2)
        if re.match(r"^[a-z]+:", target):
            return m.group(0)
        if target.startswith("#"):
            return '%s="#%s"' % (m.group(1), ident(target[1:]))
        f = re.match(r"^([a-z0-9-]+)\.html(?:#(.+))?$", target)
        if f and f.group(1) in KNOWN:
            other = f.group(1)
            return '%s="#%s"' % (m.group(1), other + ("--" + f.group(2) if f.group(2) else ""))
        if re.match(r"^[a-z0-9_./-]+$", target) and not target.startswith("/"):
            where = os.path.basename(HERE)
            return '%s="%s"' % (m.group(1), TREE + os.path.normpath(os.path.join(where, target)))
        sys.exit("%s.html: a link the bundle cannot follow: %s" % (page, target))

    return re.sub(r'\b(href)="([^"]*)"', link, html)


def edition():
    """Which slgo this build of the handbook is for, as internal/version
    says it for a program built from the same tree:

        slgo v0.7.0 (2026-09-25) fc8dfda
        slgo development build after v0.7.0 (2026-09-26) 1a2b3c4 modified

    The release is the tag on this commit, or else the last one before
    it; the date is the commit's, in UTC as the Go toolchain records it;
    "modified" means the tracked files have edits the commit does not.
    None outside a git checkout, and then the pages say what they say."""
    def git(*args):
        env = dict(os.environ, TZ="UTC")
        r = subprocess.run(["git", "-C", HERE] + list(args), capture_output=True, text=True, env=env)
        return r.stdout.strip() if r.returncode == 0 else None
    commit = git("rev-parse", "--short=7", "HEAD")
    if not commit:
        return None
    release = git("describe", "--tags", "--exact-match", "--match", "v[0-9]*", "HEAD")
    if not release:
        last = git("describe", "--tags", "--abbrev=0", "--match", "v[0-9]*", "HEAD")
        release = "development build after " + last if last else "development build"
    date = git("log", "-1", "--format=%cd", "--date=format-local:%Y-%m-%d") or "unknown"
    line = "slgo %s (%s) %s" % (release, date, commit)
    if git("status", "--porcelain", "--untracked-files=no"):
        line += " modified"
    return line


def commit():
    """The commit this is built from, in full, or None outside git."""
    r = subprocess.run(["git", "-C", HERE, "rev-parse", "HEAD"], capture_output=True, text=True)
    return r.stdout.strip() if r.returncode == 0 else None


def check_links(doc):
    """Every #anchor in the one file names something in it.  A link that
    goes nowhere in one file does nothing when clicked, which nobody
    reports; the Go packages' pages alone carry thousands of links."""
    ids = set(re.findall(r'\bid="([^"]+)"', doc))
    broken = sorted({t for t in re.findall(r'\bhref="#([^"]*)"', doc) if t and t not in ids})
    if broken:
        sys.exit("%d links go nowhere in the bundle, among them:\n  %s"
                 % (len(broken), "\n  ".join(broken[:15])))


def check_order():
    """PAGES is the reading order, and two other lists repeat it: the
    script's, which makes the site bar's menus, and the footers' pager
    links, which are written into each page so that it works on its own.
    A page moved in one and not the others sends the reader sideways."""
    js = open(os.path.join(HERE, "handbook.js")).read()
    listed = re.findall(r'\{ id: "([a-z0-9-]+)"', js)
    if listed != PAGES:
        sys.exit("handbook.js lists the pages as:\n  %s\nbundle.py as:\n  %s"
                 % (" ".join(listed), " ".join(PAGES)))
    for i, page in enumerate(PAGES):
        text = open(os.path.join(HERE, page + ".html")).read()
        m = re.search(r'<span class="pager">(.*?)</span>\s*</footer>', text, re.S)
        if not m:
            sys.exit("%s.html: no pager at the end of its footer" % page)
        got = re.findall(r'href="([a-z0-9-]+)\.html"', m.group(1))
        want = ([PAGES[i - 1]] if i else []) + [PAGES[(i + 1) % len(PAGES)]]
        if got != want:
            sys.exit("%s.html: the pager goes to %s; the page order says %s"
                     % (page, " and ".join(got) or "nowhere", " and ".join(want)))


def check_excerpts():
    """A block marked data-from="PATH" is a copy of part of that file,
    PATH relative to this directory: the "Your own bot" page quotes the
    greeter in examples/ that way, function by function.  The program
    is compiled and tested with the tree and the page is not, so this is
    what notices the day they part: every such block, with its markup
    taken out, has to appear in the file exactly as it stands."""
    for page in PAGES:
        text = open(os.path.join(HERE, page + ".html")).read()
        for m in re.finditer(r'<div class="file" data-from="([^"]+)">.*?<pre>(.*?)</pre>', text, re.S):
            path = os.path.normpath(os.path.join(HERE, m.group(1)))
            try:
                source = open(path).read()
            except OSError as e:
                sys.exit("%s.html: quotes %s, which cannot be read: %s" % (page, m.group(1), e))
            code = html.unescape(re.sub(r"<[^>]+>", "", m.group(2)))
            if code not in source:
                first = code.split("\n", 1)[0]
                sys.exit("%s.html: the copy of %s that begins\n  %s\nno longer matches the file"
                         % (page, m.group(1), first))


def main():
    check_order()
    check_excerpts()
    args = sys.argv[1:]
    only = None
    api = True
    if "--no-api" in args:
        args.remove("--no-api")
        api = False
    if args[:1] == ["--page"]:
        if len(args) < 2 or args[1] not in PAGES:
            sys.exit("--page wants one of: " + " ".join(PAGES))
        only, args = args[1], args[2:]
    out = args[0] if args else os.path.join(HERE, (only or "handbook") + "-bundle.html")
    css = open(os.path.join(HERE, "handbook.css")).read()
    js = open(os.path.join(HERE, "handbook.js")).read()
    parts = []
    # The file is UTF-8 and says so.  Without this a browser opening it
    # from disk may read it as Latin-1, and every arrow, dash and dot in
    # it arrives as two or three letters of nonsense.  First, because a
    # browser looks for it only near the top.
    charset = '<meta charset="utf-8">\n'
    head = ('<title>slgo handbook</title>\n'
            '<meta name="description" content="Setting up slgod, slsh, slbotd and sl-host, on one machine or several.">\n')
    # The Go packages first, since the page that lists them is one of
    # the pages, and their names are names a link may use.
    listing, packages = None, []
    if api and not only:
        listing, packages = apidoc.build(commit())
        KNOWN.update(name for name, _ in packages)
    for page in ([only] if only else PAGES):
        text = open(os.path.join(HERE, page + ".html")).read()
        if listing is not None:
            # What a checkout says in their place, replaced by the list.
            text, n = re.subn(r'<section id="packages" data-generated="api">.*?</section>',
                              lambda m: listing, text, flags=re.S)
            if page == "api" and n != 1:
                sys.exit("api.html: no <section data-generated=\"api\"> to put the packages in")
        found = ARTICLE.findall(text)
        if len(found) != 1:
            sys.exit("%s.html: want one <article class=\"page\">, found %d" % (page, len(found)))
        article = rewrite(page, found[0])
        if only:
            # Alone in its file: the script shows no links to other pages,
            # and the page keeps its own title.
            article = article.replace('<article class="page"', '<article class="page" data-alone="1"', 1)
            title = re.search(r"<title>(.*?)</title>", text, re.S)
            head = "<title>%s</title>\n" % (title.group(1).strip() if title else page)
        parts.append(article)
    for name, article in packages:
        parts.append(rewrite(name, article))
    doc = (
        charset + head
        + "<style>\n" + css + "</style>\n"
        + "\n".join(parts)
        + "\n<script>\n" + js.replace("</script", "<\\/script") + "</script>\n"
    )
    stamp = edition()
    if stamp:
        # A browser breaks a line after a hyphen, and a date broken there
        # reads as two numbers; the date in brackets is kept whole.
        stamp = re.sub(r"\(([0-9-]+|unknown)\)", r'<span class="nowrap">(\1)</span>', stamp)
        doc = re.sub(r'<span class="hb-version">[^<]*</span>',
                     lambda m: '<span class="hb-version">%s</span>' % stamp, doc)
    if not only:
        check_links(doc)
    with open(out, "w") as f:
        f.write(doc)
    print(out)


if __name__ == "__main__":
    main()
