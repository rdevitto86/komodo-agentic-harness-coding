"use strict";
// The dashboard script: it opens the one event stream and draws each state it receives. It sends nothing back.

const STAGES = ["intake", "build", "test_review", "commit", "cleanup", "done"];
const TITLES = {intake: "Intake", build: "Build", test_review: "Test · review", commit: "Commit", cleanup: "Cleanup", done: "Done"};
const SVG = "http://www.w3.org/2000/svg";
const dots = new Map(); // pipeline dot elements by run and group, kept so a moved dot slides
const rows = new Map(); // run row elements by run id, so focus can return to one
const ZERO = "0001-01-01T00:00:00Z"; // how an unset time arrives
let current = {runs: []}; // the latest state received
let openId = null; // the run id shown in the drawer, or null when it is closed
let closer = null; // the drawer's close button

// el builds an element with attributes and children; text children become text nodes, never markup.
function el(tag, attrs, ...kids) {
  const e = document.createElementNS(tag === "svg" || attrs.svg ? SVG : "http://www.w3.org/1999/xhtml", tag);
  for (const [k, v] of Object.entries(attrs)) if (k !== "svg") e.setAttribute(k, v);
  for (const k of kids.flat()) if (k != null) e.append(k);
  return e;
}
const html = (tag, cls, ...kids) => el(tag, cls ? {class: cls} : {}, ...kids);
const svg = (tag, attrs, ...kids) => el(tag, {svg: 1, ...attrs}, ...kids);

// fill replaces the children of the element with the id.
function fill(id, ...kids) {
  document.getElementById(id).replaceChildren(...kids.flat().filter((k) => k != null));
}

const num = (n) => Math.round(n).toLocaleString("en-US");
const usd = (v) => "$" + v.toFixed(2);
const clock = (t) => new Date(t).toLocaleTimeString("en-GB", {hour12: false});

// span turns seconds into "1h 02m", "4m 05s" or "12s".
function span(s) {
  const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), r = s % 60;
  if (h > 0) return h + "h " + String(m).padStart(2, "0") + "m";
  if (m > 0) return m + "m " + String(r).padStart(2, "0") + "s";
  return r + "s";
}

// tone is the colour class of a group state.
function tone(state) {
  return {blocked: "blocked", paused: "paused", done: "done", waiting: "waiting"}[state] || "active";
}

// chipText is the words on a group's chip: the hold for a held group, else the stage.
function chipText(g) {
  if (g.state === "blocked") return "Blocked";
  if (g.state === "paused") return "Paused";
  if (g.state === "waiting") return "Waiting";
  return TITLES[g.stage] || g.stage;
}

// kpis draws the tiles for the counts and sums.
function kpis(t) {
  const tile = (cls, n, label) => html("div", "kpi " + cls, html("b", "", String(n)), html("span", "", label));
  fill("kpis",
    tile("running", t.running, "Running"),
    tile("blocked" + (t.blocked ? "" : " zero"), t.blocked, "Blocked"),
    tile("paused", t.paused, "Paused"),
    tile("done", t.done, "Done"),
    tile("", t.queued, "Queued"),
    tile("", num(t.tokens), "Tokens"),
    tile("", usd(t.usd), "USD"));
}

// pipeline draws the stage boxes with live counts and slides one dot per group under its stage.
function pipeline(runs) {
  const svgEl = document.getElementById("pipeline");
  const counts = STAGES.map(() => 0);
  const placed = [];
  for (const r of runs) {
    for (const g of r.groups) {
      const i = STAGES.indexOf(g.stage);
      if (i < 0) continue;
      placed.push({key: r.run_id + "/" + g.group, label: g.group, tone: tone(g.state), x: 1 + i * 110 + 44, y: 122 + counts[i] * 26});
      counts[i]++;
    }
  }
  const boxes = STAGES.map((st, i) => {
    const x = 1 + i * 110;
    return svg("g", {class: "stage" + (counts[i] ? " on" : "")},
      svg("rect", {x, y: 8, width: 88, height: 92, rx: 12}),
      svg("text", {class: "stage-label", x: x + 44, y: 32}, TITLES[st]),
      svg("text", {class: "stage-count", x: x + 44, y: 82}, String(counts[i])));
  });
  const flows = STAGES.slice(1).map((_, i) => svg("line", {class: "flow", x1: 1 + i * 110 + 88, y1: 54, x2: 1 + (i + 1) * 110, y2: 54}));
  const layer = svg("g", {id: "pointers"});
  svgEl.setAttribute("viewBox", "0 0 650 " + (122 + Math.max(0, ...counts) * 26 + 10));
  svgEl.replaceChildren(...flows, ...boxes, layer);
  const live = new Set();
  for (const p of placed) {
    live.add(p.key);
    let dot = dots.get(p.key);
    const fresh = !dot;
    if (fresh) {
      dot = svg("g", {}, svg("circle", {r: 5, cx: -26}), svg("text", {x: -16, y: 4}, p.label));
      dot.style.transform = "translate(" + p.x + "px," + p.y + "px)";
      dots.set(p.key, dot);
    }
    dot.setAttribute("class", "pointer " + p.tone);
    layer.append(dot);
    if (!fresh) requestAnimationFrame(() => { dot.style.transform = "translate(" + p.x + "px," + p.y + "px)"; });
  }
  for (const k of [...dots.keys()]) if (!live.has(k)) dots.delete(k);
}

// groupCard draws one group: its chip, and the question it asks when it is blocked.
function groupCard(g) {
  const t = tone(g.state);
  const heldAt = (g.state === "blocked" || g.state === "paused") && TITLES[g.stage] ? "held in " + TITLES[g.stage] : null;
  return html("article", "g " + t,
    html("header", "", html("div", "", html("h3", "", g.group), heldAt ? html("span", "gid", heldAt) : null), html("span", "chip " + t, chipText(g))),
    g.question ? html("p", "reason", html("b", "", g.reason + ": "), g.question) : null);
}

// figures draws the time, tokens and cost chips of a run.
function figures(f) {
  const figs = html("div", "figs");
  if (f) {
    figs.append(html("span", "fig", "Time " + span(f.wall_s)), html("span", "fig", num(f.tokens_in + f.tokens_out) + " tokens"), html("span", "fig", usd(f.usd)));
    if (f.usd_equiv > 0) figs.append(html("span", "fig", "≈ " + usd(f.usd_equiv) + " subscription"));
  }
  return figs;
}

// runCard draws one run as a row that opens its drawer on click, Enter or Space.
function runCard(r) {
  const card = el("article", {class: "card run", tabindex: "0", role: "button", "aria-haspopup": "dialog", "data-run": r.run_id,
      "aria-label": "Open run " + (r.plan || r.target)},
    html("header", "", html("div", "", html("h3", "", r.target), html("span", "gid", "run " + r.run_id)), figures(r.figures)),
    html("div", "groups", r.groups.map(groupCard)));
  card.addEventListener("click", () => openRun(r.run_id));
  card.addEventListener("keydown", (ev) => {
    if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); openRun(r.run_id); }
  });
  rows.set(r.run_id, card);
  return card;
}

// facts draws label and value pairs.
function facts(pairs) {
  return html("dl", "facts", pairs.map(([k, v]) => html("div", "", html("dt", "", k), html("dd", "", v))));
}

// outcome words what a group has come to: the block reason, finished, or still open.
function outcome(g) {
  if (g.state === "blocked") return g.reason || "Waiting for an answer";
  return g.state === "done" ? "Finished" : "Open";
}

// groupTable draws every group of a run with its stage, status and outcome.
function groupTable(groups) {
  const head = html("tr", "", ["Group", "Stage", "Status", "Outcome"].map((h) => html("th", "", h)));
  const body = groups.map((g) => html("tr", "", html("td", "mono", g.group), html("td", "", TITLES[g.stage] || "—"),
    html("td", "", html("span", "chip " + tone(g.state), g.state)), html("td", "", outcome(g))));
  return html("table", "gt", html("thead", "", head), html("tbody", "", body));
}

// runEvents draws the recent events of one run, newest first.
function runEvents(rows) {
  if (!rows.length) return html("p", "muted", "No events yet.");
  return html("div", "feed", rows.map((e) => html("div", "ev", html("time", "", clock(e.time)), html("span", "tag " + evTone(e.type), e.type), html("span", "sub", e.subtype), html("span", "gid", e.group))));
}

// detail draws the whole drawer body of one run.
function detail(r) {
  const f = r.figures;
  const pairs = [["Plan", r.plan || "—"], ["Run id", r.run_id], ["Started with", r.target],
    ["Started", r.started && r.started !== ZERO ? new Date(r.started).toLocaleString("en-GB") : "—"]];
  if (f) pairs.push(["Time", span(f.wall_s)], ["Tokens", num(f.tokens_in) + " in · " + num(f.tokens_out) + " out"], ["Cost", usd(f.usd) + (f.usd_equiv > 0 ? " · ≈ " + usd(f.usd_equiv) + " subscription" : "")]);
  const asks = r.groups.filter((g) => g.question).map((g) => html("p", "reason", html("b", "", g.group + " · " + g.reason + ": "), g.question));
  return [facts(pairs), asks.length ? html("h2", "", "Waiting for an answer") : null, ...asks,
    html("h2", "", "Groups"), groupTable(r.groups), html("h2", "", "Recent events"), runEvents(r.events || [])];
}

// drawer fills the open drawer from the state, keeping the scroll position so a live update does not jump.
function drawer(s) {
  if (!openId) return;
  const body = document.getElementById("drawer-body");
  const top = body.scrollTop;
  const r = s.runs.find((x) => x.run_id === openId);
  document.getElementById("drawer-title").replaceChildren(r ? (r.plan || r.target) : "Run ended");
  body.replaceChildren(...(r ? detail(r) : [html("p", "muted", "This run is no longer live.")]).filter((k) => k != null));
  body.scrollTop = top;
}

// show hides or reveals an element by its id.
function show(id, on) {
  const e = document.getElementById(id);
  if (on) e.removeAttribute("hidden"); else e.setAttribute("hidden", "");
}

// openRun opens the drawer on a run, makes the page behind it inert and moves focus into the drawer.
function openRun(id) {
  openId = id;
  document.getElementById("app").setAttribute("inert", "");
  show("backdrop", true);
  show("drawer", true);
  drawer(current);
  closer.focus();
}

// closeRun closes the drawer and returns focus to the row that opened it.
function closeRun() {
  const id = openId;
  openId = null;
  show("drawer", false);
  show("backdrop", false);
  document.getElementById("app").removeAttribute("inert");
  const row = rows.get(id);
  if (row) row.focus();
}

// focusedRun returns the id of the run row that has focus, if any.
function focusedRun() {
  const a = document.activeElement;
  return a && a.getAttribute ? a.getAttribute("data-run") : null;
}

// wire builds the close button and binds Escape and the backdrop to it.
function wire() {
  closer = el("button", {type: "button", class: "close", "aria-label": "Close run"}, "Close");
  closer.addEventListener("click", closeRun);
  document.getElementById("drawer-close").replaceChildren(closer);
  document.getElementById("backdrop").addEventListener("click", closeRun);
  document.addEventListener("keydown", (ev) => { if (ev.key === "Escape" && openId) closeRun(); });
}

// queued draws the waiting runs.
function queued(rows) {
  if (!rows.length) return fill("queued", html("p", "muted", "No queued runs."));
  fill("queued", rows.map((q) => html("div", "q", html("b", "", q.target), html("span", "gid", "starts " + new Date(q.at).toLocaleString("en-GB")))));
}

// evTone is the colour class of an event type.
function evTone(type) {
  return {"run-status": "status", "run-complete": "done", "run-failure": "blocked", "error": "blocked", "resource": "paused"}[type] || "plain";
}

// events draws the feed, newest first.
function events(rows) {
  if (!rows.length) return fill("events", html("p", "muted", "No events yet."));
  fill("events", rows.map((e) => html("div", "ev", html("time", "", clock(e.time)), html("span", "tag " + evTone(e.type), e.type), html("span", "sub", e.subtype), html("span", "gid", e.group))));
}

// render draws a whole state.
function render(s) {
  fill("error", s.error ? html("div", "banner", "The engine could not be read: " + s.error) : null);
  kpis(s.totals);
  pipeline(s.runs);
  const had = openId ? null : focusedRun();
  current = s;
  rows.clear();
  fill("runs", s.runs.length ? s.runs.map(runCard) : html("p", "muted", "No live runs."));
  if (had && rows.has(had)) rows.get(had).focus();
  queued(s.queued);
  events(s.events);
  drawer(s);
}

// connect opens the event stream; the browser reconnects it by itself after a drop.
function connect() {
  const conn = document.getElementById("conn");
  const es = new EventSource("/events");
  es.addEventListener("state", (ev) => render(JSON.parse(ev.data)));
  es.onopen = () => { conn.textContent = "live"; conn.className = "conn"; };
  es.onerror = () => { conn.textContent = "reconnecting"; conn.className = "conn off"; };
}

wire();
connect();
