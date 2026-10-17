// Package format turns the harness's structured messages into the text a person reads, from
// templates and facts alone.
package format

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"strings"
	"text/template"
	"text/template/parse"

	komodo "github.com/rdevitto86/komodo-agentic-harness-coding"
)

// Message is the JSON that FormatJSON reads.
type Message struct {
	// reply.run, error.over_limit, event.run-status, ...
	Shape string          `json:"shape"`
	Code  string          `json:"code,omitempty"` // set for error.* and needs.* shapes
	Facts json.RawMessage `json:"facts"`          // decoded strictly into the shape's fact struct
}

// Rendered is the text FormatJSON returns.
type Rendered struct {
	Verdict string // line one of Report with code spans and bold markers removed
	Report  string // the full text for the profile, wrapped and coloured
}

// Profile names an output template set.
type Profile string

const (
	ProfileAdhd     Profile = "adhd"
	ProfileStandard Profile = "standard"
)

// ColorMode is whether to add colour codes; the CLI resolves "auto" before it calls New.
type ColorMode string

const (
	ColorOn  ColorMode = "on"
	ColorOff ColorMode = "off"
)

// Options configure one Formatter.
type Options struct {
	Profile Profile
	Color   ColorMode
	TurnEnd bool // print the four closing headings when they hold items
}

// Formatter renders messages for one profile, colour mode and closing-heading setting.
type Formatter struct {
	opts  Options
	tmpls map[string]*template.Template // shape name -> parsed template
}

var (
	ErrShapeUnknown = errors.New("no template has that shape name")
	ErrCodeUnknown  = errors.New("code is not in the code list")
	ErrFactsInvalid = errors.New("facts do not decode into the shape's fact struct")
)

// bannedFuncs are the built-in template functions a template may not call.
var bannedFuncs = []string{"call", "html", "js", "print", "printf", "println", "urlquery"}

// New parses every embedded template for the profile and fails on any that does not parse.
func New(o Options) (*Formatter, error) {
	sub, err := fs.Sub(komodo.Embedded, "embed/output")
	if err != nil {
		return nil, fmt.Errorf("open embedded output files: %w", err)
	}

	return newFromFS(sub, o)
}

// newFromFS parses the templates of the profile found under fsys, which is rooted at the output
// folder.
func newFromFS(fsys fs.FS, o Options) (*Formatter, error) {
	if o.Profile != ProfileAdhd && o.Profile != ProfileStandard {
		return nil, fmt.Errorf("unknown profile %q", o.Profile)
	}

	if o.Color != "" && o.Color != ColorOn && o.Color != ColorOff {
		return nil, fmt.Errorf("unresolved colour mode %q", o.Color)
	}

	f := &Formatter{opts: o, tmpls: make(map[string]*template.Template, len(shapes))}
	for _, shape := range shapeNames() {
		name := string(o.Profile) + "/" + shape + ".tmpl"

		text, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, fmt.Errorf("read template %s: %w", name, err)
		}

		tm, err := parseTemplate(shape, string(text))
		if err != nil {
			return nil, fmt.Errorf("parse template %s: %w", name, err)
		}

		if err := checkFuncs(tm); err != nil {
			return nil, fmt.Errorf("template %s: %w", name, err)
		}

		f.tmpls[shape] = tm
	}

	return f, nil
}

// FormatJSON reads one Message and returns the rendered text for the profile.
func (f *Formatter) FormatJSON(msg []byte) (Rendered, error) {
	var m Message
	if err := json.Unmarshal(msg, &m); err != nil {
		return Rendered{}, fmt.Errorf("decode message: %w", err)
	}

	info, ok := shapes[m.Shape]
	if !ok {
		return Rendered{}, fmt.Errorf("%w: %q", ErrShapeUnknown, m.Shape)
	}

	if err := checkCode(m, info.coded); err != nil {
		return Rendered{}, err
	}

	facts := info.facts()
	if err := decodeFacts(m.Facts, facts); err != nil {
		return Rendered{}, fmt.Errorf("%w: %v", ErrFactsInvalid, err)
	}

	if err := checkFactsCode(m, facts); err != nil {
		return Rendered{}, err
	}

	escapeFacts(reflect.ValueOf(facts))

	text, err := f.execute(m.Shape, reflect.ValueOf(facts).Elem().Interface())
	if err != nil {
		return Rendered{}, err
	}

	wrapped := wrap(tidy(text), wrapWidth)

	r := Rendered{Verdict: verdictOf(wrapped), Report: wrapped}
	if f.opts.Color == ColorOn {
		r.Report = colorize(wrapped)
	}

	return r, nil
}

// execute runs the shape's template, then its closing headings when they are on and hold items.
func (f *Formatter) execute(shape string, facts any) (string, error) {
	// turnEndName is the template a file defines for its closing headings.
	const turnEndName = "turnend"

	tm := f.tmpls[shape]

	var buf bytes.Buffer
	if err := tm.Execute(&buf, facts); err != nil {
		return "", fmt.Errorf("render %s: %w", shape, err)
	}

	if end := tm.Lookup(turnEndName); end != nil && f.opts.TurnEnd {
		var eb bytes.Buffer
		if err := end.Execute(&eb, facts); err != nil {
			return "", fmt.Errorf("render %s closing headings: %w", shape, err)
		}

		if strings.TrimSpace(eb.String()) != "" {
			buf.WriteString("\n\n" + eb.String())
		}
	}

	return buf.String(), nil
}

// OutputStyle returns the style text of one profile for the AGENTS.md block.
func OutputStyle(p Profile) (string, error) {
	if p != ProfileAdhd && p != ProfileStandard {
		return "", fmt.Errorf("unknown profile %q", p)
	}

	b, err := komodo.Embedded.ReadFile("embed/output/" + string(p) + ".md")
	if err != nil {
		return "", fmt.Errorf("read style text: %w", err)
	}

	return string(b), nil
}

// parseTemplate builds one template with the fixed options and function map.
func parseTemplate(name, text string) (*template.Template, error) {
	return template.New(name).Option("missingkey=error").Funcs(funcs).Parse(text)
}

// checkFuncs fails when a template, or a template it defines, calls a banned built-in function.
func checkFuncs(tm *template.Template) error {
	var err error

	for _, t := range tm.Templates() {
		if t.Tree == nil {
			continue
		}

		walkNodes(t.Root, func(n parse.Node) {
			if id, ok := n.(*parse.IdentifierNode); ok && err == nil {
				for _, b := range bannedFuncs {
					if id.Ident == b {
						err = fmt.Errorf("calls %s, which a template may not call", b)
					}
				}
			}
		})
	}

	return err
}

// walkNodes calls fn on n and on every node under it.
func walkNodes(n parse.Node, fn func(parse.Node)) {
	if n == nil || reflect.ValueOf(n).IsNil() {
		return
	}

	fn(n)

	switch x := n.(type) {
	case *parse.ListNode:
		for _, c := range x.Nodes {
			walkNodes(c, fn)
		}
	case *parse.ActionNode:
		walkNodes(x.Pipe, fn)
	case *parse.PipeNode:
		for _, c := range x.Cmds {
			walkNodes(c, fn)
		}
	case *parse.CommandNode:
		for _, c := range x.Args {
			walkNodes(c, fn)
		}
	case *parse.ChainNode:
		walkNodes(x.Node, fn)
	case *parse.IfNode:
		walkBranch(&x.BranchNode, fn)
	case *parse.RangeNode:
		walkBranch(&x.BranchNode, fn)
	case *parse.WithNode:
		walkBranch(&x.BranchNode, fn)
	case *parse.TemplateNode:
		walkNodes(x.Pipe, fn)
	}
}

// walkBranch visits the pipeline and both lists of an if, range or with node.
func walkBranch(b *parse.BranchNode, fn func(parse.Node)) {
	walkNodes(b.Pipe, fn)
	walkNodes(b.List, fn)
	walkNodes(b.ElseList, fn)
}

// checkCode refuses a code off its list, or one that differs from the code the shape name ends in.
func checkCode(m Message, coded bool) error {
	known := IsCode(m.Code)
	if strings.HasPrefix(m.Shape, "needs.") {
		known = IsBlockReason(m.Code)
	}

	if (coded || m.Code != "") && !known {
		return fmt.Errorf("%w: %q", ErrCodeUnknown, m.Code)
	}

	for _, prefix := range []string{"error.", "needs.", "code."} {
		if want, ok := strings.CutPrefix(m.Shape, prefix); ok && m.Code != want {
			return fmt.Errorf(
				"%w: code %q does not match shape %q",
				ErrCodeUnknown,
				m.Code,
				m.Shape,
			)
		}
	}

	return nil
}

// checkFactsCode fills an absent facts.code from the message code and refuses one that differs from
// it.
func checkFactsCode(m Message, facts any) error {
	var code *string

	switch f := facts.(type) {
	case *ErrorFacts:
		code = &f.Code
	case *NeedsFacts:
		code = &f.Code
	default:
		return nil
	}

	if *code == "" {
		*code = m.Code
	}

	if *code != m.Code {
		return fmt.Errorf(
			"%w: facts code %q does not match message code %q",
			ErrCodeUnknown,
			*code,
			m.Code,
		)
	}

	return nil
}

// decodeFacts decodes the facts object into dst, refusing unknown fields; absent facts decode as an
// empty object.
func decodeFacts(raw json.RawMessage, dst any) error {
	if t := bytes.TrimSpace(raw); len(t) == 0 || string(t) == "null" {
		raw = []byte("{}")
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	dec.UseNumber()

	return dec.Decode(dst)
}
