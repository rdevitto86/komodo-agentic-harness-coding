// diagram.js moves each group's pointer to its stage box as server-sent events arrive.
(function () {
  const svg = document.getElementById("pipeline");
  const pointers = document.getElementById("pointers");
  const token = svg.dataset.token;
  // stageX returns the centre x of a stage box, or of Intake when the stage is unknown.
  function stageX(stage) {
    const box = svg.querySelector('[data-stage="' + stage + '"]') || svg.querySelector('[data-stage="Intake"]');
    return Number(box.getAttribute("x")) + Number(box.getAttribute("width")) / 2;
  }
  // place creates or moves one pointer; CSS transitions animate the move.
  function place(g, i) {
    let p = pointers.querySelector('[data-group="' + g.group + '"]');
    if (!p) {
      p = document.createElementNS("http://www.w3.org/2000/svg", "g");
      p.setAttribute("class", "pointer");
      p.dataset.group = g.group;
      p.innerHTML = '<circle r="9"/><text y="-14" text-anchor="middle"></text>';
      pointers.appendChild(p);
    }
    p.querySelector("text").textContent = g.group;
    p.classList.toggle("blocked", g.stage === "Blocked");
    p.style.transform = "translate(" + stageX(g.stage) + "px," + (150 + i * 34) + "px)";
  }
  const es = new EventSource("/events?t=" + token);
  es.onmessage = function (e) {
    const groups = JSON.parse(e.data);
    groups.forEach(place);
    pointers.querySelectorAll(".pointer").forEach(function (p) {
      if (!groups.some(function (g) { return g.group === p.dataset.group; })) p.remove();
    });
    svg.dataset.frames = String(Number(svg.dataset.frames || 0) + 1);
  };
})();
