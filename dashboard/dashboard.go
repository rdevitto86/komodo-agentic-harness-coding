// Package dashboard is a proof of concept for a local browser view of active groups.
// One server-sent event stream patches the groups table and the stage diagram; the embedded Datastar library applies it.
package dashboard

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

var (
	// assets holds the Datastar library, the only client-side code, and its license.
	//
	//go:embed assets/datastar.js assets/DATASTAR-LICENSE.md
	assets embed.FS

	// Stages are the pipeline stages in order, as the diagram draws them.
	Stages = []string{"Intake", "Build", "Check", "Verify", "Ship", "Blocked"}

	// Agents are the seven agents whose model the page offers to change.
	Agents = []string{"builder", "reviewer", "planner", "tester", "architect", "researcher", "scout"}

	// Models are the agent `model` values the page offers, as the config agent group names them.
	Models = []string{"opus", "sonnet", "haiku", "opus-prev"}

	// maxSignals caps a posted signals body; the page sends seven short strings.
	maxSignals int64 = 64 << 10

	// lineEnds turns every line ending an event stream reader accepts into a plain newline.
	lineEnds = strings.NewReplacer("\r\n", "\n", "\r", "\n")
)

// Group is one row of the groups view: where a group is and who works on it.
type Group struct {
	Repo   string  `json:"repo"`             // repo name
	Run    string  `json:"run"`              // run id
	Group  string  `json:"group"`            // group id
	Stage  string  `json:"stage"`            // current stage
	Agent  string  `json:"agent"`            // agent of the live session
	Model  string  `json:"model"`            // model of the live session
	Tokens int     `json:"tokens,omitempty"` // tokens used so far; zero when tracking is off
	USD    float64 `json:"usd,omitempty"`    // cost so far; zero when tracking is off
	Block  string  `json:"block"`            // block reason, empty when not blocked
	Brief  string  `json:"brief"`            // prompt the live session got
}

// pageTmpl is the shell page; on load it opens the one stream that fills #groups and #pointers.
// Actions reuse the page's own query string, so the token is never written into the page.
var pageTmpl = template.Must(template.New("page").Parse(`<!doctype html>
<meta charset="utf-8"><title>Komodo</title>
<script type="module" src="/assets/datastar.js"></script>
<style>
body{font:14px system-ui;margin:16px}td,th{padding:4px 8px;text-align:left;vertical-align:top}.Blocked{color:#c33}
.stage{fill:#f4f6fa;stroke:#8a94a6;rx:8}.stage-label{font-weight:600;text-anchor:middle}
.flow{stroke:#5b8def;stroke-width:2;stroke-dasharray:6 6;animation:flow 1s linear infinite}
@keyframes flow{to{stroke-dashoffset:-12}}
.pointer{transition:transform .8s ease-in-out}.pointer circle{fill:#5b8def}.pointer.blocked circle{fill:#c33}
.pointer text{font-size:11px}
</style>
<body data-init="@get('/events'+location.search)">
<h1>Komodo runs</h1>
<svg id="pipeline" viewBox="0 0 900 260" width="900" height="260">
{{range $i, $s := .Stages}}<rect class="stage" x="{{index $.StageX $i}}" y="20" width="120" height="70"/>
<text class="stage-label" x="{{index $.StageMid $i}}" y="60">{{$s}}</text>
{{end}}{{range $i, $x := .FlowX}}<line class="flow" x1="{{$x}}" y1="55" x2="{{index $.FlowEnd $i}}" y2="55"/>
{{end}}<g id="pointers"></g>
</svg>
<p>{{range .Agents}}<label>{{.}} <select data-bind="{{.}}" data-on:change="@post('/agents/{{.}}/model'+location.search)">{{range $.Models}}<option>{{.}}</option>{{end}}</select></label> {{end}}</p>
<div id="groups"></div>
<p id="log">{{.Actions}} actions</p>`))

// rowsTmpl renders the groups table; a resume button appears only on blocked groups.
var rowsTmpl = template.Must(template.New("rows").Parse(`<div id="groups"><table>
<tr><th>Repo<th>Group<th>Stage<th>Agent<th>Model{{if .Usage}}<th>Tokens<th>USD{{end}}<th>Block<th></tr>
{{range .Groups}}<tr><td>{{.Repo}}<td>{{.Group}}<td class="{{.Stage}}">{{.Stage}}<td>{{.Agent}}<td>{{.Model}}{{if $.Usage}}<td>{{.Tokens}}<td>{{printf "%.2f" .USD}}{{end}}<td>{{.Block}}
<td>{{if .Block}}<button data-on:click="@post('/groups/{{.Group}}/resume'+location.search)">resume</button>{{end}}
<details><summary>brief</summary><pre>{{.Brief}}</pre></details></tr>
{{end}}</table></div>`))

// pointersTmpl renders one dot per group; each keeps its id across patches, so CSS animates the move.
var pointersTmpl = template.Must(template.New("pointers").Parse(`<g id="pointers">{{range .}}<g id="ptr-{{.ID}}" class="pointer{{if .Blocked}} blocked{{end}}" style="transform:translate({{.X}}px,{{.Y}}px)"><circle r="9"/><text y="-14" text-anchor="middle">{{.ID}}</text></g>{{end}}</g>`))

// Server serves the dashboard from one groups view file and records requested actions.
type Server struct {
	token   string         // per-start secret every request must carry
	usage   bool           // show tokens and cost; mirrors the usage tracking setting
	views   string         // path of the groups view JSON
	mux     *http.ServeMux // routes
	mu      sync.Mutex     // guards actions
	actions []string       // actions requested so far, newest last
}

// pointer is one dot on the stage diagram.
type pointer struct {
	ID      string // group id
	X, Y    int    // centre of the dot
	Blocked bool   // draws the dot red
}

// New builds a server that reads groups from viewsPath and requires token on every request.
// usage shows tokens and cost; when false they are dropped before rendering or streaming.
func New(token, viewsPath string, usage bool) *Server {
	s := &Server{token: token, views: viewsPath, usage: usage, mux: http.NewServeMux()}
	s.mux.Handle("GET /assets/", http.FileServerFS(assets))
	s.mux.HandleFunc("GET /{$}", s.guard(s.page))
	s.mux.HandleFunc("GET /events", s.guard(s.events))
	s.mux.HandleFunc("POST /groups/{group}/resume", s.guard(s.resume))
	s.mux.HandleFunc("POST /agents/{agent}/model", s.guard(s.setModel))
	return s
}

// ServeHTTP routes a request to the page, the stream, an action or an asset.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// Actions returns a copy of the actions requested so far.
func (s *Server) Actions() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.actions...)
}

// Serve listens on a random loopback port, prints the page URL and serves until the listener fails.
func Serve(s *Server) error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen on loopback: %w", err)
	}
	fmt.Printf("http://%s/?t=%s\n", ln.Addr(), s.token)
	return http.Serve(ln, s)
}

// guard refuses a request without the token or addressed to a host other than 127.0.0.1.
func (s *Server) guard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.Host)
		if host != "127.0.0.1" || r.URL.Query().Get("t") != s.token {
			http.Error(w, "refused", http.StatusForbidden)
			return
		}
		h(w, r)
	}
}

// page writes the shell page with one 120-wide box per stage, 30 apart.
func (s *Server) page(w http.ResponseWriter, _ *http.Request) {
	var x, mid, flowX, flowEnd []int
	for i := range Stages {
		x = append(x, 20+i*150)
		mid = append(mid, stageMid(i))
		if i < len(Stages)-2 {
			flowX, flowEnd = append(flowX, 140+i*150), append(flowEnd, 170+i*150)
		}
	}

	data := map[string]any{
		"Stages":   Stages,
		"StageX":   x,
		"StageMid": mid,
		"FlowX":    flowX,
		"FlowEnd":  flowEnd,
		"Agents":   Agents,
		"Models":   Models,
		"Actions":  len(s.Actions()),
	}

	if err := pageTmpl.Execute(w, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// readGroups loads the groups view file.
func (s *Server) readGroups() ([]Group, error) {
	data, err := os.ReadFile(s.views)
	if err != nil {
		return nil, fmt.Errorf("read groups view: %w", err)
	}

	var gs []Group
	if err := json.Unmarshal(data, &gs); err != nil {
		return nil, fmt.Errorf("decode groups view: %w", err)
	}

	if !s.usage {
		for i := range gs {
			gs[i].Tokens, gs[i].USD = 0, 0
		}
	}
	return gs, nil
}

// stageMid returns the centre x of stage box i, which page draws and pointers aims at.
func stageMid(i int) int { return 80 + i*150 }

// pointers places each group's dot under the centre of its stage box, Intake when the stage is unknown.
func pointers(gs []Group) []pointer {
	ps := make([]pointer, 0, len(gs))
	for i, g := range gs {
		box := max(slices.Index(Stages, g.Stage), 0)
		ps = append(ps, pointer{ID: g.Group, X: stageMid(box), Y: 150 + i*34, Blocked: g.Stage == "Blocked"})
	}
	return ps
}

// patch writes one datastar-patch-elements event; each line of html gets its own data line,
// so a carriage return inside a value cannot end the field early.
func patch(w io.Writer, html, namespace string) error {
	var b strings.Builder
	b.WriteString("event: datastar-patch-elements\n")
	if namespace != "" {
		b.WriteString("data: namespace " + namespace + "\n")
	}
	for _, line := range strings.Split(lineEnds.Replace(html), "\n") {
		b.WriteString("data: elements " + line + "\n")
	}
	b.WriteString("\n")
	_, err := io.WriteString(w, b.String())
	return err
}

// events streams the table and the diagram as patches, once at connect and on every change.
// It returns when the client leaves or a write fails.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	var last []byte
	for {
		gs, err := s.readGroups()
		frame, _ := json.Marshal(gs)
		if err != nil {
			frame = []byte(err.Error())
		}

		if !bytes.Equal(frame, last) {
			var werr error
			if rows, dots, err := render(gs, err, s.usage); err != nil {
				werr = patch(w, `<div id="groups"><p class="Blocked">`+template.HTMLEscapeString(err.Error())+`</p></div>`, "")
			} else if werr = patch(w, rows, ""); werr == nil {
				werr = patch(w, dots, "svg")
			}
			if werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
			last = frame
		}

		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

// render returns the table and diagram HTML, or the first read or template error.
func render(gs []Group, readErr error, usage bool) (rows, dots string, err error) {
	if readErr != nil {
		return "", "", readErr
	}

	var rb, db bytes.Buffer
	if err := rowsTmpl.Execute(&rb, map[string]any{"Groups": gs, "Usage": usage}); err != nil {
		return "", "", fmt.Errorf("render groups table: %w", err)
	}
	if err := pointersTmpl.Execute(&db, pointers(gs)); err != nil {
		return "", "", fmt.Errorf("render stage diagram: %w", err)
	}
	return rb.String(), db.String(), nil
}

// record appends an action and answers with a patch of the action log.
func (s *Server) record(w http.ResponseWriter, action string) {
	s.mu.Lock()
	s.actions = append(s.actions, action)
	n := len(s.actions)
	s.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	_ = patch(w, fmt.Sprintf(`<p id="log">%d actions: %s queued</p>`, n, template.HTMLEscapeString(action)), "")
}

// resume records a resume request for the group; the real verb call waits on the engine.
func (s *Server) resume(w http.ResponseWriter, r *http.Request) {
	s.record(w, "resume "+r.PathValue("group"))
}

// setModel records a model change for the agent in the path, read from the page's signals.
// Applying it waits on the design.
func (s *Server) setModel(w http.ResponseWriter, r *http.Request) {
	agent := r.PathValue("agent")
	if !slices.Contains(Agents, agent) {
		http.Error(w, "unknown agent "+agent, http.StatusNotFound)
		return
	}

	var signals map[string]any
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSignals)).Decode(&signals); err != nil {
		http.Error(w, fmt.Sprintf("decode signals: %v", err), http.StatusBadRequest)
		return
	}

	model, _ := signals[agent].(string)
	if !slices.Contains(Models, model) {
		http.Error(w, "unknown model "+model, http.StatusBadRequest)
		return
	}
	s.record(w, "model "+agent+"="+model)
}
