// The slgo handbook: what every page shares.
//
// Each page is one <article class="page" id="NAME"> and works on its own
// from a checkout.  bundle.py folds every page into one file for
// publishing; then all the articles are present, only one shows, and
// links between pages are plain #anchors this script routes.

(function () {
  "use strict";

  var PAGES = [
    { id: "index", title: "Start here" },
    { id: "quickstart-local", title: "One machine" },
    { id: "quickstart-remote", title: "Remote slgod" },
    { id: "quickstart-bot", title: "Adding slbotd" },
    { id: "bot-llm", title: "Bot + LLM" },
    { id: "how-llm", title: "slsh + LLM" },
    { id: "slsh-guide", title: "slsh guide" },
    { id: "sl-host", title: "sl-host" },
    { id: "setups", title: "Setups" },
    { id: "platforms", title: "Linux & macOS" },
    { id: "security", title: "Security" },
    { id: "troubleshooting", title: "Troubleshooting" },
    { id: "reference", title: "Reference" }
  ];

  var root = document.documentElement;
  var articles = Array.prototype.slice.call(document.querySelectorAll("article.page"));
  var bundled = articles.length > 1;
  // One page folded into one file on its own (bundle.py --page): there
  // is nowhere for the site bar's links to go, so it has none.
  var alone = articles.length === 1 && !!articles[0].dataset.alone;

  function store(key, value) {
    try {
      if (value === undefined) return window.localStorage.getItem("slgo-handbook:" + key);
      if (value === null) window.localStorage.removeItem("slgo-handbook:" + key);
      else window.localStorage.setItem("slgo-handbook:" + key, value);
    } catch (e) { return null; }
    return null;
  }

  function hrefFor(id) {
    if (bundled) return "#" + id;
    return id + ".html";
  }

  // The plywood cube every prim starts as: the mark in the status bar.
  var CUBE = '<svg viewBox="0 0 20 22" aria-hidden="true">' +
    '<path d="M10 1 L19 6 L10 11 L1 6 Z" fill="#dcb67c"/>' +
    '<path d="M1 6 L10 11 L10 21 L1 16 Z" fill="#b98a4e"/>' +
    '<path d="M19 6 L10 11 L10 21 L19 16 Z" fill="#8e6635"/></svg>';

  // Second Life Time is the Pacific coast's, and the viewer's status bar
  // shows it.  So does this one.
  function sltClock(el) {
    var fmt;
    try {
      fmt = new Intl.DateTimeFormat("en-US", { timeZone: "America/Los_Angeles", hour: "numeric", minute: "2-digit" });
    } catch (e) { el.hidden = true; return; }
    function tick() { el.innerHTML = "<b>" + esc(fmt.format(new Date())) + "</b> SLT"; }
    tick();
    setInterval(tick, 15000);
  }

  // ------------------------------------------------------ Linux / macOS

  function guessOS() {
    var p = (navigator.userAgentData && navigator.userAgentData.platform) || navigator.platform || "";
    return /mac|iphone|ipad/i.test(p) ? "mac" : "linux";
  }

  // Which systems the reader's machines run: "linux", "mac", or "both"
  // for a setup that mixes them.  Content tagged data-os-only for one
  // system hides when the other alone is chosen; content tagged "both"
  // is for mixed setups and shows only then.  "both" is the state with
  // no attribute, which is also what a page is with no script.
  function setOS(os) {
    if (os === "both") root.removeAttribute("data-os");
    else root.setAttribute("data-os", os);
    store("os", os);
    document.querySelectorAll(".os-toggle:not(.sh-toggle) button").forEach(function (b) {
      b.setAttribute("aria-pressed", String(b.dataset.os === os));
    });
  }

  // No guess for the shell: a browser cannot see it.  Until the reader
  // picks one, every variant shows.
  function setShell(shell) {
    if (shell) root.setAttribute("data-shell", shell);
    else root.removeAttribute("data-shell");
    store("shell", shell || null);
    document.querySelectorAll(".sh-toggle button").forEach(function (b) {
      b.setAttribute("aria-pressed", String(b.dataset.shell === (shell || "")));
    });
  }

  // ------------------------------------------------------------ site bar

  function buildBar() {
    var bar = document.createElement("header");
    bar.className = "sitebar";
    var inner = document.createElement("div");
    inner.className = "sitebar-inner";

    var brand = document.createElement("a");
    brand.className = "brand";
    brand.href = alone ? "#" : hrefFor("index");
    brand.innerHTML = CUBE + "slgo <span>handbook</span>";
    inner.appendChild(brand);

    var menu = document.createElement("button");
    menu.className = "menu-button";
    menu.type = "button";
    menu.textContent = "pages";
    menu.setAttribute("aria-expanded", "false");
    menu.addEventListener("click", function () {
      var open = bar.classList.toggle("open");
      menu.setAttribute("aria-expanded", String(open));
    });

    var nav = document.createElement("nav");
    nav.className = "sitenav";
    nav.setAttribute("aria-label", "Handbook pages");
    PAGES.forEach(function (p) {
      var a = document.createElement("a");
      a.href = hrefFor(p.id);
      a.textContent = p.title;
      a.dataset.page = p.id;
      nav.appendChild(a);
    });
    if (!alone) inner.appendChild(nav);

    var tog = document.createElement("div");
    tog.className = "os-toggle";
    tog.setAttribute("role", "group");
    tog.setAttribute("aria-label", "Show instructions for the systems your machines run");
    tog.innerHTML = '<span class="os-label">OS</span>' +
      '<button type="button" data-os="linux" title="Every machine runs Linux">Linux</button>' +
      '<button type="button" data-os="mac" title="Every machine runs macOS">macOS</button>' +
      '<button type="button" data-os="both" title="Machines of both kinds">both</button>';
    tog.querySelectorAll("button").forEach(function (b) {
      b.addEventListener("click", function () { setOS(b.dataset.os); });
    });
    inner.appendChild(tog);

    var sh = document.createElement("div");
    sh.className = "os-toggle sh-toggle";
    sh.setAttribute("role", "group");
    sh.setAttribute("aria-label", "Show commands for the shell");
    sh.innerHTML = '<span class="os-label">shell</span>' +
      '<button type="button" data-shell="">all</button>' +
      '<button type="button" data-shell="sh" title="sh, bash, dash, ksh">sh</button>' +
      '<button type="button" data-shell="zsh">zsh</button>' +
      '<button type="button" data-shell="csh" title="csh, tcsh">csh</button>';
    sh.querySelectorAll("button").forEach(function (b) {
      b.addEventListener("click", function () { setShell(b.dataset.shell); });
    });
    inner.appendChild(sh);

    var clock = document.createElement("span");
    clock.className = "slt";
    clock.title = "Second Life Time: the clock on the Pacific coast, which the grid keeps";
    sltClock(clock);
    inner.appendChild(clock);
    if (!alone) inner.appendChild(menu);

    bar.appendChild(inner);
    document.body.insertBefore(bar, document.body.firstChild);
  }

  function markNav(id) {
    document.querySelectorAll(".sitenav a").forEach(function (a) {
      a.classList.toggle("here", a.dataset.page === id);
      if (a.dataset.page === id) a.setAttribute("aria-current", "page");
      else a.removeAttribute("aria-current");
    });
  }

  // ---------------------------------------------------- per-page things

  function buildRail(article) {
    var rail = article.querySelector("nav.rail");
    if (!rail || rail.dataset.built) return;
    rail.dataset.built = "1";
    var links = {};
    if (rail.querySelector("ol")) {
      // The page wrote its own contents, with titles shorter than the
      // headings.  Keep them; only mark the one being read.
      rail.querySelectorAll("a[href^='#']").forEach(function (a) {
        links[a.getAttribute("href").slice(1)] = a;
      });
    } else {
      var ol = document.createElement("ol");
      article.querySelectorAll("main > section[id]").forEach(function (s) {
        var h = s.querySelector("h2");
        if (!h) return;
        var li = document.createElement("li");
        var a = document.createElement("a");
        a.href = "#" + s.id;
        var clone = h.cloneNode(true);
        var num = clone.querySelector(".num");
        if (num) num.remove();
        a.textContent = clone.textContent.trim();
        li.appendChild(a);
        // A section for one system, or for mixed setups, takes its
        // entry here with it when it hides.
        if (s.dataset.osOnly) li.dataset.osOnly = s.dataset.osOnly;
        ol.appendChild(li);
        links[s.id] = a;
      });
      rail.appendChild(ol);
    }
    if (!("IntersectionObserver" in window)) return;
    var watcher = new IntersectionObserver(function (entries) {
      entries.forEach(function (entry) {
        if (!entry.isIntersecting) return;
        Object.keys(links).forEach(function (id) { links[id].classList.remove("here"); });
        var here = links[entry.target.id];
        if (here) here.classList.add("here");
      });
    }, { rootMargin: "-10% 0px -75% 0px" });
    article.querySelectorAll("main > section[id]").forEach(function (s) { watcher.observe(s); });
  }

  // Copy what a person would type: prompts and printed output stay behind.
  function addCopy(block) {
    var pre = block.querySelector("pre");
    if (!pre || block.querySelector(".copy")) return;
    var button = document.createElement("button");
    button.type = "button";
    button.className = "copy";
    button.textContent = "copy";
    button.setAttribute("aria-label", "Copy to clipboard");
    button.addEventListener("click", function () {
      var clone = pre.cloneNode(true);
      clone.querySelectorAll(".p, .o").forEach(function (n) { n.remove(); });
      // Typed at a zsh or csh prompt, # starts no comment: it is passed
      // on as words, or run as a command.  So a command block copies
      // without its comments.  A file's comments are part of the file.
      var isTerm = block.classList.contains("term");
      if (isTerm) clone.querySelectorAll(".c").forEach(function (n) { n.remove(); });
      var text = clone.textContent;
      if (isTerm) {
        text = text.split("\n").map(function (l) { return l.replace(/[ \t]+$/, ""); })
          .filter(function (l) { return l !== ""; }).join("\n");
      }
      text = text.replace(/\n{3,}/g, "\n\n").replace(/^\n+|\s+$/g, "") + "\n";
      var done = function () {
        button.textContent = "copied";
        setTimeout(function () { button.textContent = "copy"; }, 1200);
      };
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(text).then(done, function () { button.textContent = "select + copy"; });
      } else {
        button.textContent = "select + copy";
      }
    });
    block.appendChild(button);
  }

  function addWhere(block) {
    var w = block.dataset.where;
    if (!w || block.querySelector(".where")) return;
    var label = { laptop: "on your computer", server: "on the slgod machine", bot: "on the slbotd machine" }[w] || w;
    if (block.dataset.whereLabel) label = block.dataset.whereLabel;
    var div = document.createElement("div");
    div.className = "where";
    div.textContent = label;
    block.insertBefore(div, block.firstChild);
  }

  // Tick boxes on numbered steps, remembered per page in this browser.
  function buildSteps(article) {
    article.querySelectorAll("ol.steps").forEach(function (ol, n) {
      if (ol.dataset.built) return;
      ol.dataset.built = "1";
      var key = "steps:" + article.id + ":" + (ol.id || n);
      var saved = (store(key) || "").split(",");
      var items = Array.prototype.slice.call(ol.children);

      var prog = document.createElement("div");
      prog.className = "progress";
      prog.innerHTML = '<span class="bar"><i></i></span><span class="count"></span>' +
        '<button type="button">clear ticks</button>';
      ol.parentNode.insertBefore(prog, ol);

      function update() {
        var done = [];
        items.forEach(function (li, i) {
          var box = li.querySelector(".tick input");
          li.classList.toggle("done", box.checked);
          if (box.checked) done.push(i);
        });
        prog.querySelector(".bar i").style.width = (100 * done.length / items.length) + "%";
        prog.querySelector(".count").textContent = done.length + " of " + items.length + " steps done";
        store(key, done.length ? done.join(",") : null);
      }

      items.forEach(function (li, i) {
        var body = li.querySelector(".step-body") || li;
        var label = document.createElement("label");
        label.className = "tick";
        var box = document.createElement("input");
        box.type = "checkbox";
        box.id = key.replace(/[^a-z0-9]+/gi, "-") + "-" + i;
        box.checked = saved.indexOf(String(i)) >= 0;
        box.addEventListener("change", update);
        label.appendChild(box);
        label.appendChild(document.createTextNode("done"));
        body.appendChild(label);
      });
      prog.querySelector("button").addEventListener("click", function () {
        items.forEach(function (li) { li.querySelector(".tick input").checked = false; });
        update();
      });
      update();
    });
  }

  // The troubleshooting filter: words in the search box, and chips that
  // narrow by component.  Every entry carries data-tags.
  function buildFaults(article) {
    article.querySelectorAll(".fault-tools").forEach(function (tools) {
      if (tools.dataset.built) return;
      tools.dataset.built = "1";
      var scope = tools.closest("section") || article;
      var input = tools.querySelector("input[type=search]");
      var chips = Array.prototype.slice.call(tools.querySelectorAll(".chip"));
      var count = tools.querySelector(".fault-count");
      var faults = Array.prototype.slice.call(scope.querySelectorAll("details.fault"));
      var tag = "";

      function apply() {
        var words = (input ? input.value : "").toLowerCase().split(/\s+/).filter(Boolean);
        var shown = 0;
        faults.forEach(function (d) {
          var text = d.textContent.toLowerCase() + " " + (d.dataset.tags || "");
          var ok = words.every(function (w) { return text.indexOf(w) >= 0; }) &&
            (!tag || (" " + (d.dataset.tags || "") + " ").indexOf(" " + tag + " ") >= 0);
          d.hidden = !ok;
          if (ok) shown++;
          if (ok && words.length && !d.open && shown <= 3) d.open = true;
        });
        if (count) count.textContent = shown + " of " + faults.length + " shown";
      }
      if (input) input.addEventListener("input", apply);
      chips.forEach(function (c) {
        c.addEventListener("click", function () {
          var on = c.getAttribute("aria-pressed") !== "true";
          chips.forEach(function (o) { o.setAttribute("aria-pressed", "false"); });
          c.setAttribute("aria-pressed", String(on));
          tag = on ? c.dataset.filter : "";
          apply();
        });
      });
      apply();
    });
  }

  // ------------------------------------------------ the topology picker

  var MACHINE = {
    laptop: { cls: "laptop", label: "your computer" },
    server: { cls: "server", label: "server" },
    bot: { cls: "bot", label: "bot machine" }
  };

  function drawTopology(svg, plan) {
    // plan.machines: [{key, os, procs:[...]}], left to right.
    var most = Math.max.apply(null, plan.machines.map(function (m) { return m.procs.length; }));
    var boxH = 70 + most * 34;
    var W = 640, H = boxH + 40, gap = 26;
    var n = plan.machines.length;
    var boxW = Math.min(190, (W - 150 - gap * (n - 1)) / n);
    var parts = [];
    parts.push('<defs><marker id="hb-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path class="arrowhead" d="M0,0 L10,5 L0,10 z"/></marker></defs>');
    var x = 12;
    var centers = {};
    plan.machines.forEach(function (m) {
      var cls = MACHINE[m.key].cls;
      parts.push('<rect class="box ' + cls + '" x="' + x + '" y="30" width="' + boxW + '" height="' + boxH + '" rx="6"/>');
      parts.push('<text class="t-machine" x="' + (x + 12) + '" y="52">' + esc(m.label) + '</text>');
      parts.push('<text class="t-small" x="' + (x + 12) + '" y="' + (boxH + 20) + '">' + esc(m.os === "mac" ? "macOS" : "Linux") + '</text>');
      m.procs.forEach(function (p, i) {
        var py = 66 + i * 34;
        parts.push('<rect class="proc" x="' + (x + 12) + '" y="' + py + '" width="' + (boxW - 24) + '" height="26" rx="3"/>');
        parts.push('<text class="t-proc" x="' + (x + 22) + '" y="' + (py + 18) + '">' + esc(p) + '</text>');
        centers[p] = { x: x, w: boxW, y: py + 13, left: x + 12, right: x + boxW - 12, machine: m.key };
      });
      x += boxW + gap;
    });
    // The grid, always on the right: a region tile, as the map draws one.
    var gx = W - 118;
    parts.push('<rect class="box grid" x="' + gx + '" y="60" width="106" height="90" rx="8"/>');
    parts.push('<text class="t-grid" x="' + (gx + 53) + '" y="102" text-anchor="middle">Second</text>');
    parts.push('<text class="t-grid" x="' + (gx + 53) + '" y="119" text-anchor="middle">Life</text>');

    (plan.links || []).forEach(function (l) {
      var a = centers[l.from];
      var d = centers[l.to || "slgod"];
      if (!a || !d) return;
      var cross = a.machine !== d.machine;
      var cls = "link" + (cross && l.via === "TLS" ? " secure" : "");
      var y1 = a.y, y2 = d.y;
      var x1 = a.x + a.w < d.x ? a.right : a.left;
      var x2 = a.x + a.w < d.x ? d.left : d.right;
      if (a.machine === d.machine) {
        // Same box: out to the margin on the right, along, and back in.
        var ex = a.right + 7;
        parts.push('<path class="' + cls + '" d="M' + a.right + ',' + y1 + ' L' + ex + ',' + y1 + ' L' + ex + ',' + y2 + ' L' + (a.right + 1) + ',' + y2 + '" marker-end="url(#hb-arrow)"/>');
      } else {
        parts.push('<path class="' + cls + '" d="M' + x1 + ',' + y1 + ' C' + (x1 + x2) / 2 + ',' + y1 + ' ' + (x1 + x2) / 2 + ',' + y2 + ' ' + x2 + ',' + y2 + '" marker-end="url(#hb-arrow)"/>');
        parts.push('<text class="t-small" x="' + ((x1 + x2) / 2) + '" y="' + (Math.min(y1, y2) - 8) + '" text-anchor="middle">' + esc(l.via) + '</text>');
      }
    });
    var g = centers.slgod;
    if (g) {
      parts.push('<path class="link grid" d="M' + (g.x + g.w) + ',' + g.y + ' L' + gx + ',105" marker-end="url(#hb-arrow)"/>');
    }
    svg.setAttribute("viewBox", "0 0 " + W + " " + H);
    svg.innerHTML = parts.join("");
  }

  function esc(s) {
    return String(s).replace(/[&<>"]/g, function (c) { return { "&": "&amp;", "<": "&lt;", ">": "&gt;", "\"": "&quot;" }[c]; });
  }

  function buildPicker(article) {
    var form = article.querySelector("form.picker");
    if (!form || form.dataset.built) return;
    form.dataset.built = "1";
    form.addEventListener("submit", function (e) { e.preventDefault(); });

    function val(name) {
      var el = form.querySelector('input[name="' + name + '"]:checked');
      return el ? el.value : "";
    }

    function update() {
      var slgod = val("slgod");      // here | remote
      var bot = val("bot");          // none | with-slgod | own
      var myos = val("myos");        // linux | mac
      var srvos = val("srvos");      // linux | mac
      var llm = val("llm");          // yes | no

      form.querySelector('[data-when="remote"]').hidden = slgod !== "remote";
      if (slgod !== "remote") srvos = myos;
      form.querySelector('[data-when="bot"]').hidden = bot === "none";

      var machines = [];
      var steps = [];
      var guide, name;
      if (slgod === "here") {
        var procs = ["slsh", "slgod"];
        machines.push({ key: "laptop", label: "your computer", os: myos, procs: procs });
        guide = "quickstart-local";
        name = "Everything on one machine";
        steps.push(["quickstart-local", "Build and start slgod, then connect slsh to it"]);
      } else {
        machines.push({ key: "laptop", label: "your computer", os: myos, procs: ["slsh"] });
        machines.push({ key: "server", label: "server", os: srvos, procs: ["slgod"] });
        guide = "quickstart-remote";
        name = "slgod on a server, slsh on your computer";
        steps.push(["quickstart-remote", "Start slgod on the server and reach it from your computer"]);
        steps.push(["sl-host", "Only if your computer moves between networks: let sl-host pick slgod's address"]);
      }
      var links = [{ from: "slsh", via: "TLS" }];
      if (bot === "with-slgod") {
        machines[machines.length - 1].procs.push("slbotd");
        links.push({ from: "slbotd", via: "" });
        steps.push(["quickstart-bot", "Add slbotd beside slgod"]);
      } else if (bot === "own") {
        machines.push({ key: "bot", label: "bot machine", os: val("botos") || "linux", procs: ["slbotd"] });
        links.push({ from: "slbotd", via: "TLS" });
        steps.push(["quickstart-bot", "Run slbotd on its own machine, pointed at slgod"]);
      }
      if (bot !== "none" && llm === "yes") {
        machines[machines.length - 1].procs.push("llama-server");
        links.push({ from: "slbotd", to: "llama-server", via: "HTTP" });
        steps.push(["bot-llm", "Give the bot a llama-server to chat through"]);
      }
      if (myos !== srvos || (bot === "own" && (val("botos") || "linux") !== srvos)) {
        steps.push(["platforms", "Read where Linux and macOS differ — your machines mix them"]);
      }
      if (slgod === "remote" || bot === "own") steps.push(["security", "Check the security notes for networked slgod"]);

      var out = form.querySelector(".result");
      out.querySelector(".scenario").textContent = name;
      var ol = out.querySelector("ol");
      ol.innerHTML = "";
      steps.forEach(function (s) {
        var li = document.createElement("li");
        var a = document.createElement("a");
        a.href = hrefFor(s[0]);
        a.textContent = s[1];
        li.appendChild(a);
        ol.appendChild(li);
      });
      out.querySelector(".button").href = hrefFor(guide);
      drawTopology(out.querySelector("svg"), { machines: machines, links: links });
      store("picker", JSON.stringify({ slgod: slgod, bot: bot, myos: myos, srvos: val("srvos"), llm: llm, botos: val("botos") }));
    }

    try {
      var saved = JSON.parse(store("picker") || "null");
      if (saved) Object.keys(saved).forEach(function (k) {
        var el = form.querySelector('input[name="' + k + '"][value="' + saved[k] + '"]');
        if (el) el.checked = true;
      });
    } catch (e) { /* a fresh start */ }
    if (!store("picker")) {
      var mine = form.querySelector('input[name="myos"][value="' + guessOS() + '"]');
      if (mine) mine.checked = true;
    }
    form.addEventListener("change", function () {
      update();
      // The picker names every machine, so the top bar can follow it:
      // one system if they all run the same one, both if they mix.
      var systems = {};
      systems[val("myos")] = 1;
      if (val("slgod") === "remote") systems[val("srvos")] = 1;
      if (val("bot") === "own") systems[val("botos") || "linux"] = 1;
      var list = Object.keys(systems);
      setOS(list.length === 1 ? list[0] : "both");
    });
    update();
  }

  function prepare(article) {
    if (article.dataset.ready) return;
    article.dataset.ready = "1";
    buildRail(article);
    buildSteps(article);
    buildFaults(article);
    buildPicker(article);
  }

  // ------------------------------------------------------------ routing

  function show(target) {
    var el = target ? document.getElementById(target) : null;
    var page = el ? (el.classList.contains("page") ? el : el.closest("article.page")) : null;
    if (!page) page = document.getElementById("index") || articles[0];
    articles.forEach(function (a) { a.hidden = a !== page; });
    prepare(page);
    markNav(page.id);
    var t = page.querySelector("h1");
    if (t) document.title = page.dataset.title || t.textContent + " — slgo handbook";
    if (el && el !== page) el.scrollIntoView();
    else window.scrollTo(0, 0);
    if (el && el.tagName === "DETAILS") el.open = true;
  }

  function init() {
    buildBar();
    // No guess: a browser knows the machine it runs on and nothing about
    // the others.  Until the reader says, every system shows, labelled.
    setOS(store("os") || "both");
    setShell(store("shell") || "");

    document.querySelectorAll(".term, .file").forEach(function (b) { addWhere(b); addCopy(b); });

    if (!bundled) {
      var only = articles[0];
      if (only) { prepare(only); markNav(only.id); }
      if (location.hash) {
        var d = document.getElementById(location.hash.slice(1));
        if (d && d.tagName === "DETAILS") d.open = true;
      }
      return;
    }

    document.addEventListener("click", function (e) {
      var a = e.target.closest && e.target.closest('a[href^="#"]');
      if (!a) return;
      var id = a.getAttribute("href").slice(1);
      if (!id || !document.getElementById(id)) return;
      e.preventDefault();
      document.querySelector(".sitebar").classList.remove("open");
      try { history.pushState(null, "", "#" + id); } catch (err) { /* the frame may refuse */ }
      show(id);
    });
    window.addEventListener("popstate", function () { show(location.hash.slice(1)); });
    window.addEventListener("hashchange", function () { show(location.hash.slice(1)); });
    show(location.hash.slice(1));
  }

  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", init);
  else init();
})();
