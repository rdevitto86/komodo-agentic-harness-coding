// Package dashboard is a proof of concept for a local browser view of active groups.
// It serves server-rendered HTML fragments that the embedded htmx library polls and posts.
package dashboard

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"os"
	"slices"
	"sync"
	"time"
)

var (
	// assets holds htmx 2.0.11 and the pipeline diagram script, the only client-side code.
	//
	//go:embed assets/htmx.min.js assets/diagram.js
	assets embed.FS

	// Stages are the pipeline stages in order, as the diagram draws them.
	Stages = []string{"Intake", "Build", "Check", "Verify", "Ship", "Blocked"}

	// Agents are the seven agents whose model the page offers to change.
	Agents = []string{"builder", "reviewer", "planner", "tester", "architect", "researcher", "scout"}

	// Models are the agent `model` values the page offers, as the config agent group names them.
	Models = []string{"opus", "sonnet", "haiku", "opus-prev"}
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

// pageTmpl is the shell page; its one div polls the groups fragment every 2 seconds.
var pageTmpl = template.Must(template.New("page").Parse(`<!doctype html>
<meta charset="utf-8"><title>Komodo</title>
<script src="/assets/htmx.min.js"></script>
<style>
body{font:14px system-ui;margin:16px}td,th{padding:4px 8px;text-align:left;vertical-align:top}.Blocked{color:#c33}
.stage{fill:#f4f6fa;stroke:#8a94a6;rx:8}.stage-label{font-weight:600;text-anchor:middle}
.flow{stroke:#5b8def;stroke-width:2;stroke-dasharray:6 6;animation:flow 1s linear infinite}
@keyframes flow{to{stroke-dashoffset:-12}}
.pointer{transition:transform .8s ease-in-out}.pointer circle{fill:#5b8def}.pointer.blocked circle{fill:#c33}
.pointer text{font-size:11px}
</style>
<h1>Komodo runs</h1>
<svg id="pipeline" data-token="{{.Token}}" viewBox="0 0 900 260" width="900" height="260">
{{range $i, $s := .Stages}}<rect class="stage" data-stage="{{$s}}" x="{{index $.StageX $i}}" y="20" width="120" height="70"/>
<text class="stage-label" x="{{index $.StageMid $i}}" y="60">{{$s}}</text>
{{end}}{{range $i, $x := .FlowX}}<line class="flow" x1="{{$x}}" y1="55" x2="{{index $.FlowEnd $i}}" y2="55"/>
{{end}}<g id="pointers"></g>
</svg>
<p>{{range .Agents}}<label>{{.}} <select name="{{.}}" hx-post="/agents/model?t={{$.Token}}" hx-target="#log">{{range $.Models}}<option>{{.}}</option>{{end}}</select></label> {{end}}</p>
<div hx-get="/groups?t={{.Token}}" hx-trigger="load, every 2s"></div>
<script src="/assets/diagram.js"></script>`))

// rowsTmpl renders the groups table; a resume button appears only on blocked groups.
var rowsTmpl = template.Must(template.New("rows").Parse(`<table>
<tr><th>Repo<th>Group<th>Stage<th>Agent<th>Model{{if .Usage}}<th>Tokens<th>USD{{end}}<th>Block<th></tr>
{{range .Groups}}<tr><td>{{.Repo}}<td>{{.Group}}<td class="{{.Stage}}">{{.Stage}}<td>{{.Agent}}<td>{{.Model}}{{if $.Usage}}<td>{{.Tokens}}<td>{{printf "%.2f" .USD}}{{end}}<td>{{.Block}}
<td>{{if .Block}}<button hx-post="/groups/{{.Group}}/resume?t={{$.Token}}" hx-target="#log">resume</button>{{end}}
<details><summary>brief</summary><pre>{{.Brief}}</pre></details></tr>
{{end}}</table>
<p id="log">{{len .Actions}} actions</p>`))

// Server serves the dashboard from one groups view file and records requested actions.
type Server struct {
	token   string         // per-start secret every request must carry
	usage   bool           // show tokens and cost; mirrors the usage tracking setting
	views   string         // path of the groups view JSON
	mux     *http.ServeMux // routes
	mu      sync.Mutex     // guards actions
	actions []string       // actions requested so far, newest last
}

// New builds a server that reads groups from viewsPath and requires token on every request.
// usage shows tokens and cost; when false they are dropped before rendering or streaming.
func New(token, viewsPath string, usage bool) *Server {
	s := &Server{token: token, views: viewsPath, usage: usage, mux: http.NewServeMux()}
	s.mux.Handle("GET /assets/", http.FileServerFS(assets))
	s.mux.HandleFunc("GET /{$}", s.guard(s.page))
	s.mux.HandleFunc("GET /groups", s.guard(s.groups))
	s.mux.HandleFunc("POST /groups/{group}/resume", s.guard(s.resume))
	s.mux.HandleFunc("GET /events", s.guard(s.events))
	s.mux.HandleFunc("POST /agents/model", s.guard(s.setModels))
	return s
}

// ServeHTTP routes a request to the page, a fragment, an action or an asset.
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
		mid = append(mid, 80+i*150)
		if i < len(Stages)-2 {
			flowX, flowEnd = append(flowX, 140+i*150), append(flowEnd, 170+i*150)
		}
	}

	data := map[string]any{
		"Token":    s.token,
		"Stages":   Stages,
		"StageX":   x,
		"StageMid": mid,
		"FlowX":    flowX,
		"FlowEnd":  flowEnd,
		"Agents":   Agents,
		"Models":   Models,
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

// groups reads the groups view and writes the table fragment.
func (s *Server) groups(w http.ResponseWriter, _ *http.Request) {
	gs, err := s.readGroups()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := rowsTmpl.Execute(w, map[string]any{
		"Groups": gs, "Token": s.token, "Usage": s.usage, "Actions": s.Actions()},
	); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// resume records a resume request for the group; the real verb call waits on the engine.
func (s *Server) resume(w http.ResponseWriter, r *http.Request) {
	action := "resume " + r.PathValue("group")
	s.mu.Lock()
	s.actions = append(s.actions, action)
	n := len(s.actions)
	s.mu.Unlock()
	fmt.Fprintf(w, "%d actions: %s queued", n, template.HTMLEscapeString(action))
}

// events streams the groups view as server-sent events, once at connect and on every change.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	var last []byte
	for {
		if gs, err := s.readGroups(); err == nil {
			frame, _ := json.Marshal(gs)
			if !bytes.Equal(frame, last) {
				fmt.Fprintf(w, "data: %s\n\n", frame)
				if flusher != nil {
					flusher.Flush()
				}
				last = frame
			}
		}

		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

// setModels records a model change for every agent the form names; applying it waits on the design.
func (s *Server) setModels(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, fmt.Sprintf("parse form: %v", err), http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	for _, a := range Agents {
		if m := r.PostForm.Get(a); slices.Contains(Models, m) {
			s.actions = append(s.actions, "model "+a+"="+m)
		}
	}

	n := len(s.actions)
	s.mu.Unlock()
	fmt.Fprintf(w, "%d actions recorded", n)
}
