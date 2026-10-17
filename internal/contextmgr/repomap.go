package contextmgr

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// RepoMap is the ranked symbol list that fills the repo_map slot.
type RepoMap struct {
	TreeHash string    // content fingerprint of the tree the map was built from
	Files    []MapFile // best-ranked first
}

// MapFile is one file with its rank and symbols.
type MapFile struct {
	Path    string
	Rank    int      // inbound references; a file the goal names counts three times
	Symbols []Symbol // functions, types, classes and exports found in the file
	Source  bool     // true for a Go file or a file of a language with a pattern file
	Named   bool     // true when the goal names the file
}

// Symbol is one named definition.
type Symbol struct {
	Name string
	Kind string // function, type, class or export
}

// langPattern is one language's file extensions and symbol patterns.
type langPattern struct {
	Name       string
	Extensions []string
	Symbols    []symbolPattern
}

// symbolPattern is a regular expression whose first group is a symbol name.
type symbolPattern struct {
	Kind string
	Re   *regexp.Regexp
}

// langFile is the JSON form of embed/langs/<lang>.json.
type langFile struct {
	Extensions []string `json:"extensions"`
	Symbols    []struct {
		Kind    string `json:"kind"`
		Pattern string `json:"pattern"`
	} `json:"symbols"`
}

// noFiles is what an empty map or layout renders as.
const noFiles = "no source files"

// Render returns the map as text, or noFiles when it holds no file.
func (rm RepoMap) Render() string {
	if len(rm.Files) == 0 {
		return noFiles
	}

	parts := make([]string, len(rm.Files))
	for i, f := range rm.Files {
		parts[i] = f.entry()
	}

	return strings.Join(parts, "\n")
}

// Fit drops the lowest-ranked files until the rendered map fits the token budget; a budget of 0
// or less keeps all.
func (rm RepoMap) Fit(budgetTokens int) RepoMap {
	if budgetTokens <= 0 {
		return rm
	}

	size := -1
	for _, f := range rm.Files {
		size += len(f.entry()) + 1
	}

	keep := len(rm.Files)
	for keep > 0 && (size+3)/4 > budgetTokens {
		keep--
		size -= len(rm.Files[keep].entry()) + 1
	}

	return RepoMap{TreeHash: rm.TreeHash, Files: rm.Files[:keep:keep]}
}

// entry renders one file of the map: its path and rank, then each symbol on its own line.
func (f MapFile) entry() string {
	var b strings.Builder

	b.WriteString(f.Path + " (" + strconv.Itoa(f.Rank) + ")")

	for _, s := range f.Symbols {
		b.WriteString("\n  " + s.Kind + " " + s.Name)
	}

	return b.String()
}

// loadLangs reads and compiles every pattern file under embed/langs, in file name order.
func loadLangs(fsys fs.FS) ([]langPattern, error) {
	ents, err := fs.ReadDir(fsys, "embed/langs")
	if err != nil {
		return nil, fmt.Errorf("read language patterns: %w", err)
	}

	var out []langPattern

	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}

		raw, err := fs.ReadFile(fsys, "embed/langs/"+e.Name())
		if err != nil {
			return nil, fmt.Errorf("read language patterns: %w", err)
		}

		var lf langFile
		if err := decodeStrict(raw, &lf); err != nil {
			return nil, fmt.Errorf("decode %s: %w", e.Name(), err)
		}

		lp := langPattern{Name: strings.TrimSuffix(e.Name(), ".json"), Extensions: lf.Extensions}

		for _, s := range lf.Symbols {
			re, err := regexp.Compile(s.Pattern)
			if err != nil {
				return nil, fmt.Errorf("pattern in %s: %w", e.Name(), err)
			}

			if re.NumSubexp() < 1 {
				return nil, fmt.Errorf("pattern %q in %s has no group", s.Pattern, e.Name())
			}

			lp.Symbols = append(lp.Symbols, symbolPattern{Kind: s.Kind, Re: re})
		}

		out = append(out, lp)
	}

	return out, nil
}

// languageOf returns the name of the language of a file path, or an empty string.
func languageOf(p string, langs []langPattern) (string, *langPattern) {
	ext := path.Ext(p)
	if ext == ".go" {
		return "go", nil
	}

	for i := range langs {
		for _, e := range langs[i].Extensions {
			if e == ext {
				return langs[i].Name, &langs[i]
			}
		}
	}

	return "", nil
}

// goSymbols lists the top-level definitions of a Go file through the Go parser, in file order.
func goSymbols(p string, src []byte) []Symbol {
	f, _ := parser.ParseFile(token.NewFileSet(), p, src, parser.SkipObjectResolution)
	if f == nil {
		return nil
	}

	var out []Symbol

	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			out = append(out, Symbol{Name: d.Name.Name, Kind: "function"})
		case *ast.GenDecl:
			for _, sp := range d.Specs {
				switch sp := sp.(type) {
				case *ast.TypeSpec:
					out = append(out, Symbol{Name: sp.Name.Name, Kind: "type"})
				case *ast.ValueSpec:
					for _, n := range sp.Names {
						if n.IsExported() {
							out = append(out, Symbol{Name: n.Name, Kind: "export"})
						}
					}
				}
			}
		}
	}

	return out
}

// patternSymbols lists the definitions a language's patterns find, matched line by line.
func patternSymbols(src []byte, lp *langPattern) []Symbol {
	var out []Symbol

	for _, line := range strings.Split(string(src), "\n") {
		for _, sp := range lp.Symbols {
			if m := sp.Re.FindStringSubmatch(line); len(m) > 1 && m[1] != "" {
				out = append(out, Symbol{Name: m[1], Kind: sp.Kind})

				break
			}
		}
	}

	return out
}

// wordSet returns the set of whole words (letters, digits and underscores) in text.
func wordSet(text string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, w := range strings.FieldsFunc(text, func(r rune) bool { return !isWordRune(r) }) {
		out[w] = struct{}{}
	}

	return out
}

// goalFields returns the goal split at spaces and quotes, so a path or file name stays whole.
func goalFields(goal string) map[string]struct{} {
	out := map[string]struct{}{}

	for _, f := range strings.FieldsFunc(goal, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune(",;:()[]{}\"'`", r)
	}) {
		out[strings.TrimRight(f, ".!?")] = struct{}{}
	}

	return out
}

// buildRepoMap ranks every file of the tree by how many other files name its symbols.
func buildRepoMap(t Tree, goal string, langs []langPattern) RepoMap {
	// goalBoost is how many times a file the goal names counts.
	const goalBoost = 3

	type scanned struct {
		file  MapFile
		words map[string]struct{}
	}

	var all []scanned

	df := map[string]int{}

	for _, p := range t.Paths() {
		lang, lp := languageOf(p, langs)
		if lang == "" {
			all = append(all, scanned{file: MapFile{Path: p}})

			continue
		}

		src, err := t.ReadFile(p)
		if err != nil {
			continue
		}

		var syms []Symbol
		if lp == nil {
			syms = goSymbols(p, src)
		} else {
			syms = patternSymbols(src, lp)
		}

		words := wordSet(string(src))
		for w := range words {
			df[w]++
		}

		all = append(all, scanned{
			file:  MapFile{Path: p, Symbols: dedupeSymbols(syms), Source: true},
			words: words,
		})
	}

	gf, gw := goalFields(goal), wordSet(goal)
	rm := RepoMap{TreeHash: t.Hash(), Files: make([]MapFile, 0, len(all))}

	for _, s := range all {
		named := map[string]bool{}

		for _, sym := range s.file.Symbols {
			if named[sym.Name] {
				continue
			}

			named[sym.Name] = true

			n := df[sym.Name]
			if _, own := s.words[sym.Name]; own {
				n--
			}

			s.file.Rank += n
		}

		if goalNames(s.file, gf, gw) {
			s.file.Named = true
			s.file.Rank = max(s.file.Rank*goalBoost, 1)
		}

		rm.Files = append(rm.Files, s.file)
	}

	tier := func(f MapFile) int {
		if f.Source || f.Named {
			return 0
		}

		return 1
	}

	sort.SliceStable(rm.Files, func(i, j int) bool {
		a, b := rm.Files[i], rm.Files[j]

		switch {
		case tier(a) != tier(b):
			return tier(a) < tier(b)
		case a.Rank != b.Rank:
			return a.Rank > b.Rank
		case a.Named != b.Named:
			return a.Named
		}

		return a.Path < b.Path
	})

	return rm
}

// dedupeSymbols drops repeated kind and name pairs, keeping file order.
func dedupeSymbols(in []Symbol) []Symbol {
	seen := map[Symbol]bool{}

	var out []Symbol

	for _, s := range in {
		if !seen[s] {
			seen[s] = true

			out = append(out, s)
		}
	}

	return out
}

// goalNames reports whether the goal names the file by path, base name or one of its symbols.
func goalNames(f MapFile, fields, words map[string]struct{}) bool {
	if _, ok := fields[f.Path]; ok {
		return true
	}

	if _, ok := fields[path.Base(f.Path)]; ok {
		return true
	}

	for _, s := range f.Symbols {
		if _, ok := words[s.Name]; ok {
			return true
		}
	}

	return false
}

// renderLayout lists the tree's paths, one per line; over the token budget, deep folders collapse
// to file counts. A budget of 0 or less lists every path.
func renderLayout(t Tree, budget int) string {
	paths := t.Paths()
	if len(paths) == 0 {
		return noFiles
	}

	full := strings.Join(paths, "\n")
	if budget <= 0 || CountTokens(full) <= budget {
		return full
	}

	deepest := 0
	for _, p := range paths {
		deepest = max(deepest, strings.Count(p, "/")+1)
	}

	for d := deepest - 1; d > 1; d-- {
		if out := collapseLayout(paths, d); CountTokens(out) <= budget {
			return out
		}
	}

	return collapseLayout(paths, 1)
}

// collapseLayout keeps paths of at most depth segments and folds deeper ones into "dir/ (N files)"
// lines.
func collapseLayout(paths []string, depth int) string {
	counts := map[string]int{}

	var keys []string

	for _, p := range paths {
		key := p
		if segs := strings.SplitN(p, "/", depth+1); len(segs) > depth {
			key = strings.Join(segs[:depth], "/") + "/"
		}

		if counts[key] == 0 {
			keys = append(keys, key)
		}

		counts[key]++
	}

	sort.Strings(keys)

	lines := make([]string, len(keys))

	for i, k := range keys {
		switch {
		case !strings.HasSuffix(k, "/"):
			lines[i] = k
		case counts[k] == 1:
			lines[i] = k + " (1 file)"
		default:
			lines[i] = fmt.Sprintf("%s (%d files)", k, counts[k])
		}
	}

	return strings.Join(lines, "\n")
}
