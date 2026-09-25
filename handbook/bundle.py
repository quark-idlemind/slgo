#!/usr/bin/env python3
"""Fold the handbook into one self-contained HTML file.

    python3 handbook/bundle.py [OUT]      (default: handbook-bundle.html here)
    python3 handbook/bundle.py --page NAME [OUT]   one page on its own
                                          (default: NAME-bundle.html here)

Every page is a file of its own with one <article class="page" id=NAME>.
The bundle holds every article, the stylesheet and the script inline;
handbook.js sees more than one article and shows one at a time.  Ids
inside a page become NAME--id so that two pages may use the same one,
and links between pages become #anchors:

    other.html          ->  #other
    other.html#part     ->  #other--part
    #part  (in NAME)    ->  #NAME--part
"""

import os
import re
import sys

HERE = os.path.dirname(os.path.abspath(__file__))

PAGES = [
    "index", "quickstart-local", "quickstart-remote", "quickstart-bot",
    "bot-llm", "slsh-guide", "how-llm", "setups", "sl-host", "platforms",
    "security", "troubleshooting", "reference",
]

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
        if f and f.group(1) in PAGES:
            other = f.group(1)
            return '%s="#%s"' % (m.group(1), other + ("--" + f.group(2) if f.group(2) else ""))
        if re.match(r"^[a-z0-9_./-]+$", target) and not target.startswith("/"):
            where = os.path.basename(HERE)
            return '%s="%s"' % (m.group(1), TREE + os.path.normpath(os.path.join(where, target)))
        sys.exit("%s.html: a link the bundle cannot follow: %s" % (page, target))

    return re.sub(r'\b(href)="([^"]*)"', link, html)


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


def main():
    check_order()
    args = sys.argv[1:]
    only = None
    if args[:1] == ["--page"]:
        if len(args) < 2 or args[1] not in PAGES:
            sys.exit("--page wants one of: " + " ".join(PAGES))
        only, args = args[1], args[2:]
    out = args[0] if args else os.path.join(HERE, (only or "handbook") + "-bundle.html")
    css = open(os.path.join(HERE, "handbook.css")).read()
    js = open(os.path.join(HERE, "handbook.js")).read()
    parts = []
    head = ('<title>slgo handbook</title>\n'
            '<meta name="description" content="Setting up slgod, slsh, slbotd and sl-host, on one machine or several.">\n')
    for page in ([only] if only else PAGES):
        text = open(os.path.join(HERE, page + ".html")).read()
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
    doc = (
        head
        + "<style>\n" + css + "</style>\n"
        + "\n".join(parts)
        + "\n<script>\n" + js.replace("</script", "<\\/script") + "</script>\n"
    )
    with open(out, "w") as f:
        f.write(doc)
    print(out)


if __name__ == "__main__":
    main()
