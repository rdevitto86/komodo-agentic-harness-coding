package dashboard

import "html/template"

// funcs are the helpers the page templates call.
var funcs = template.FuncMap{"num": num, "usd": usd}

// pageTmpl is the shell page; on load it opens the one stream that fills every element with an id.
// Actions reuse the page's own query string, so the token is never written into the page.
var pageTmpl = template.Must(template.New("page").Funcs(funcs).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Komodo</title>
<script type="module" src="/assets/datastar.js"></script>
<style>` + pageCSS + `</style></head>
<body data-init="@get('/events'+location.search)">
<main>
<header class="top"><div class="brand"><span class="logo"></span>Komodo<small>runs</small></div><div id="runhead"></div></header>
<div id="kpis" class="kpis"></div>
<section class="card"><h2>Pipeline</h2>
<svg id="pipeline" viewBox="0 0 640 {{.Height}}" role="img" aria-label="Stage pipeline with a live count per stage">
{{range $i, $b := .Boxes}}{{if $i}}<line class="flow" x1="{{$b.Prev}}" y1="54" x2="{{$b.X}}" y2="54"/>{{end}}{{end}}
<g id="diagram">{{template "stages" .Boxes}}<g id="pointers"></g></g>
</svg></section>
<div class="cols">
<section><h2 class="h">Groups</h2><div id="groups"></div></section>
<aside>
<section class="card"><h2>Events</h2><div id="events"></div></section>
<section class="card"><h2>Plan windows</h2><div id="resources"></div></section>
<section class="card"><h2>Agent models</h2><div class="models">
{{range .Agents}}<label>{{.}} <select data-bind="{{.}}" data-on:change="@post('/agents/{{.}}/model'+location.search)">{{range $.Models}}<option>{{.}}</option>{{end}}</select></label>
{{end}}</div><p id="log" class="muted">{{.Actions}} actions</p></section>
</aside></div>
</main></body></html>

{{define "stages"}}<g id="stages">{{range .}}<g class="stage{{if .Count}} on{{end}}"><rect x="{{.X}}" y="8" width="88" height="92" rx="12"/>
<text class="stage-label" x="{{.Mid}}" y="32">{{.Title}}</text><text class="stage-count" x="{{.Mid}}" y="82" id="count-{{.Key}}">{{.Count}}</text></g>{{end}}</g>{{end}}`))

// viewTmpl renders the elements the stream patches as HTML: header, tiles, cards, feed and meters.
var viewTmpl = template.Must(template.New("view").Funcs(funcs).Parse(`{{define "runhead"}}<div id="runhead">
<span><i>Run</i><b>{{.Run.ID}}</b></span><span><i>Repo</i><b>{{.Run.Repo}}</b></span><span class="wide"><i>Epic</i><b>{{.Run.Epic}}</b></span><span><i>Started</i><b>{{.Started}}</b></span></div>{{end}}

{{define "kpis"}}<div id="kpis" class="kpis">
<div class="kpi running"><b>{{.KPIs.Running}}</b><span>Running</span></div>
<div class="kpi blocked{{if not .KPIs.Blocked}} zero{{end}}"><b>{{.KPIs.Blocked}}</b><span>Blocked</span></div>
<div class="kpi done"><b>{{.KPIs.Done}}</b><span>Done</span></div>
{{if .Usage}}<div class="kpi"><b>{{num .KPIs.Tokens}}</b><span>Tokens</span></div>
<div class="kpi"><b>{{usd .KPIs.USD}}</b><span>USD</span></div>{{end}}</div>{{end}}

{{define "groups"}}<div id="groups" class="cards">{{range .Cards}}
<article class="card g {{.Tone}}">
<header><div><h3>{{.Label}}</h3><span class="gid">{{.Group.Group}}</span></div><span class="chip {{.Tone}}">{{.Chip}}</span></header>
<div class="prog"><div class="bar"><i style="width:{{.Pct}}%"></i></div><span>{{.TasksDone}} / {{.TasksTotal}} tasks · {{.ElapsedMin}} min</span></div>
<div class="sessions{{if .InTest}} test-review{{end}}">{{range .Rows}}<div class="sess {{.Status}}"><span class="role">{{.Role}}</span><span class="st">{{.Status}}</span><span class="model">{{.Model}}</span>{{if $.Usage}}<span class="use">{{num .Tokens}} tok · {{usd .USD}}</span>{{end}}</div>{{end}}</div>
{{if .Block}}<p class="reason {{.Tone}}"><b>{{.Chip}}</b> {{.Block}}</p>{{end}}
{{if .Held}}<button class="resume" data-on:click="@post('/groups/{{.Group.Group}}/resume'+location.search)">Resume</button>{{end}}
<details id="{{.ID}}" data-ignore-morph><summary>Brief</summary><pre>{{.Brief}}</pre></details>
</article>{{end}}</div>{{end}}

{{define "events"}}<div id="events" class="feed">{{range .Events}}<div class="ev"><time>{{.Time}}</time><span class="tag {{.Tone}}">{{.Type}}</span><span class="sub">{{.Subtype}}</span><span class="gid">{{.Group}}</span></div>{{else}}<p class="muted">No events yet.</p>{{end}}</div>{{end}}

{{define "resources"}}<div id="resources">{{range .Links}}<div class="res"><div class="rl"><span>{{.Name}}</span><b>{{.Percent}}%</b></div><div class="meter {{.Tone}}"><i style="width:{{.Width}}%"></i></div></div>{{else}}<p class="muted">No links.</p>{{end}}</div>{{end}}`))

// diagramTmpl renders the pipeline boxes and the group dots, patched in the svg namespace.
var diagramTmpl = template.Must(template.Must(pageTmpl.Clone()).New("dots").Parse(
	`<g id="diagram">{{template "stages" .Boxes}}<g id="pointers">{{range .Pointers}}<g id="ptr-{{.ID}}" class="pointer {{.Tone}}" style="transform:translate({{.X}}px,{{.Y}}px)"><circle r="5" cx="-26"/><text x="-16" y="4">{{.ID}}</text></g>{{end}}</g></g>`))

// pageCSS styles the page with tokens; the dark tokens follow the system theme.
const pageCSS = `
:root{--bg:#f1f5f9;--card:#fff;--soft:#f8fafc;--ink:#0f172a;--mute:#64748b;--line:#e2e8f0;
--accent:#4f46e5;--accent-soft:#eef2ff;--red:#dc2626;--red-soft:#fee2e2;--amber:#b45309;--amber-soft:#fef3c7;
--green:#15803d;--green-soft:#dcfce7;--shadow:0 1px 2px rgba(15,23,42,.06),0 6px 18px rgba(15,23,42,.07)}
@media (prefers-color-scheme:dark){:root{--bg:#0b1120;--card:#1e293b;--soft:#172033;--ink:#e2e8f0;--mute:#94a3b8;--line:#334155;
--accent:#a5b4fc;--accent-soft:#2b2f66;--red:#f87171;--red-soft:#4c1d24;--amber:#fbbf24;--amber-soft:#4a3410;
--green:#4ade80;--green-soft:#123524;--shadow:0 1px 2px rgba(0,0,0,.4),0 6px 18px rgba(0,0,0,.35)}}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--ink);font:14px/1.45 system-ui,-apple-system,"Segoe UI",sans-serif}
main{max-width:1240px;margin:0 auto;padding:20px 16px 40px;display:grid;gap:16px}
h2,h3{margin:0}
h2{font-size:11px;letter-spacing:.09em;text-transform:uppercase;color:var(--mute);margin-bottom:10px}
h2.h{margin:0 0 10px}
.card{background:var(--card);border-radius:14px;box-shadow:var(--shadow);padding:16px;min-width:0}
.muted{color:var(--mute);margin:8px 0 0;font-size:12px}
.top{display:flex;flex-wrap:wrap;gap:12px 28px;align-items:center;justify-content:space-between}
.brand{font-weight:800;font-size:20px;letter-spacing:.02em;display:flex;align-items:center;gap:10px}
.brand small{font-size:11px;font-weight:700;letter-spacing:.09em;text-transform:uppercase;color:var(--mute)}
.logo{width:22px;height:22px;border-radius:7px;background:linear-gradient(135deg,var(--accent),#7c3aed)}
#runhead{display:flex;flex-wrap:wrap;gap:6px 24px}
#runhead span{display:grid;min-width:0}
#runhead i,.kpi span,.role,.ev time{font-style:normal;font-size:10.5px;letter-spacing:.08em;text-transform:uppercase;color:var(--mute);font-weight:700}
#runhead b{font-size:13px;overflow-wrap:anywhere}
.kpis{display:grid;grid-template-columns:repeat(auto-fit,minmax(130px,1fr));gap:12px}
.kpi{background:var(--card);border-radius:14px;box-shadow:var(--shadow);padding:14px 16px;display:grid;gap:2px;border-top:3px solid var(--accent)}
.kpi b{font-size:28px;line-height:1.1;font-variant-numeric:tabular-nums}
.kpi.running{border-color:var(--accent)}.kpi.blocked{border-color:var(--red)}.kpi.blocked b{color:var(--red)}
.kpi.blocked.zero b{color:inherit}.kpi.done{border-color:var(--green)}.kpi:not(.running):not(.blocked):not(.done){border-color:var(--line)}
svg{display:block;width:100%;max-width:880px;margin:0 auto;height:auto;overflow:visible;font-family:system-ui,sans-serif}
.stage rect{fill:var(--soft);stroke:var(--line);stroke-width:1.5}
.stage.on rect{fill:var(--accent-soft);stroke:var(--accent)}
.stage-label{font-weight:700;font-size:11px;text-anchor:middle;fill:var(--mute)}
.stage-count{font-weight:800;font-size:30px;text-anchor:middle;fill:var(--line)}
.stage.on .stage-count{fill:var(--accent)}.stage.on .stage-label{fill:var(--ink)}
.flow{stroke:var(--accent);stroke-width:2;stroke-dasharray:4 4;animation:flow 1s linear infinite}
@keyframes flow{to{stroke-dashoffset:-8}}
.pointer{transition:transform .8s ease-in-out}
.pointer circle{fill:var(--accent)}.pointer.blocked circle{fill:var(--red)}.pointer.paused circle{fill:var(--amber)}.pointer.done circle{fill:var(--green)}
.pointer text{font:600 11px ui-monospace,monospace;fill:var(--ink)}
.cols{display:grid;grid-template-columns:minmax(0,1fr) 340px;gap:16px;align-items:start}
.cols>*{min-width:0}
aside{display:grid;gap:16px}
.cards{display:grid;grid-template-columns:repeat(auto-fill,minmax(min(100%,330px),1fr));gap:16px}
.g{display:grid;gap:12px;align-content:start;border-top:3px solid var(--accent)}
.g.blocked{border-color:var(--red)}.g.paused{border-color:var(--amber)}.g.done{border-color:var(--green)}
.g header{display:flex;justify-content:space-between;gap:8px;align-items:flex-start}
.g h3{font-size:16px}.gid{font:600 11px ui-monospace,monospace;color:var(--mute)}
.chip{display:inline-block;padding:3px 10px;border-radius:999px;font-size:10.5px;font-weight:800;letter-spacing:.06em;text-transform:uppercase;white-space:nowrap;background:var(--accent-soft);color:var(--accent)}
.chip.blocked{background:var(--red-soft);color:var(--red)}.chip.paused{background:var(--amber-soft);color:var(--amber)}.chip.done{background:var(--green-soft);color:var(--green)}
.prog{display:grid;gap:5px;font-size:12px;color:var(--mute)}
.bar,.meter{height:8px;border-radius:99px;background:var(--line);overflow:hidden}
.bar i,.meter i{display:block;height:100%;border-radius:99px;background:var(--accent);transition:width .6s ease}
.g.blocked .bar i{background:var(--red)}.g.paused .bar i{background:var(--amber)}.g.done .bar i{background:var(--green)}
.sessions{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:8px}
.sess{display:grid;gap:3px;padding:8px;border-radius:10px;background:var(--soft);border:1px solid var(--line);min-width:0}
.sess .st{justify-self:start;padding:1px 8px;border-radius:99px;font-size:10.5px;font-weight:800;text-transform:uppercase;letter-spacing:.05em;background:var(--line);color:var(--mute)}
.sess.running .st{background:var(--accent-soft);color:var(--accent);animation:pulse 1.4s ease-in-out infinite}
.sess.pass .st{background:var(--green-soft);color:var(--green)}.sess.fail .st{background:var(--red-soft);color:var(--red)}
@keyframes pulse{50%{opacity:.45}}
.sess .model{font:600 11px ui-monospace,monospace;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.sess .use{font-size:10.5px;color:var(--mute);overflow-wrap:anywhere}
.sessions.test-review .sess.builder{opacity:.5}
.sessions.test-review .sess.tester,.sessions.test-review .sess.reviewer{border-color:var(--accent);box-shadow:0 0 0 2px var(--accent-soft)}
.reason{margin:0;padding:8px 10px;border-radius:10px;font-size:12.5px;overflow-wrap:anywhere}
.reason.blocked{background:var(--red-soft);color:var(--red)}.reason.paused{background:var(--amber-soft);color:var(--amber)}
button{font:700 12px system-ui;padding:8px 14px;border-radius:9px;border:0;background:var(--accent);color:#fff;cursor:pointer;justify-self:start}
@media (prefers-color-scheme:dark){button{color:#0b1120}}
button:hover{filter:brightness(1.1)}
summary{cursor:pointer;font-size:11px;font-weight:800;letter-spacing:.08em;text-transform:uppercase;color:var(--mute)}
pre{margin:8px 0 0;padding:10px;border-radius:10px;background:var(--soft);font:12px/1.5 ui-monospace,monospace;white-space:pre-wrap;overflow-wrap:anywhere}
.feed{display:grid;gap:2px}
.ev{display:grid;grid-template-columns:62px auto 1fr auto;gap:8px;align-items:center;padding:6px 0;border-bottom:1px solid var(--line);font-size:12px}
.ev:last-child{border:0}.ev .sub{overflow-wrap:anywhere}
.tag{padding:1px 7px;border-radius:6px;font:700 10.5px ui-monospace,monospace;background:var(--line);color:var(--mute)}
.tag.status{background:var(--accent-soft);color:var(--accent)}.tag.done{background:var(--green-soft);color:var(--green)}
.tag.blocked{background:var(--red-soft);color:var(--red)}.tag.paused{background:var(--amber-soft);color:var(--amber)}
.res+.res{margin-top:12px}.rl{display:flex;justify-content:space-between;margin-bottom:5px;font-size:12.5px}
.meter.warn i{background:var(--amber)}.meter.hot i{background:var(--red)}
.models{display:grid;grid-template-columns:1fr 1fr;gap:10px}
label{display:grid;gap:3px;font-size:10.5px;font-weight:700;letter-spacing:.08em;text-transform:uppercase;color:var(--mute)}
select{font:13px system-ui;padding:6px 8px;border-radius:8px;border:1px solid var(--line);background:var(--soft);color:var(--ink);min-width:0;width:100%}
.err{color:var(--red);font-weight:600}
@media (max-width:900px){.cols{grid-template-columns:minmax(0,1fr)}}
@media (max-width:480px){main{padding:14px 16px 32px}.kpi b{font-size:24px}.ev{grid-template-columns:56px auto 1fr}.ev .gid{display:none}}
`
