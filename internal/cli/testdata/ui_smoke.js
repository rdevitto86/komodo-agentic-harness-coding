// Runs embed/ui/ui.js against a stub document in macOS JavaScript and prints what it drew as JSON.
ObjC.import("Foundation");
function read(p) { return $.NSString.stringWithContentsOfFileEncodingError(p, $.NSUTF8StringEncoding, null).js; }

function run(argv) {
  const src = read(argv[0]);
  const listeners = {};
  // Text children are recorded as strings the way a browser would show them, so a stray null reads "null".
  const kid = (x) => (x instanceof N ? x : { text: String(x) });
  class N {
    constructor(t) { this.tag = t; this.kids = []; this.attrs = {}; this.style = {}; }
    setAttribute(k, v) { this.attrs[k] = String(v); }
    append(...k) { for (const x of k) this.kids.push(kid(x)); }
    replaceChildren(...k) { this.kids = k.map(kid); }
    addEventListener(n, f) { listeners[n] = f; }
  }
  const byId = {};
  for (const id of ["conn", "error", "kpis", "pipeline", "runs", "queued", "events"]) byId[id] = new N(id);
  const doc = { getElementById: (id) => byId[id], createElementNS: (ns, t) => new N(t) };
  const make = new Function("document", "EventSource", "requestAnimationFrame", src + "\n;return render;");
  const render = make(doc, function () { return new N("es"); }, (f) => f());
  const texts = (n, out) => { if (n.text !== undefined) out.push(n.text); else n.kids.forEach((k) => texts(k, out)); return out; };
  const st = {
    runs: [{ run_id: "r1", target: "ep-1", figures: { wall_s: 3725, tokens_in: 1000, tokens_out: 234, usd: 0.5, usd_equiv: 1.25 },
      groups: [{ group: "tg-1", stage: "test_review", state: "running" }, { group: "tg-2", stage: "build", state: "blocked", reason: "needs_scope", question: "ok?" },
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
  listeners.state({ data: JSON.stringify(st) });
  return JSON.stringify(clean);
}
