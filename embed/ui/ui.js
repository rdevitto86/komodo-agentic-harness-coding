"use strict";
// The dashboard script: it opens the one event stream and draws each state it receives. It sends nothing back.

const STAGES = ["intake", "build", "test_review", "commit", "cleanup", "done"];
const TITLES = {intake: "Intake", build: "Build", test_review: "Test · review", commit: "Commit", cleanup: "Cleanup", done: "Done"};
const SVG = "http://www.w3.org/2000/svg";
const dots = new Map(); // pipeline dot elements by run and group, kept so a moved dot slides

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
  document.getElementById(id).replaceChildren(...kids.flat());
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
  return html("article", "g " + t,
    html("header", "", html("div", "", html("h3", "", g.group), html("span", "gid", g.stage || "not started")), html("span", "chip " + t, chipText(g))),
    g.question ? html("p", "reason", html("b", "", g.reason + ": "), g.question) : null);
}

// runCard draws one run: its target, id, time, tokens and cost, and its group cards.
function runCard(r) {
  const f = r.figures;
  const figs = html("div", "figs");
  if (f) {
    figs.append(html("span", "fig", "Time " + span(f.wall_s)), html("span", "fig", num(f.tokens_in + f.tokens_out) + " tokens"), html("span", "fig", usd(f.usd)));
    if (f.usd_equiv > 0) figs.append(html("span", "fig", "≈ " + usd(f.usd_equiv) + " subscription"));
  }
  return html("article", "card run",
    html("header", "", html("div", "", html("h3", "", r.target), html("span", "gid", r.run_id)), figs),
    html("div", "groups", r.groups.map(groupCard)));
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
  fill("runs", s.runs.length ? s.runs.map(runCard) : html("p", "muted", "No live runs."));
  queued(s.queued);
  events(s.events);
}

// connect opens the event stream; the browser reconnects it by itself after a drop.
function connect() {
  const conn = document.getElementById("conn");
  const es = new EventSource("/events");
  es.addEventListener("state", (ev) => render(JSON.parse(ev.data)));
  es.onopen = () => { conn.textContent = "live"; conn.className = "conn"; };
  es.onerror = () => { conn.textContent = "reconnecting"; conn.className = "conn off"; };
}

connect();
