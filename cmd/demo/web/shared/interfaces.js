"use strict";
// The interface panel: a side panel showing a proto service as one or more
// callers use it: who calls which methods, the service source with the
// used methods highlighted, and every message those calls carry, grouped by
// protobuf package. Data comes from /api/interface or /api/contract
// (protodoc.Contract).
(() => {
  const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
  const panel = document.createElement("aside");
  panel.id = "panel";
  panel.setAttribute("aria-hidden", "true");
  panel.innerHTML = `<div class="head"><div><h3 id="p-title"></h3><div class="sub" id="p-sub"></div></div><button class="close" id="p-close" aria-label="Close">×</button></div>
    <div class="body" id="p-body"></div>`;
  document.body.appendChild(panel);
  const $ = (id) => panel.querySelector("#" + id);

  // Proto source with comments and keywords marked, and (given the used
  // methods) the rpc lines highlighted or dimmed.
  const KEYWORDS = /\b(service|rpc|returns|message|enum|repeated|oneof|map|optional)\b/g;
  function highlight(src, used) {
    return src.replace(/\n$/, "").split("\n").map((line) => {
      if (/^\s*\/\//.test(line)) return `<span class="c">${esc(line)}</span>`;
      let html = esc(line).replace(KEYWORDS, '<span class="k">$1</span>');
      const rpc = line.match(/^\s*rpc (\w+)\(/);
      if (rpc && used) html = `<span class="${used.has(rpc[1]) ? "used" : "dim"}">${html}</span>`;
      return html;
    }).join("\n");
  }

  const api = {
    onClose: null,
    // open shows the panel with a title and returns its (empty) body.
    open(title, sub = "") {
      $("p-title").textContent = title;
      $("p-sub").textContent = sub;
      $("p-body").innerHTML = "";
      panel.classList.add("open");
      panel.setAttribute("aria-hidden", "false");
      return $("p-body");
    },
    close() {
      if (!panel.classList.contains("open")) return;
      panel.classList.remove("open");
      panel.setAttribute("aria-hidden", "true");
      api.onClose?.();
    },
    // contractHTML is a contract's calls, service and messages.
    contractHTML(c, calls) {
      const used = new Set(c.methods.filter((m) => m.used).map((m) => m.name));
      return `
        ${calls?.length ? `<h4>Calls</h4>
        <div class="calls">${calls.map((call) => `<div class="call"><b>${esc(call.by)}</b>${call.methods.map((m) => `<span class="chip">${esc(m)}</span>`).join("")}</div>`).join("")}</div>` : ""}
        <h4>Service <span class="file">${esc(c.file)}</span></h4>
        <pre class="proto">${highlight(c.source, used)}</pre>
        <h4>Messages the calls carry, by package</h4>
        ${c.packages.map((p, i) => `<details class="pkg"${i === 0 ? " open" : ""}><summary>${esc(p.name)}<span>${p.types.length} type${p.types.length === 1 ? "" : "s"} · ${esc(p.types[0].file)}</span></summary>
          <div class="types">${p.types.map((t) => `<pre class="proto">${highlight(t.source)}</pre>`).join("")}</div></details>`).join("")}`;
    },
    // showContract fills the open panel with a contract.
    showContract(c, calls) {
      $("p-sub").textContent = c.service;
      $("p-body").innerHTML = api.contractHTML(c, calls);
    },
  };
  $("p-close").addEventListener("click", () => api.close());
  document.addEventListener("keydown", (ev) => { if (ev.key === "Escape") api.close(); });
  // A click outside the panel closes it; a click that opens it again is
  // handled by the page after this.
  document.addEventListener("click", (ev) => {
    if (!ev.target.closest("#panel") && !ev.target.closest("[data-opens-panel]")) api.close();
  }, true);
  window.InterfacePanel = api;
})();
