// Package dashboard is a proof of concept for a local browser view of a run.
// One server-sent event stream patches the tiles, group cards, stage diagram, event feed and
// meters; the embedded Datastar library applies it.
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

// Server serves the dashboard from one view file and records requested actions.
type Server struct {
	token   string         // per-start secret every request must carry
	usage   bool           // show tokens and cost; mirrors the usage tracking setting
	views   string         // path of the view JSON
	mux     *http.ServeMux // routes
	mu      sync.Mutex     // guards actions
	actions []string       // actions requested so far, newest last
}

var (
	// assets holds the Datastar library, the only client-side code, and its license.
	//
	//go:embed assets/datastar.js assets/DATASTAR-LICENSE.md
	assets embed.FS

	// maxSignals caps a posted signals body; the page sends four short strings.
	maxSignals int64 = 64 << 10

	// lineEnds turns every line ending an event stream reader accepts into a plain newline.
	lineEnds = strings.NewReplacer("\r\n", "\n", "\r", "\n")
)

// New builds a server that reads the view from viewsPath and requires token on every request.
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

// page writes the shell page; the diagram leaves room for four dots stacked under one stage.
func (s *Server) page(w http.ResponseWriter, _ *http.Request) {
	v, _ := s.readView()
	data := map[string]any{
		"Boxes":   boxes(make([]int, len(Stages))),
		"Height":  110 + min(max(len(v.Groups), 1), 4)*26,
		"Agents":  Agents,
		"Models":  Models,
		"Actions": len(s.Actions()),
	}

	if err := pageTmpl.Execute(w, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// readView loads the view file; usage figures are dropped when tracking is off.
func (s *Server) readView() (View, error) {
	var v View

	data, err := os.ReadFile(s.views)
	if err != nil {
		return v, fmt.Errorf("read dashboard view: %w", err)
	}

	if err := json.Unmarshal(data, &v); err != nil {
		return v, fmt.Errorf("decode dashboard view: %w", err)
	}

	if !s.usage {
		for i := range v.Groups {
			ss := &v.Groups[i].Sessions
			for _, p := range []*Session{&ss.Builder, &ss.Tester, &ss.Reviewer} {
				p.Tokens, p.USD = 0, 0
			}
		}
	}

	return v, nil
}

// events streams the page's elements as patches, once at connect and on every change.
// It returns when the client leaves or a write fails.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	var last []byte

	for {
		v, err := s.readView()

		frame, _ := json.Marshal(v)
		if err != nil {
			frame = []byte(err.Error())
		}

		if !bytes.Equal(frame, last) {
			var werr error
			if page, svg, err := render(v, err, s.usage); err != nil {
				msg := template.HTMLEscapeString(err.Error())
				werr = patch(w, `<div id="groups"><p class="err">`+msg+`</p></div>`, "")
			} else if werr = patch(w, page, ""); werr == nil {
				werr = patch(w, svg, "svg")
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

// record appends an action and answers with a patch of the action log.
func (s *Server) record(w http.ResponseWriter, action string) {
	s.mu.Lock()
	s.actions = append(s.actions, action)
	n := len(s.actions)
	s.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	_ = patch(
		w,
		fmt.Sprintf(
			`<p id="log" class="muted">%d actions: %s queued</p>`,
			n,
			template.HTMLEscapeString(action),
		),
		"",
	)
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
	if err := json.NewDecoder(http.MaxBytesReader(
		w,
		r.Body,
		maxSignals,
	)).Decode(&signals); err != nil {
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

// Serve listens on a loopback port, prints the page URL and serves until the listener fails.
// Port 0 picks a free one.
func Serve(s *Server, port int) error {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("listen on loopback: %w", err)
	}

	fmt.Printf("http://%s/?t=%s\n", ln.Addr(), s.token)

	return http.Serve(ln, s)
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

// render returns the HTML parts and the diagram parts of the view, or the first read or template
// error.
func render(v View, readErr error, usage bool) (page, svg string, err error) {
	if readErr != nil {
		return "", "", readErr
	}

	m := map[string]any{
		"Run": v.Run, "Started": v.Run.Started.Local().Format("Jan 2, 15:04"), "Usage": usage,
		"KPIs":   kpis(v),
		"Cards":  cards(v.Groups),
		"Events": eventRows(v.Events),
		"Links":  linkRows(v.Resources),
	}

	var pb, sb bytes.Buffer
	for _, name := range []string{"runhead", "kpis", "groups", "events", "resources"} {
		if err := viewTmpl.ExecuteTemplate(&pb, name, m); err != nil {
			return "", "", fmt.Errorf("render %s: %w", name, err)
		}

		pb.WriteByte('\n')
	}

	d := map[string]any{"Boxes": boxes(stageCounts(v.Groups)), "Pointers": pointers(v.Groups)}
	if err := diagramTmpl.ExecuteTemplate(&sb, "dots", d); err != nil {
		return "", "", fmt.Errorf("render stage diagram: %w", err)
	}

	return pb.String(), sb.String(), nil
}
