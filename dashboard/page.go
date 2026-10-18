package dashboard

import (
	_ "embed"
	"html/template"
	"strings"
)

// pageCSS styles the page with tokens; the dark tokens follow the system theme.
//
//go:embed page.css
var pageCSS string

// pageHTML is the shell page; its css marker takes the styles on load.
//
//go:embed page.html
var pageHTML string

// viewHTML holds the elements the stream patches as HTML: header, tiles, cards, feed and meters.
//
//go:embed view.html
var viewHTML string

// diagramHTML holds the pipeline boxes and the group dots, patched in the svg namespace.
//
//go:embed diagram.html
var diagramHTML string

// funcs are the helpers the page templates call.
var funcs = template.FuncMap{"num": num, "usd": usd}

// pageTmpl is the shell page; on load it opens the one stream that fills every element with an id. Actions reuse the page's
// own query string, so the token is never written into the page.
var pageTmpl = template.Must(template.New("page").Funcs(funcs).Parse(strings.Replace(pageHTML, "/*css*/", pageCSS, 1)))

// viewTmpl renders the elements the stream patches as HTML: header, tiles, cards, feed and meters.
var viewTmpl = template.Must(template.New("view").Funcs(funcs).Parse(viewHTML))

// diagramTmpl renders the pipeline boxes and the group dots, patched in the svg namespace.
var diagramTmpl = template.Must(template.Must(pageTmpl.Clone()).New("dots").Parse(diagramHTML))
