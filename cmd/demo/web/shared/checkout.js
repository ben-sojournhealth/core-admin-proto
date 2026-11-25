// Which checkout this demo comes from (the main one or a worktree), on every
// page: a badge in the bottom-left corner, colored per checkout, and its
// name before the tab's title. The server injects window.CHECKOUT into
// every page (cmd/demo/checkout.go).
(() => {
  const c = window.CHECKOUT;
  if (!c) return;
  let hash = 0;
  for (const ch of c.name) hash = (hash * 31 + ch.charCodeAt(0)) >>> 0;
  const color = c.main ? "#8b949e" : `hsl(${hash % 360} 70% 62%)`;

  const badge = document.createElement("div");
  badge.title = `${c.main ? "main checkout" : "worktree " + c.name}${c.branch ? " · branch " + c.branch : ""} · port ${c.port}`;
  badge.style.cssText = [
    "position:fixed", "left:12px", "bottom:12px", "z-index:2147483647",
    "display:flex", "align-items:center", "gap:7px", "padding:5px 11px 5px 9px",
    "border-radius:999px", "background:#0d1117e6", "border:1px solid " + color,
    "color:#e6edf3", "font:600 12px/1.2 -apple-system, system-ui, sans-serif",
    "box-shadow:0 2px 8px #0006", "pointer-events:auto",
  ].join(";");
  const dot = document.createElement("span");
  dot.style.cssText = `width:9px;height:9px;border-radius:50%;background:${color}`;
  const label = document.createElement("span");
  label.textContent = c.branch && c.branch !== c.name ? `${c.name} · ${c.branch}` : c.name;
  const port = document.createElement("span");
  port.textContent = `:${c.port}`;
  port.style.cssText = "color:#8b949e;font-weight:500";
  badge.append(dot, label, port);
  document.body.append(badge);

  const prefix = `[${c.name}] `;
  const retitle = () => { if (!document.title.startsWith(prefix)) document.title = prefix + document.title; };
  retitle();
  new MutationObserver(retitle).observe(document.querySelector("title") ?? document.head, { childList: true, subtree: true, characterData: true });
})();
