#!/usr/bin/env python3
"""Fold the handbook into one self-contained HTML file.

    python3 handbook/bundle.py [OUT]      (default: handbook-bundle.html here)
    python3 handbook/bundle.py --page NAME [OUT]   one page on its own
                                          (default: NAME-bundle.html here)

Every page is a file of its own with one <article class="page" id=NAME>,
here or, for the slsh guide, in doc/.
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
    "bot-llm", "how-llm", "slsh-guide", "sl-host", "setups", "platforms",
    "security", "troubleshooting", "reference",
]

# Pages that live outside this directory, by the path a handbook page
# links to them with.
ELSEWHERE = {"slsh-guide": "../doc/slsh-guide.html"}

# A relative link to a file that is not a page (the guide's link to
# doc/guide.md) has nowhere to go in one file, so it goes to the tree.
TREE = "https://github.com/quark-idlemind/slgo/blob/main/"

ARTICLE = re.compile(r'<article class="page"[^>]*>.*?</article>', re.S)


def source(page):
    if page in ELSEWHERE:
        return os.path.normpath(os.path.join(HERE, ELSEWHERE[page]))
    return os.path.join(HERE, page + ".html")


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
        f = re.match(r"^(?:\.\./[a-z]+/)?([a-z0-9-]+)\.html(?:#(.+))?$", target)
        if f and f.group(1) in PAGES:
            other = f.group(1)
            return '%s="#%s"' % (m.group(1), other + ("--" + f.group(2) if f.group(2) else ""))
        if re.match(r"^[a-z0-9_./-]+$", target) and not target.startswith("/"):
            where = os.path.dirname(os.path.relpath(source(page), os.path.dirname(HERE)))
            return '%s="%s"' % (m.group(1), TREE + os.path.normpath(os.path.join(where, target)))
        sys.exit("%s.html: a link the bundle cannot follow: %s" % (page, target))

    return re.sub(r'\b(href)="([^"]*)"', link, html)


def main():
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
        text = open(source(page)).read()
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
