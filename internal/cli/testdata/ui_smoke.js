// Runs embed/ui/ui.js against a stub document in macOS JavaScript and prints what it drew as JSON.
ObjC.import("Foundation");
function read(p) { return $.NSString.stringWithContentsOfFileEncodingError(p, $.NSUTF8StringEncoding, null).js; }

function run(argv) {
  const src = read(argv[0]);
  const docListeners = {};
  const doc = { activeElement: null };
  // Text children are recorded as strings the way a browser would show them, so a stray null reads "null".
  const kid = (x) => (x instanceof N ? x : { text: String(x) });
  class N {
    constructor(t) { this.tag = t; this.kids = []; this.attrs = {}; this.style = {}; this.on = {}; this.scrollTop = 0; }
    setAttribute(k, v) { this.attrs[k] = String(v); }
    removeAttribute(k) { delete this.attrs[k]; }
    getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; }
    append(...k) { for (const x of k) this.kids.push(kid(x)); }
    replaceChildren(...k) { this.kids = k.map(kid); }
    addEventListener(n, f) { this.on[n] = f; }
    focus() { doc.activeElement = this; }
  }
  const byId = {};
  for (const id of ["conn", "error", "kpis", "pipeline", "runs", "queued", "events", "app", "backdrop", "drawer", "drawer-title",
    "drawer-body", "drawer-close"]) byId[id] = new N(id);
  byId.drawer.attrs.hidden = "";
  byId.backdrop.attrs.hidden = "";
  doc.getElementById = (id) => byId[id];
  doc.createElementNS = (ns, t) => new N(t);
  doc.addEventListener = (n, f) => { docListeners[n] = f; };
  let stream = null;
  const make = new Function("document", "EventSource", "requestAnimationFrame", src + "\n;return render;");
  const render = make(doc, function () { stream = new N("es"); return stream; }, (f) => f());
  const texts = (n, out) => { if (n.text !== undefined) out.push(n.text); else n.kids.forEach((k) => texts(k, out)); return out; };
  const st = {
    runs: [{ run_id: "r1", target: "plan-7f3a9c", plan: "plan-7f3a9c", started: "2026-10-15T11:58:00Z",
      figures: { wall_s: 3725, tokens_in: 1000, tokens_out: 234, usd: 0.5, usd_equiv: 1.25 },
      events: [{ time: "2026-10-15T12:00:03Z", type: "run-complete", subtype: "stopped", run: "r1", group: "tg-1" }],
      groups: [{ group: "tg-1", stage: "test_review", state: "running" },
        { group: "tg-2", stage: "build", state: "blocked", reason: "risky_change", question: "Approve the lease change?" },
        { group: "tg-3", stage: "", state: "waiting" }, { group: "tg-4", stage: "intake", state: "done" }] }],
    queued: [{ target: "ep-2", at: "2026-10-16T09:00:00Z" }], blocked: [],
    totals: { running: 1, paused: 0, blocked: 1, waiting: 1, done: 1, queued: 1, tokens: 1234, usd: 0.5 },
    events: [{ time: "2026-10-15T12:00:03Z", type: "run-complete", subtype: "stopped", run: "r1", group: "tg-1" }],
  };
  render(st);
  render(st);
  const clean = { errorKids: byId.error.kids.length, runTexts: texts(byId.runs, []), all: [].concat(...Object.values(byId).map((n) => texts(n, []))) };
  render(Object.assign({}, st, { error: "boom" }));
  clean.errorKidsWithError = byId.error.kids.length;
  stream.on.state({ data: JSON.stringify(st) });

  // The drawer: Space opens it with focus inside, a live update refills it, Escape closes it and returns focus to the row.
  const row = () => byId.runs.kids[0];
  const key = (k) => ({ key: k, preventDefault() {} });
  const keys = [];
  row().on.keydown(key(" "));
  clean.opened = !("hidden" in byId.drawer.attrs) && "inert" in byId.app.attrs;
  clean.focusInDrawer = doc.activeElement && doc.activeElement.tag === "button";
  clean.drawerText = texts(byId["drawer-body"], []).concat(texts(byId["drawer-title"], []));
  const next = JSON.parse(JSON.stringify(st));
  next.runs[0].groups[0].state = "done";
  render(next);
  clean.liveUpdate = texts(byId["drawer-body"], []).includes("done") && !("hidden" in byId.drawer.attrs);
  docListeners.keydown(key("Escape"));
  clean.closed = "hidden" in byId.drawer.attrs && !("inert" in byId.app.attrs);
  clean.focusBack = doc.activeElement === row();
  row().on.keydown(key("Enter"));
  clean.enterOpens = !("hidden" in byId.drawer.attrs);
  byId.backdrop.on.click();
  clean.backdropCloses = "hidden" in byId.drawer.attrs;
  row().on.click();
  clean.clickOpens = !("hidden" in byId.drawer.attrs);
  return JSON.stringify(clean);
}
