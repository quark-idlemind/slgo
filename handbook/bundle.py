#!/usr/bin/env python3
"""Fold the handbook into one self-contained HTML file.

    python3 handbook/bundle.py [OUT]      (default: handbook-bundle.html here)

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
    "bot-llm", "how-llm", "sl-host", "setups", "platforms", "security",
    "troubleshooting", "reference",
]

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
        sys.exit("%s.html: a link the bundle cannot follow: %s" % (page, target))

    return re.sub(r'\b(href)="([^"]*)"', link, html)


def main():
    out = sys.argv[1] if len(sys.argv) > 1 else os.path.join(HERE, "handbook-bundle.html")
    css = open(os.path.join(HERE, "handbook.css")).read()
    js = open(os.path.join(HERE, "handbook.js")).read()
    parts = []
    for page in PAGES:
        text = open(os.path.join(HERE, page + ".html")).read()
        found = ARTICLE.findall(text)
        if len(found) != 1:
            sys.exit("%s.html: want one <article class=\"page\">, found %d" % (page, len(found)))
        parts.append(rewrite(page, found[0]))
    doc = (
        '<title>slgo handbook</title>\n'
        '<meta name="description" content="Setting up slgod, slsh, slbotd and sl-host, on one machine or several.">\n'
        "<style>\n" + css + "</style>\n"
        + "\n".join(parts)
        + "\n<script>\n" + js.replace("</script", "<\\/script") + "</script>\n"
    )
    with open(out, "w") as f:
        f.write(doc)
    print(out)


if __name__ == "__main__":
    main()
