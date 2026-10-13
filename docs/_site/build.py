#!/usr/bin/env python3
"""Render docs/hld.md and docs/lld.md as a browsable site.

Usage: python3 docs/_site/build.py build|serve [--engine mkdocs|zensical] [--ref BRANCH]

The Markdown in docs/ stays plain. This script adds the depth at build time:
one page per chapter and per appendix, glossary hover definitions, plain-terms
callouts and collapsed row explanations. Output goes to .docs-build/site/.
serve reads a snapshot of docs/; rerun it after editing the source.
"""
import argparse, json, re, shutil, subprocess, sys, unicodedata
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
DOCS = ROOT / "docs"
HERE = Path(__file__).resolve().parent
WORK = ROOT / ".docs-build"
REPO = "https://github.com/rdevitto86/komodo-agentic-harness-coding/blob"
END = "*End of the read."
TITLES = {"hld": "High-Level Design", "lld": "Low-Level Design"}
ADDR = "127.0.0.1:8765"
LIST = re.compile(r"^(\s*)([-*+] |\d+\. )")
CHAPTER = re.compile(r"^(## (?:\d+|T)\. .*)$", re.M)
PAGES = {}


def fenced(lines):
    """Yield (line, inside_fence) so transforms can skip code and Mermaid blocks."""
    inside = False
    for line in lines:
        if line.lstrip().startswith("```"):
            yield line, True
            inside = not inside
        else:
            yield line, inside


def lists(text):
    """Double list indents to 4-space nesting and open a blank line before a list that follows prose."""
    out, prev = [], ""
    for line, code in fenced(text.split("\n")):
        m = None if code else LIST.match(line)
        if m:
            line = " " * (2 * len(m.group(1))) + line[len(m.group(1)):]
            if prev.strip() and not LIST.match(prev) and not prev.startswith(" "):
                out.append("")
        out.append(line)
        prev = line if not code else ""
    return "\n".join(out)


def callouts(text):
    """Turn plain-terms paragraphs into callouts and row-explanation lists into collapsed blocks."""
    out, lines, i, inside = [], text.split("\n"), 0, False
    while i < len(lines):
        line = lines[i]
        if line.lstrip().startswith("```"):
            inside = not inside
        m = None if inside else re.match(r"^\*\*In plain terms\.\*\* (.*)", line)
        lead = None if inside else re.match(r"^\*\*What each one does\*\*(.*)$", line)
        if m:
            out += ['!!! abstract "In plain terms"', "    " + m.group(1)]
            i += 1
        elif lead:
            out.append(f'??? info {json.dumps(("What each one does" + lead.group(1)).strip())}')
            i += 1
            while i < len(lines) and (LIST.match(lines[i]) or not lines[i].strip()):
                nxt = lines[i + 1] if i + 1 < len(lines) else ""
                if not lines[i].strip() and not LIST.match(nxt):
                    break
                out.append("    " + lines[i] if lines[i].strip() else "")
                i += 1
        else:
            out.append(line)
            i += 1
    return "\n".join(out)


def slug(text):
    """Return the anchor id Python-Markdown gives a heading with this text."""
    text = unicodedata.normalize("NFKD", text).encode("ascii", "ignore").decode()
    return re.sub(r"[-\s]+", "-", re.sub(r"[^\w\s-]", "", text).strip().lower())


def sentences(text):
    """Split prose into sentences, leaving code spans, links and abbreviations whole."""
    keep = []
    def hold(m):
        keep.append(m.group(0))
        return f"\x00{len(keep) - 1}\x00"
    masked = re.sub(r"`[^`]*`|\[[^\]]*\]\([^)]*\)|\b(?:e\.g|i\.e|vs|etc)\.", hold, text)
    parts = re.split(r"(?<=[.!?])\s+(?=[A-Z0-9\"(*\x00])", masked)
    return [re.sub(r"\x00(\d+)\x00", lambda m: keep[int(m.group(1))], x) for x in parts if x.strip()]


def clauses(text):
    """Split a long semicolon chain into its clauses, leaving code spans and links whole; one item means no chain."""
    keep = []
    def hold(m):
        keep.append(m.group(0))
        return f"\x00{len(keep) - 1}\x00"
    masked = re.sub(r"`[^`]*`|\[[^\]]*\]\([^)]*\)", hold, text)
    parts = masked.split("; ")
    if len(parts) < 4 or len(text) < 400:
        return [text]
    return [re.sub(r"\x00(\d+)\x00", lambda m: keep[int(m.group(1))], x).rstrip(".") for x in parts]


def chunks(items, size=3):
    """Cut a list into the fewest runs of at most size items, keeping runs even."""
    n = -(-len(items) // size)
    cut = [round(i * len(items) / n) for i in range(n + 1)]
    return [items[cut[i]:cut[i + 1]] for i in range(n)]


def readable(text):
    """Break walls of text: walkthroughs become numbered steps, long paragraphs short ones, long items a lead plus sub-points."""
    out = []
    for line, code in fenced(text.split("\n")):
        plain = not code and line.strip() and not re.match(r"^\s*(\||#|!!!|\?\?\?|<|\{:|\*\[|```)", line)
        item = LIST.match(line) if plain else None
        if plain and line.startswith("**Reading the diagram.**"):
            out += ["**Reading the diagram**", ""] + [f"{i}. {x}" for i, x in enumerate(sentences(line[24:].strip()), 1)]
        elif plain and not item and len(clauses(line.strip())) > 1:
            pad = re.match(r"^\s*", line).group(0)
            parts = clauses(line.strip())
            lead = re.match(r"^(.{3,80}?:) (.*)$", parts[0])
            if lead and "`" not in lead.group(1):
                out += [pad + lead.group(1), ""]
                parts[0] = lead.group(2)
            else:
                out.append("")
            out += [f"{pad}- {x[0].upper() + x[1:]}." for x in parts]
        elif item and (len(sentences(line[item.end():])) > 4 or
                       (len(sentences(line[item.end():])) > 3 and len(line) > 450)):
            parts = sentences(line[item.end():])
            pad = " " * (len(item.group(1)) + 4)
            out.append(line[:item.end()] + " ".join(parts[:2]))
            out += [f"{pad}- {x}" for x in parts[2:]]
        elif plain and not item and len(sentences(line.strip())) > 3:
            pad = re.match(r"^\s*", line).group(0)
            for i, run in enumerate(chunks(sentences(line.strip()))):
                out += ([pad.rstrip() if pad else ""] if i else []) + [pad + " ".join(run)]
        else:
            out.append(line)
    return "\n".join(out)


def labels(text):
    """Tag lines that are bold and nothing else, such as "What matters", so the theme styles them as labels."""
    out = []
    for line, code in fenced(text.split("\n")):
        out.append(line)
        if not code and re.fullmatch(r"\*\*[^*]+\*\*:?", line.strip()) and not line.startswith(" "):
            out += ["{: .k-label }", ""]
    return "\n".join(out)


def links(text, doc, ref):
    """Point chapter and appendix anchors, cross-document links and sibling files at the generated pages."""
    other = "lld" if doc == "hld" else "hld"
    text = re.sub(r"\]\(#appendix-([a-z])-[^)]*\)", r"](appendix-\1.md)", text)
    text = re.sub(rf"\]\({other}\.md#appendix-([a-z])-[^)]*\)", rf"](../{other}/appendix-\1.md)", text)
    text = re.sub(r"\]\(#([^)]+)\)", lambda m: f"]({PAGES[doc][m.group(1)]})" if m.group(1) in PAGES[doc] else m.group(0), text)
    text = re.sub(rf"\]\({other}\.md#([^)]+)\)",
                  lambda m: f"](../{other}/{PAGES[other][m.group(1)]})" if m.group(1) in PAGES[other] else m.group(0), text)
    text = re.sub(rf"\]\({other}\.md\)", f"](../{other}/index.md)", text)
    text = re.sub(r"\]\(\.\./([^)#]+)\)", rf"]({REPO}/{ref}/\1)", text)
    return re.sub(r"\]\((?!https?:|#|\.\./|appendix-|chapter-|index\.md)([^)]+\.md(?:#[^)]*)?)\)", r"](../\1)", text)


def page(title, text, doc, ref):
    """Return one page: an H1, then the text with lists, callouts and links fixed."""
    lines = text.strip("\n").split("\n")
    if lines and re.match(r"^#{2,4} ", lines[0]) and lines[0].lstrip("#").strip() == title:
        lines = lines[1:]
    return f"# {title}\n\n" + links(labels(readable(callouts(lists("\n".join(lines))))), doc, ref) + "\n"


def cells(row):
    """Split a table row on unescaped pipes."""
    return [c.strip().replace("\\|", "|") for c in re.split(r"(?<!\\)\|", row.strip().strip("|"))]


def glossary():
    """Build hover definitions from the HLD's full glossary table, one per listed term."""
    hld = (DOCS / "hld.md").read_text()
    if "## Appendix C. Full Glossary" not in hld:
        sys.exit("docs/hld.md has no '## Appendix C. Full Glossary' heading")
    section = hld.split("## Appendix C. Full Glossary", 1)[1].split("\n## ", 1)[0]
    out = []
    for row in (l for l in section.split("\n") if l.startswith("|")):
        c = cells(row)
        if len(c) < 2 or c[0] == "Term" or set(c[0]) <= set("-: "):
            continue
        meaning = c[1].replace("`", "").replace("]", ")")
        out += [f"*[{t}]: {meaning}" for t in (x.strip().strip("`") for x in c[0].split(",")) if t]
    return out


def chapters(doc):
    """Cut the doc into (file, title, text) pages: the title page with chapter 0, each later chapter, each appendix."""
    text = (DOCS / f"{doc}.md").read_text()
    if END not in text:
        sys.exit(f"docs/{doc}.md has no end-of-read marker")
    body, apps = text.split(END, 1)
    parts = CHAPTER.split(body.rstrip().rstrip("-"))
    if len(parts) < 3:
        sys.exit(f"docs/{doc}.md has no '## N. ' chapter headings")
    title = re.match(r"^# (.*)$", parts[0].strip())
    out = [("index.md", title.group(1) if title else TITLES[doc], re.sub(r"^# .*$", "", parts[0], flags=re.M) + parts[1] + parts[2])]
    for head, content in zip(parts[3::2], parts[4::2]):
        number = re.match(r"## (\d+|T)\.", head).group(1).lower()
        out.append((f"chapter-{number}.md", head[3:], content))
    parts = re.split(r"^(## Appendix [A-Z]\..*)$", apps, flags=re.M)
    for head, content in zip(parts[1::2], parts[2::2]):
        letter = re.match(r"## Appendix ([A-Z])", head).group(1).lower()
        out.append((f"appendix-{letter}.md", head[3:], content.rstrip().rstrip("-")))
    PAGES[doc] = {slug(t): f for f, t, _ in out[1:]} | {slug(CHAPTER.search(body).group(1)[3:]): "index.md"}
    return out


def split(doc, pages, ref):
    """Write the doc's pages; return its nav: chapters first, then the appendices as one group."""
    folder = WORK / "docs" / doc
    folder.mkdir(parents=True)
    nav, apps = [], []
    for name, title, text in pages:
        (folder / name).write_text(page(title, text, doc, ref))
        label = CHAPTER.search(text).group(1)[3:] if name == "index.md" else title
        (apps if name.startswith("appendix-") else nav).append({label: f"{doc}/{name}"})
    return nav + [{"Appendices": apps}]


def config(nav):
    """Return mkdocs.yml text: Material theme, dark mode, tooltips, Mermaid, and the nav."""
    lines = [
        "site_name: Komodo Design Docs", "docs_dir: docs", "site_dir: site",
        "extra_css: [assets/theme.css]",
        "extra_javascript:", "  - path: assets/diagrams.js", "    type: module",
        "theme:", "  name: material", "  font: false", "  palette:",
        '    - media: "(prefers-color-scheme: light)"', "      scheme: default",
        "      toggle: {icon: material/brightness-7, name: Dark mode}",
        '    - media: "(prefers-color-scheme: dark)"', "      scheme: slate",
        "      toggle: {icon: material/brightness-4, name: Light mode}",
        "  features: [navigation.sections, navigation.footer, navigation.top, toc.follow, content.tooltips, search.highlight]",
        "markdown_extensions:",
        "  - abbr", "  - attr_list", "  - admonition", "  - tables",
        "  - toc: {permalink: true}", "  - pymdownx.details",
        "  - pymdownx.snippets: {auto_append: [includes/glossary.md]}",
        "  - pymdownx.superfences:", "      custom_fences:", "        - name: mermaid",
        "          class: diagram", "          format: !!python/name:pymdownx.superfences.fence_div_format",
        "nav:",
    ]
    def emit(entries, depth):
        for e in entries:
            for t, p in e.items():
                pad = "  " * depth
                if isinstance(p, list):
                    lines.append(f"{pad}- {json.dumps(t)}:")
                    emit(p, depth + 1)
                else:
                    lines.append(f"{pad}- {json.dumps(t)}: {p}")
    emit([{TITLES[doc]: entries} for doc, entries in nav.items()], 1)
    return "\n".join(lines) + "\n"


def prepare(ref):
    """Refill the work folder's contents in place with pages, sibling docs, glossary and config."""
    WORK.mkdir(exist_ok=True)
    for child in ("docs", "includes"):
        shutil.rmtree(WORK / child, ignore_errors=True)
    (WORK / "includes").mkdir()
    (WORK / "docs" / "assets").mkdir(parents=True)
    for asset in ("theme.css", "diagrams.js"):
        shutil.copy2(HERE / asset, WORK / "docs" / "assets" / asset)
    for src in DOCS.rglob("*"):
        if src.is_file() and src.name not in ("hld.md", "lld.md") and HERE not in src.parents:
            dst = WORK / "docs" / src.relative_to(DOCS)
            dst.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(src, dst)
    terms = glossary()
    (WORK / "includes" / "glossary.md").write_text("\n".join(terms) + "\n")
    pages = {doc: chapters(doc) for doc in TITLES}
    nav = {doc: split(doc, pages[doc], ref) for doc in TITLES}
    (WORK / "mkdocs.yml").write_text(config(nav))
    print(f"pages: {sum(len(v) for v in pages.values())}, glossary terms: {len(terms)}")


def branch():
    """Return the current git branch, the ref links to repo files point at."""
    out = subprocess.run(["git", "rev-parse", "--abbrev-ref", "HEAD"], cwd=ROOT, capture_output=True, text=True)
    return out.stdout.strip() if out.returncode == 0 and out.stdout.strip() != "HEAD" else "main"


def main():
    """Check the engine, prepare the sources and run the build or the preview server."""
    ap = argparse.ArgumentParser(description="Render the design docs as a site.")
    ap.add_argument("action", choices=["build", "serve"])
    ap.add_argument("--engine", choices=["mkdocs", "zensical"], default="mkdocs")
    ap.add_argument("--ref", default=None, help="git ref for links to repo files (default: current branch)")
    args = ap.parse_args()
    if shutil.which(args.engine) is None:
        sys.exit(f"{args.engine} not found; install with: pip install -r docs/_site/requirements.txt")
    prepare(args.ref or branch())
    cmd = [args.engine, args.action] + (["-a", ADDR] if args.action == "serve" else [])
    sys.exit(subprocess.run(cmd, cwd=WORK).returncode)


if __name__ == "__main__":
    main()
