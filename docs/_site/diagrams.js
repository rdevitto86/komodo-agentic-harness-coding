// --- Renders Mermaid diagrams in the site palette and opens any diagram full screen with pan and zoom
import mermaid from "https://cdn.jsdelivr.net/npm/mermaid@11.16.0/dist/mermaid.esm.min.mjs";
import elk from "https://cdn.jsdelivr.net/npm/@mermaid-js/layout-elk@0.2.3/dist/mermaid-layout-elk.esm.min.mjs";

mermaid.registerLayoutLoaders(elk);

const blocks = [...document.querySelectorAll(".md-typeset div.diagram")];
const sources = blocks.map((b) => b.textContent);

// Reads one theme token from the page so diagrams follow light and dark mode.
function token(name) {
  return getComputedStyle(document.body).getPropertyValue(name).trim();
}

// Builds Mermaid's theme from the page tokens.
function theme() {
  const v = (n) => token(`--k-${n}`);
  return {
    startOnLoad: false, securityLevel: "strict", theme: "base", layout: "elk",
    elk: { mergeEdges: false, nodePlacementStrategy: "BRANDES_KOEPF", cycleBreakingStrategy: "MODEL_ORDER", considerModelOrder: "NODES_AND_EDGES" },
    flowchart: { useMaxWidth: false, htmlLabels: true, curve: "linear", padding: 16, nodeSpacing: 40, rankSpacing: 90, wrappingWidth: 480 },
    markdownAutoWrap: false,
    // Label sizing lives here, not in theme.css, so Mermaid measures labels at their displayed size.
    themeCSS: ".nodeLabel{font-weight:600;line-height:1.35} .nodeLabel small{font-size:11.5px;font-weight:400}"
      + " .edgeLabel :is(span,p,div){white-space:nowrap!important;max-width:none!important;font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:12px;line-height:1.5}", sequence: { useMaxWidth: false },
    state: { useMaxWidth: false }, class: { useMaxWidth: false },
    themeVariables: {
      fontFamily: token("--md-text-font-family") || "system-ui, sans-serif", fontSize: "14px",
      background: v("soft"), primaryColor: v("panel"), primaryTextColor: v("fg"),
      primaryBorderColor: v("acc"), secondaryColor: v("acc-soft"), tertiaryColor: v("soft"),
      lineColor: v("mute"), textColor: v("fg"), mainBkg: v("panel"), nodeBorder: v("acc"),
      clusterBkg: v("acc-soft"), clusterBorder: v("line"), edgeLabelBackground: v("panel"),
      titleColor: v("fg"), actorBkg: v("panel"), actorBorder: v("acc"), actorTextColor: v("fg"),
      signalColor: v("fg"), signalTextColor: v("fg"), labelBoxBkgColor: v("acc-soft"),
      noteBkgColor: v("acc-soft"), noteTextColor: v("fg"), noteBorderColor: v("line"),
    },
  };
}

// Renders every diagram block, keeping the source so a theme change can redraw it.
async function render() {
  mermaid.initialize(theme());
  for (const [i, block] of blocks.entries()) {
    try {
      const { svg } = await mermaid.render(`k-diagram-${i}-${Date.now()}`, sources[i]);
      block.innerHTML = svg;
      block.classList.add("diagram-frame");
      const open = document.createElement("button");
      open.className = "diagram-open";
      open.type = "button";
      open.textContent = "⤢ Expand";
      block.prepend(open);
      block.onclick = () => view(block.querySelector("svg"));
    } catch (err) {
      block.textContent = sources[i];
      console.error("diagram render failed", i, err);
    }
  }
}

// Opens one diagram full screen: wheel or pinch to zoom, drag to pan, Esc or ✕ to close.
function view(svg) {
  const shell = document.createElement("div");
  shell.className = "diagram-viewer";
  shell.innerHTML = `<div class="stage"></div><div class="bar">
    <button data-z="in" title="Zoom in">+</button><button data-z="out" title="Zoom out">−</button>
    <button data-z="fit" title="Fit to screen">Fit</button><button data-z="one" title="Actual size">1:1</button>
    <button data-z="close" title="Close (Esc)">✕</button></div>
    <div class="hint">Scroll or pinch to zoom · drag to move · Esc to close</div>`;
  const stage = shell.querySelector(".stage");
  const copy = svg.cloneNode(true);
  const box = svg.viewBox.baseVal;
  const w = box && box.width ? box.width : svg.getBoundingClientRect().width;
  const h = box && box.height ? box.height : svg.getBoundingClientRect().height;
  copy.setAttribute("width", w);
  copy.setAttribute("height", h);
  copy.removeAttribute("style");
  stage.append(copy);
  document.body.append(shell);
  document.body.style.overflow = "hidden";

  let s = 1, x = 0, y = 0;
  const apply = () => { stage.style.transform = `translate(${x}px, ${y}px) scale(${s})`; };
  const fit = () => {
    s = Math.min((innerWidth - 48) / w, (innerHeight - 110) / h);
    x = (innerWidth - w * s) / 2; y = Math.max(64, (innerHeight - h * s) / 2); apply();
  };
  const zoomAt = (factor, cx, cy) => {
    const next = Math.min(12, Math.max(0.1, s * factor));
    x = cx - (cx - x) * (next / s); y = cy - (cy - y) * (next / s); s = next; apply();
  };
  const close = () => {
    shell.remove(); document.body.style.overflow = ""; removeEventListener("keydown", keys);
  };
  const keys = (e) => {
    if (e.key === "Escape") close();
    else if (e.key === "+" || e.key === "=") zoomAt(1.25, innerWidth / 2, innerHeight / 2);
    else if (e.key === "-") zoomAt(0.8, innerWidth / 2, innerHeight / 2);
    else if (e.key === "0") fit();
  };
  addEventListener("keydown", keys);
  shell.querySelector(".bar").onclick = (e) => {
    const z = e.target.dataset.z;
    if (z === "in") zoomAt(1.25, innerWidth / 2, innerHeight / 2);
    else if (z === "out") zoomAt(0.8, innerWidth / 2, innerHeight / 2);
    else if (z === "fit") fit();
    else if (z === "one") { s = 1; x = 24; y = 64; apply(); }
    else if (z === "close") close();
  };
  shell.addEventListener("wheel", (e) => {
    e.preventDefault();
    zoomAt(Math.exp(-e.deltaY * (e.ctrlKey ? 0.01 : 0.0025)), e.clientX, e.clientY);
  }, { passive: false });

  const points = new Map();
  let last = null;
  shell.addEventListener("pointerdown", (e) => {
    if (e.target.closest(".bar")) return;
    shell.setPointerCapture(e.pointerId);
    points.set(e.pointerId, { x: e.clientX, y: e.clientY });
    shell.classList.add("dragging");
  });
  shell.addEventListener("pointermove", (e) => {
    if (!points.has(e.pointerId)) return;
    const prev = points.get(e.pointerId);
    points.set(e.pointerId, { x: e.clientX, y: e.clientY });
    if (points.size === 1) {
      x += e.clientX - prev.x; y += e.clientY - prev.y; apply();
    } else if (points.size === 2) {
      const [a, b] = [...points.values()];
      const d = Math.hypot(a.x - b.x, a.y - b.y);
      if (last) zoomAt(d / last, (a.x + b.x) / 2, (a.y + b.y) / 2);
      last = d;
    }
  });
  const up = (e) => {
    points.delete(e.pointerId); last = null;
    if (!points.size) shell.classList.remove("dragging");
  };
  shell.addEventListener("pointerup", up);
  shell.addEventListener("pointercancel", up);
  fit();
}

render();
new MutationObserver(render).observe(document.body, { attributes: true, attributeFilter: ["data-md-color-scheme"] });
