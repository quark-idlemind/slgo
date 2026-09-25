#!/usr/bin/env python3
"""The Go packages' documentation, as pages of the one-file handbook.

    python3 handbook/apidoc.py DIR     write each package's page to DIR,
                                       to look at on its own

bundle.py calls build() and puts what it returns after the handbook's
own pages, so that one file holds the handbook and the documentation of
every package in the module.

Nothing here renders documentation.  `go doc -http` runs pkgsite, the
program behind pkg.go.dev, against this checkout; this asks it for each
package's page and keeps the part pkg.go.dev shows as Documentation --
the same markup, from the same source comments -- and changes only where
it points:

    another package here          its page in the bundle
    the standard library, and     pkg.go.dev, where they are public
      modules this one uses
    a declaration's source        the file on GitHub, at this commit
    a built-in type               nothing: `string` needs no link

The repository is private, so pkg.go.dev has none of this, and that is
the reason to carry it.

The first run builds pkgsite, at the version this Go toolchain names,
into the build cache, which needs the module proxy.  After that it is a
few seconds.
"""

import contextlib
import json
import os
import re
import shutil
import signal
import subprocess
import sys
import threading
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
REPO = "https://github.com/quark-idlemind/slgo"

# Declarations left out, by package, and the file they are in.  msg's
# message structures are generated from Linden Lab's message template:
# nearly fourteen hundred structs, each a line of documentation and the
# same three methods, which is more than half of everything here and
# says less than the file does.  A link to one goes to its line there.
GENERATED = {"msg": "msg/messages_gen.go"}

OPEN = '<div class="Documentation-content js-docContent">'


def go(*args):
    r = subprocess.run(["go"] + list(args), cwd=ROOT, capture_output=True, text=True)
    if r.returncode != 0:
        sys.exit("go %s: %s" % (" ".join(args), r.stderr.strip()))
    return r.stdout


def packages():
    """Every package in the module, in go list's order: its import path,
    name, and the first sentence of its documentation."""
    out, pkgs, dec, i = go("list", "-json", "./..."), [], json.JSONDecoder(), 0
    while True:
        while i < len(out) and out[i].isspace():
            i += 1
        if i >= len(out):
            return pkgs
        p, i = dec.raw_decode(out, i)
        pkgs.append(p)


@contextlib.contextmanager
def pkgsite(first):
    """go doc -http, running, and where it is listening.

    It serves the documentation of the module it is started in, and
    opens a browser at the package named -- which is the one thing not
    wanted, so $BROWSER is a program that succeeds and does nothing.
    It says where it is listening on stderr."""
    env = dict(os.environ, BROWSER=shutil.which("true") or "/usr/bin/true")
    p = subprocess.Popen(["go", "doc", "-http", first], cwd=ROOT, env=env,
                         stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                         stderr=subprocess.PIPE, text=True, start_new_session=True)

    def stop(sig):
        try:
            os.killpg(p.pid, sig)
        except ProcessLookupError:
            pass

    # A first run builds pkgsite and may fetch it; ten minutes is plenty,
    # and a wait with no end is not.
    timer = threading.Timer(600, stop, [signal.SIGKILL])
    timer.start()
    said, addr = [], None
    try:
        for line in p.stderr:
            said.append(line)
            m = re.search(r"listening on addr (http://\S+)", line)
            if m:
                addr = m.group(1).rstrip("/")
                break
        timer.cancel()
        if not addr:
            sys.exit("go doc -http did not start:\n" + "".join(said[-20:]))
        # Keep reading what it says, so that a full pipe never stops it.
        threading.Thread(target=lambda: [None for _ in p.stderr], daemon=True).start()
        yield addr
    finally:
        timer.cancel()
        stop(signal.SIGINT)
        try:
            p.wait(10)
        except subprocess.TimeoutExpired:
            stop(signal.SIGKILL)
            p.wait()


def element(s, at, tag):
    """The end of the element that opens at s[at], counting nested ones
    of the same tag."""
    depth = 0
    for m in re.finditer(r"<(/?)%s\b[^>]*>" % tag, s[at:]):
        depth += -1 if m.group(1) else 1
        if depth == 0:
            return at + m.end()
    raise ValueError("unclosed <%s>" % tag)


def documentation(page):
    """The Documentation part of a pkgsite page, without its wrapper."""
    a = page.find(OPEN)
    if a < 0:
        return ""
    return page[a + len(OPEN):element(page, a, "div") - len("</div>")]


class Module:
    def __init__(self, path, commit, pkgs):
        self.path = path
        self.blob = "%s/blob/%s/" % (REPO, commit or "main")
        self.ids = {self.rel(p["ImportPath"]): self.page_id(p["ImportPath"]) for p in pkgs}
        # Where each declaration left out is: package -> id -> "file#Lnn".
        self.left_out = {}

    def rel(self, path):
        return path[len(self.path) + 1:] if path != self.path else ""

    def page_id(self, path):
        return "pkg-" + self.rel(path).replace("/", "-")

    def link(self, rel, frag, here):
        """Where a link to another package of this module goes."""
        name = frag[1:] if frag else ""
        if name in self.left_out.get(rel, {}):
            return self.blob + self.left_out[rel][name]
        if rel == here:
            return frag or "#pkg-overview"
        if rel not in self.ids:
            return "api.html"
        return self.ids[rel] + ".html" + frag


def leave_out(doc, file):
    """doc without the declarations from file, and where each of those
    was, by id.  Types only: their methods go with them, since pkgsite
    puts a type's methods inside its block."""
    gone, out, at = {}, [], 0
    for m in re.finditer(r'<div class="Documentation-type">', doc):
        if m.start() < at:
            continue
        end = element(doc, m.start(), "div")
        block = doc[m.start():end]
        src = re.search(r'data-src="(%s#L\d+)"' % re.escape(file), block[:2000])
        if src:
            name = re.search(r'\bid="([^"]+)"', block).group(1)
            gone[name] = src.group(1)
            out.append(doc[at:m.start()])
            at = end
    out.append(doc[at:])
    doc = "".join(out)

    # And their lines in the index.  A type's methods are listed in an
    # item of their own after it, so an item goes when everything it
    # links to was left out, or is a method of something that was.
    def dropped(li):
        links = re.findall(r'href="#([^"]+)"', li)
        return links and all(x.split(".")[0] in gone for x in links)
    out, at = [], 0
    for m in re.finditer(r"<li\b[^>]*>", doc):
        if m.start() < at:
            continue
        end = element(doc, m.start(), "li")
        out.append(doc[at:m.start()])
        if not dropped(doc[m.start():end]):
            out.append(doc[m.start():end])
        at = end
    out.append(doc[at:])
    return "".join(out), gone


def tidy(doc, pkg, mod):
    """pkgsite's markup, pointed at the bundle and shaped like a handbook
    page: each part a section of main with an h2, so that the page's
    contents list and the search find them."""
    here = mod.rel(pkg["ImportPath"])
    mine = re.escape(mod.path)

    # Its source links name the file on this disk.  They become the file
    # in the repository, finished by the script once the page is shown:
    # there are thousands, and the address is the same up to the file.
    doc = re.sub(r'href="/files/[^"]*?/%s/([^"]+)"' % mine, r'data-src="\1"', doc)

    def module_link(m):
        rel = (m.group(1) or "").lstrip("/")
        return 'href="%s"' % mod.link(rel, m.group(2) or "", here)
    doc = re.sub(r'href="/%s(?:@[^/"#]*)?(/[^"#]*)?(#[^"]*)?"' % mine, module_link, doc)
    doc = re.sub(r'<a href="/builtin(?:#\w+)?">(\w+)</a>', r"\1", doc)
    # A declaration left out of this very page is linked as "#Name".
    gone = mod.left_out.get(here, {})
    doc = re.sub(r'href="#([^"]+)"', lambda m: 'href="%s"' % (mod.blob + gone[m.group(1)])
                 if m.group(1) in gone else m.group(0), doc)
    doc = re.sub(r'href="/', 'href="https://pkg.go.dev/', doc)

    # What the script puts back for the page being read, or nobody needs.
    doc = re.sub(r'\s*<a\b[^>]*>¶</a>', "", doc)
    doc = re.sub(r'<span class="Documentation-sinceVersion">\s*</span>', "", doc)
    doc = re.sub(r' tabindex="-1"', "", doc)
    doc = re.sub(r' title="Go to [^"]*"', "", doc)

    # A section per part, headed h2; a type or function is then an h3
    # and a method an h4, and a heading in the package's own words an h3.
    # The overview and index carry their heading inside the section, the
    # later parts just before it.
    def part(kind, ident, title):
        title = re.sub(r"<[^>]+>", "", title).strip()
        return '<section id="%s" class="Documentation-%s"><h2>%s</h2>' % (ident, kind, title)
    doc = re.sub(r'<section class="Documentation-(\w+)">\s*<h3[^>]*\bid="([^"]+)"[^>]*>(.*?)</h3>',
                 lambda m: part(m.group(1), m.group(2), m.group(3)), doc, flags=re.S)
    doc = re.sub(r'<h3[^>]*\bid="([^"]+)"[^>]*\bclass="Documentation-(\w+)Header"[^>]*>(.*?)</h3>\s*'
                 r'<section class="Documentation-\2">',
                 lambda m: part(m.group(2), m.group(1), m.group(3)), doc, flags=re.S)
    doc = re.sub(r'<h4([^>]*class="Documentation-(?:typeHeader|functionHeader)"[^>]*)>(.*?)</h4>',
                 r"<h3\1>\2</h3>", doc, flags=re.S)
    doc = re.sub(r'<h4( id="hdr-[^"]*"[^>]*)>(.*?)</h4>', r"<h3\1>\2</h3>", doc, flags=re.S)

    # Space between tags, but none inside a <pre>, where it is the code.
    parts = re.split(r"(<pre\b.*?</pre>)", doc, flags=re.S)
    return "".join(p if p.startswith("<pre") else re.sub(r">\s+<", "><", re.sub(r"\n\s+", "\n", p))
                   for p in parts).strip()


def kind(pkg, rel):
    if rel.startswith("examples/"):
        return "Example program"
    if pkg["Name"] == "main":
        return "Command"
    if rel.startswith("internal/") or "/internal/" in rel:
        return "Internal package"
    return "Go package"


def esc(s):
    return s.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;").replace('"', "&quot;")


def article(pkg, doc, mod, prev, nxt, note=""):
    path = pkg["ImportPath"]
    rel = mod.rel(path)
    k = kind(pkg, rel)
    if k == "Go package":
        meta = '<span class="pill api-import"><code>import "%s"</code></span>' % esc(path)
    elif k == "Internal package":
        meta = '<span class="pill">only this module can import it</span>'
    else:
        meta = '<span class="pill"><code>go install ./%s</code></span>' % esc(rel)
    pager = '<a href="api.html">↑ Go packages</a>'
    if prev:
        pager += ' <a href="%s.html">← %s</a>' % (mod.page_id(prev["ImportPath"]), esc(mod.rel(prev["ImportPath"])))
    if nxt:
        pager += ' <a href="%s.html">%s →</a>' % (mod.page_id(nxt["ImportPath"]), esc(mod.rel(nxt["ImportPath"])))
    if not doc:
        doc = '<section id="pkg-overview"><h2>Overview</h2><p>This package has no documentation.</p></section>'
    return (
        '<article class="page api-pkg" id="%(id)s" data-parent="api" data-label="%(k)s %(rel)s" '
        'data-title="%(rel)s — Go packages — slgo handbook" data-src-base="%(blob)s">\n'
        '<div class="wrap">\n'
        '  <header class="masthead">\n'
        '    <p class="eyebrow">%(k)s</p>\n'
        '    <h1>%(rel)s</h1>\n'
        '    <p class="standfirst">%(syn)s</p>%(note)s\n'
        '    <div class="meta-row">%(meta)s</div>\n'
        '  </header>\n'
        '  <div class="body-grid">\n'
        '    <nav class="rail" aria-label="On this page"></nav>\n'
        '    <main class="api-doc">\n%(doc)s\n    </main>\n'
        '  </div>\n'
        '  <footer>\n'
        '    <span>slgo handbook · for <span class="hb-version">the slgo in this checkout</span> · from <code>go doc</code></span>\n'
        '    <span class="pager">%(pager)s</span>\n'
        '  </footer>\n'
        '</div>\n'
        '</article>'
    ) % {"id": mod.page_id(path), "k": k, "rel": esc(rel), "blob": esc(mod.blob), "syn": esc(pkg.get("Doc", "")),
         "note": note, "meta": meta, "doc": doc, "pager": pager}


# The index page's groups, in the order they are shown.
GROUPS = [
    ("packages", "Packages", "What a program of your own imports. Most need only <code>sl</code>, "
     "which uses the rest; <code>msg</code> has the types its methods take, such as "
     "<code>UUID</code> and <code>Vector3</code>."),
    ("commands", "Commands", "The programs, documented in their own words. How to run them is in the "
     "rest of this handbook."),
    ("internal", "Internal packages", "Shared by the commands. A program outside this module cannot "
     "import them, and they may change without notice."),
]


def group(pkg, rel):
    k = kind(pkg, rel)
    return {"Go package": "packages", "Internal package": "internal"}.get(k, "commands")


def index(pkgs, mod):
    """The sections of the Go packages page that list every package."""
    out = []
    for key, title, words in GROUPS:
        rows = [p for p in pkgs if group(p, mod.rel(p["ImportPath"])) == key]
        if not rows:
            continue
        out.append('<section id="%s"><h2>%s</h2><p>%s</p><div class="tablewrap"><table>'
                   '<thead><tr><th>Package</th><th>What it is</th></tr></thead><tbody>' % (key, title, words))
        for p in rows:
            rel = mod.rel(p["ImportPath"])
            said = esc(p.get("Doc", "")) or '<span class="api-none">No package comment.</span>'
            out.append('<tr><td><a href="%s.html"><code>%s</code></a></td><td class="what">%s</td></tr>'
                       % (mod.page_id(p["ImportPath"]), esc(rel), said))
        out.append("</tbody></table></div></section>")
    return "\n".join(out)


def order(pkgs, mod):
    """Packages, then commands, then internal packages, each by path:
    the order of the index, and of the pages' pagers."""
    keys = [k for k, _, _ in GROUPS]
    return sorted(pkgs, key=lambda p: (keys.index(group(p, mod.rel(p["ImportPath"]))), p["ImportPath"]))


def build(commit):
    """(the Go packages page's sections, [(page id, article)...])."""
    mod_path = go("list", "-m").strip()
    pkgs = packages()
    mod = Module(mod_path, commit, pkgs)
    pkgs = order(pkgs, mod)

    pages = {}
    with pkgsite(pkgs[0]["ImportPath"]) as addr:
        for p in pkgs:
            with urllib.request.urlopen("%s/%s" % (addr, p["ImportPath"]), timeout=120) as r:
                pages[p["ImportPath"]] = r.read().decode("utf-8")

    # What is left out first, so that every page's links to it can go to
    # the source instead.
    docs, notes = {}, {}
    for p in pkgs:
        rel = mod.rel(p["ImportPath"])
        doc = documentation(pages[p["ImportPath"]])
        doc = re.sub(r'href="/files/[^"]*?/%s/([^"]+)"' % re.escape(mod.path), r'data-src="\1"', doc)
        if rel in GENERATED:
            doc, gone = leave_out(doc, GENERATED[rel])
            mod.left_out[rel] = gone
            notes[rel] = ('\n    <p class="api-note">The %d message types generated from Linden Lab\'s message '
                          'template are left out, with their methods: they are in <a href="%s%s">%s</a>, '
                          'one struct per message. A link to one goes to its line there.</p>'
                          % (len(gone), esc(mod.blob), GENERATED[rel], GENERATED[rel]))
        docs[p["ImportPath"]] = doc

    arts = []
    for i, p in enumerate(pkgs):
        rel = mod.rel(p["ImportPath"])
        doc = tidy(docs[p["ImportPath"]], p, mod)
        arts.append((mod.page_id(p["ImportPath"]),
                     article(p, doc, mod, pkgs[i - 1] if i else None,
                             pkgs[i + 1] if i + 1 < len(pkgs) else None, notes.get(rel, ""))))
    return index(pkgs, mod), arts


def main():
    if len(sys.argv) != 2:
        sys.exit(__doc__.strip().split("\n\n")[1])
    out = sys.argv[1]
    os.makedirs(out, exist_ok=True)
    listing, arts = build(None)
    with open(os.path.join(out, "index.html"), "w") as f:
        f.write(listing)
    for name, html in arts:
        with open(os.path.join(out, name + ".html"), "w") as f:
            f.write(html)
    print("%d packages in %s" % (len(arts), out))


if __name__ == "__main__":
    main()
