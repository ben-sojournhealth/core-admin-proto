"use strict";
// Journey's tabs beside Claims: Members, Employers, Network, Finance, Files.
// Loaded before the page's own script, whose helpers ($, esc, usd, words,
// S, act, claimHref) these functions use when called.

const TABS = [
  { id: "claims", label: "Claims", href: "#/q/pended", search: "Search claims, members, procedures" },
  { id: "members", label: "Members", href: "#/members", search: "Search members by name or card" },
  { id: "employers", label: "Employers", href: "#/employers" },
  { id: "network", label: "Network", href: "#/network", search: "Search providers by name, NPI or TIN" },
  { id: "finance", label: "Finance", href: "#/finance" },
  { id: "files", label: "Files", href: "#/files" },
];
// The first part of the hash, to its tab.
const TAB_OF = { q: "claims", claim: "claims", search: "claims", members: "members", member: "members",
  employers: "employers", employer: "employers", network: "network", provider: "network", finance: "finance", files: "files" };

let A = null;      // api/admin
let TAB = "claims";
let FILTER = "";   // the search box, on the Members and Network tabs

function renderTabs() {
  // What's waiting on someone, per tab.
  const work = {
    claims: S.claims.filter((c) => c.status === "PENDED").length,
    finance: S.claims.filter((c) => c.actions.includes("select") || (c.payment && c.payment.state === "SELECTED")).length,
  };
  $("#tabs").innerHTML = TABS.map((t) =>
    `<a class="tab ${t.id === TAB ? "on" : ""}" href="${t.href}">${t.label}${work[t.id] ? `<span class="n">${work[t.id]}</span>` : ""}</a>`).join("");
  const t = TABS.find((x) => x.id === TAB);
  $("#q").placeholder = t.search || TABS[0].search;
}

// ---------- shared pieces ----------
const day = (iso) => iso ? new Date(iso + "T00:00:00Z").toLocaleDateString("en-US", { month: "short", day: "numeric", year: "numeric", timeZone: "UTC" }) : "";
const range = (a, b) => b ? `${day(a)} – ${day(b)}` : `since ${day(a)}`;
const todayISO = () => S.today.slice(0, 10);
const hit = (...vals) => { const t = FILTER.trim().toLowerCase(); return !t || vals.some((v) => String(v ?? "").toLowerCase().includes(t)); };
const COVER = { ACTIVE: ["Active", "APPROVED"], TERMINATED: ["Terminated", "DENIED"], FUTURE: ["Future", "todo"], NONE: ["No coverage", ""] };
const coverChip = (m) => `<span class="chip ${COVER[m.state][1]}">${COVER[m.state][0]}</span>`;
const TIER = { EMPLOYEE: "Employee", EMPLOYEE_SPOUSE: "Employee + spouse", EMPLOYEE_CHILDREN: "Employee + children", FAMILY: "Family" };
const employerOf = (id) => A.employers.find((e) => e.id === id) || { id, name: id };
const empty = (title, sub) => `<div class="clear"><b>${esc(title)}</b>${sub || ""}</div>`;
const tile = (label, value, href, cls) => `<a class="tile ${cls || ""}" ${href ? `href="${href}"` : ""}><small>${esc(label)}</small><b class="num">${value}</b></a>`;
const page = (title, count, body, tools) =>
  `<div class="head"><h1>${esc(title)}</h1>${count === null ? "" : `<span class="muted">${count}</span>`}<span class="tools">${tools || ""}</span></div>${body}`;

// sections: [{ title, items: [{ label, href, n, on, hot, off, ext }] }]. An
// item with `off` isn't built: it's dimmed and its title says why. An item
// with `ext` opens another demo.
function navHTML(sections) {
  return sections.map((s) => `<h4>${esc(s.title)}</h4>` + s.items.map((i) => {
    if (i.off) return `<span class="q off" title="${esc(i.off)}">${esc(i.label)}<span class="tag">not built</span></span>`;
    if (i.ext) return `<a class="q ext" href="${i.ext}" target="_blank" title="${esc(i.title || "")}">${esc(i.label)}<span class="tag">↗</span></a>`;
    return `<a class="q ${i.kid ? "kid" : ""} ${i.on ? "on" : ""}" href="${i.href}">${esc(i.label)}${i.n === undefined ? "" : `<span class="n ${i.hot && i.n ? "hot" : ""}">${i.n}</span>`}</a>`;
  }).join("")).join("");
}

// The year's coverage as a bar, with today marked.
function yearPct(iso, endOfDay) {
  const y = +S.today.slice(0, 4), a = Date.UTC(y, 0, 1), b = Date.UTC(y + 1, 0, 1);
  const t = Date.parse(iso + "T00:00:00Z") + (endOfDay ? 86400000 : 0);
  return Math.max(0, Math.min(1, (t - a) / (b - a))) * 100;
}
function coverBar(spans, tall) {
  const segs = spans.filter((s) => s.line === "MEDICAL").map((s) => {
    const l = yearPct(s.start), r = s.end ? yearPct(s.end, true) : 100;
    return r > l ? `<i class="${s.cobra ? "cobra" : ""}" style="left:${l}%;width:${r - l}%" title="${esc(s.plan)} · ${esc(range(s.start, s.end))}"></i>` : "";
  }).join("");
  const months = tall ? `<div class="months">${"JFMAMJJASOND".split("").map((m) => `<span>${m}</span>`).join("")}</div>` : "";
  return `<div class="cov ${tall ? "tall" : ""}">${segs}<u style="left:${yearPct(todayISO())}%" title="Today"></u></div>${months}`;
}

function claimsTable(rows) {
  if (!rows.length) return `<div class="sub">No claims.</div>`;
  return `<div class="tablewrap"><table class="list tight"><thead><tr><th>Claim</th><th>Service</th><th>Provider</th><th class="r">Billed</th><th class="r">Plan paid</th><th>Status</th></tr></thead><tbody>` +
    rows.slice().reverse().map((c) => `<tr data-claim="${esc(c.id)}"><td><span class="id num">${esc(c.number)}</span></td>
      <td>${esc(c.procedure)} <span class="sub">· ${esc(c.dos.slice(5).replace("-", "/"))}</span></td><td>${esc(c.provider)}</td>
      <td class="r num">${usd(c.charge)}</td><td class="r num">${["PENDED", "RECEIVED"].includes(c.status) ? "–" : usd(c.paid)}</td>
      <td>${decisionChips(c)}</td></tr>`).join("") + `</tbody></table></div>`;
}

// ---------- routing ----------
// Returns true when the hash belongs to one of these tabs.
async function routeTab(kind, a, b) {
  const render = { members: membersList, member: memberPage, employers: employerPage, employer: employerPage,
    network: networkList, provider: providerPage, finance: financePage, files: filesPage }[kind];
  if (!render) return false;
  await render(a ? decodeURIComponent(a) : "", b);
  return true;
}

// ---------- Members ----------
const MEMBER_VIEWS = [
  { id: "all", label: "All members", test: () => true },
  { id: "active", label: "Active", test: (m) => m.state === "ACTIVE" },
  { id: "terminated", label: "Terminated", test: (m) => m.state === "TERMINATED" },
  { id: "none", label: "No coverage", test: (m) => m.state === "NONE" || m.state === "FUTURE" },
];
function membersNav(active) {
  $("#nav").innerHTML = navHTML([
    { title: "Work", items: [
      { label: "Enrollment exceptions", ext: "/eligibility/", title: "834 loops that didn't apply. Worked in Eligibility, which has its own sandbox." },
      { label: "Terminations to approve", ext: "/eligibility/", title: "Members a full file left out. Worked in Eligibility, which has its own sandbox." },
    ] },
    { title: "Browse", items: MEMBER_VIEWS.map((v) => ({ label: v.label, href: "#/members/" + v.id, n: A.members.filter(v.test).length, on: active === v.id })) },
  ]);
}
function membersList(view) {
  const v = MEMBER_VIEWS.find((x) => x.id === view) || MEMBER_VIEWS[0];
  membersNav(v.id);
  const rows = A.members.filter(v.test).filter((m) => hit(m.name, m.card, m.id));
  const body = rows.length ? `<div class="tablewrap"><table class="list"><thead><tr><th>Member</th><th>Card</th><th>Employer</th><th>Plan</th><th style="width:26%">${S.today.slice(0, 4)} coverage</th><th>Status</th></tr></thead><tbody>` +
    rows.map((m) => {
      const med = m.coverage.find((s) => s.line === "MEDICAL");
      return `<tr data-go="#/member/${esc(m.id)}"><td><span class="id">${esc(m.name)}</span> <span class="sub">${words(m.relationship)}</span></td>
        <td class="mono">${esc(m.card)}</td><td>${esc(employerOf(m.customerId).name)}</td><td>${med ? esc(med.plan) : `<span class="sub">–</span>`}</td>
        <td>${coverBar(m.coverage)}</td><td>${coverChip(m)}</td></tr>`;
    }).join("") + `</tbody></table></div>` : empty("No members", "");
  $("#main").innerHTML = page(v.label, rows.length, `<div class="panel">${body}</div>`);
}

async function memberPage(id) {
  membersNav(null);
  const m = A.members.find((x) => x.id === id);
  if (!m) return void ($("#main").innerHTML = empty("No member " + id, "It may have been removed by a reset."));
  let acc = null;
  try { acc = await get("api/accumulators?member=" + encodeURIComponent(id)); } catch (e) { /* no accumulators */ }
  const family = A.members.filter((x) => x.subscriberId === m.subscriberId);
  const emp = employerOf(m.customerId);
  const spans = m.coverage.length ? m.coverage.map((s) => `<div class="kv wide"><span>${words(s.line)}${s.cobra ? " · COBRA" : ""}</span>
      <span><b>${esc(s.plan)}</b> <span class="sub">${esc(TIER[s.tier] || words(s.tier))} · ${esc(range(s.start, s.end))}</span></span></div>`).join("")
    : `<div class="sub">No coverage on file.</div>`;
  const meters = () => {
    if (!acc || !acc.rows || !acc.columns) return "";
    const fam = acc.rows[0], me = acc.rows.find((r, i) => i > 0 && r.owner === m.name.split(" ")[0]);
    const meter = (label, cell) => {
      if (!cell) return "";
      const pct = Math.min(100, Math.round(cell.applied / cell.limit * 100));
      return `<div class="meter"><div class="row"><span>${esc(label)}</span><span class="num">${usd(cell.applied)} <span class="sub">/ ${usd(cell.limit)}</span></span></div>
        <div class="track2"><i class="${pct >= 100 ? "full" : ""}" style="width:${pct}%"></i></div></div>`;
    };
    const col = (i) => meter("Individual", me && me.cells[i]) + meter("Family", fam.cells[i]);
    const inn = acc.columns.map((c, i) => [c, i]).filter(([c]) => c.startsWith("In-network"));
    const oon = acc.columns.map((c, i) => [c, i]).filter(([c]) => !c.startsWith("In-network"));
    const block = ([c, i]) => `<div class="label">${esc(c)}</div>${col(i)}`;
    return `<div class="section">${inn.map(block).join("")}</div>` +
      (oon.length ? `<div class="section"><details><summary class="label more">Out-of-network</summary>${oon.map(block).join("")}</details></div>` : "");
  };
  $("#main").innerHTML = `
    <a class="back" href="#/members">← Members</a>
    <div class="claimhead"><div><h1>${esc(m.name)} <span style="vertical-align:4px">${coverChip(m)}</span></h1>
      <div class="who"><span class="mono">${esc(m.card)}</span> · born ${esc(day(m.born))} · ${words(m.relationship)} · <a href="#/employer/${esc(emp.id)}">${esc(emp.name)}</a></div></div></div>
    <div class="cols"><div>
      <div class="panel"><div class="section"><div class="label">Coverage</div>${coverBar(m.coverage, true)}<div style="margin-top:12px">${spans}</div></div>
        <div class="section"><div class="label">Family</div><div class="boxes">${family.map((f) =>
          `<a class="box ${f.id === m.id ? "on" : ""}" href="#/member/${esc(f.id)}"><b>${esc(f.name)}</b><small>${words(f.relationship)}</small>${coverChip(f)}</a>`).join("")}</div></div>
        <div class="section"><div class="label">Claims</div>${claimsTable(S.claims.filter((c) => c.memberId === m.id))}</div></div>
    </div><div><div class="panel">${meters() || `<div class="section sub">No accumulators.</div>`}</div></div></div>`;
}

// ---------- Employers ----------
function shareText(s) {
  if (!s) return `<span class="sub">–</span>`;
  if (!s.covered) return `<span class="chip DENIED">Not covered</span>`;
  const parts = [];
  if (s.copay) parts.push(usd(s.copay) + " copay");
  if (s.coinsuranceBps) parts.push(s.coinsuranceBps / 100 + "%");
  const cost = parts.join(" + ") || (s.deductibleApplies ? "0%" : "No cost share");
  return s.deductibleApplies ? `<span class="sub">Deductible, then</span> ${cost}` : cost;
}
function planHTML(p) {
  const money = (c) => c ? usd(c) : "–";
  const tiers = p.tiers.map((t) => `<div class="box plain"><b>${esc(t.name)}</b><small>${esc(t.networks.join(", ") || t.basis || "")}</small>
      <div class="pair"><span>Deductible <span class="sub">ind / fam</span></span><span class="num">${money(t.deductibleIndividual)} <span class="sub">/ ${money(t.deductibleFamily)}</span></span></div>
      <div class="pair"><span>Out-of-pocket max</span><span class="num">${money(t.oopIndividual)} <span class="sub">/ ${money(t.oopFamily)}</span></span></div></div>`).join("");
  const benefits = `<table class="lines"><thead><tr><th>Service</th>${p.tiers.map((t) => `<th>${esc(t.name)}</th>`).join("")}</tr></thead><tbody>` +
    p.benefits.map((b) => `<tr><td>${esc(b.category)}${b.priorAuth ? ` <span class="chip todo">Prior auth</span>` : ""}${b.limits.map((l) => ` <span class="chip">${esc(l)}</span>`).join("")}</td>
      ${b.shares.map((s) => `<td>${shareText(s)}</td>`).join("")}</tr>`).join("") +
    `<tr><td class="sub">Anything else</td><td class="sub" colspan="${p.tiers.length}">${p.default === "DENY" ? "Denied" : "Pended"}</td></tr></tbody></table>`;
  return `<div class="section"><div class="planhead"><b>${esc(p.name)}</b><span class="chip">${esc(p.type)}</span>
      <span class="sub">${esc(range(p.start, p.end))} · version ${p.version} of ${p.versions}</span></div>
    <div class="boxes wide" style="margin-top:10px">${tiers}</div>
    <details style="margin-top:12px"><summary class="label more">Benefits</summary>${benefits}</details></div>`;
}
function employerPage(id) {
  const e = A.employers.find((x) => x.id === id) || A.employers[0];
  $("#nav").innerHTML = navHTML([
    { title: "Employers", items: A.employers.map((x) => ({ label: x.name, href: "#/employer/" + x.id, on: e && x.id === e.id, n: A.members.filter((m) => m.customerId === x.id).length })) },
    { title: "Work", items: [{ label: "Billing & funding", off: "Invoices and claim funding requests to the employer aren't built." }] },
  ]);
  if (!e) return void ($("#main").innerHTML = empty("No employers", ""));
  const mine = A.members.filter((m) => m.customerId === e.id);
  const count = (s) => mine.filter((m) => m.state === s).length;
  const subs = e.tiers.reduce((n, t) => n + t.subscribers, 0);
  const policy = A.stopLoss.find((p) => p.customerId === e.id);
  const tierBar = subs ? `<div class="bar thick">${e.tiers.filter((t) => t.subscribers).map((t, i) =>
      `<div class="seg t${i}" style="flex-grow:${t.subscribers}" title="${esc(TIER[t.tier])}: ${t.subscribers}"></div>`).join("")}</div>
    <div class="legend">${e.tiers.filter((t) => t.subscribers).map((t, i) => `<span><i class="seg t${i}"></i>${esc(TIER[t.tier])} <b class="num">${t.subscribers}</b></span>`).join("")}</div>`
    : `<div class="sub">No subscribers covered today.</div>`;
  $("#main").innerHTML = `
    <div class="claimhead"><div><h1>${esc(e.name)} <span class="chip APPROVED" style="vertical-align:4px">${words(e.status)}</span> <span class="chip" style="vertical-align:4px">${words(e.funding)}</span></h1>
      <div class="who">EIN <span class="mono">${esc(e.ein)}</span> · ${esc(e.place)} · client ${esc(range(e.since))}</div></div></div>
    <div class="tiles">${tile("Subscribers", subs)}${tile("Members", mine.length, "#/members")}${tile("Active", count("ACTIVE"))}${tile("Terminated", count("TERMINATED"))}${tile("No coverage", count("NONE") + count("FUTURE"))}</div>
    <div class="cols"><div>
      <div class="panel">${e.plans.map(planHTML).join("") || `<div class="section sub">No plans.</div>`}</div>
    </div><div>
      <div class="panel">
        <div class="section"><div class="label">Subscribers by tier</div>${tierBar}</div>
        <div class="section"><div class="label">Enrollment senders</div><div class="boxes one">${e.senders.map((s) =>
          `<div class="box plain"><b class="mono">${esc(s.sender)}</b><small>${esc(s.role)}</small></div>`).join("") || `<span class="sub">None set up.</span>`}</div></div>
        <div class="section"><div class="label">Groups</div>${e.groups.map((g) => `<div class="kv wide"><span class="mono">${esc(g.id)}</span><span>${esc(g.name)}</span></div>`).join("") || `<span class="sub">None.</span>`}</div>
        <div class="section"><div class="label">Stop-loss</div>${policy
          ? `<a class="box" href="#/finance/stoploss"><b>${esc(policy.carrier)}</b><small>${usd(policy.deductible)} specific · ${esc(basisLabel(policy))}</small></a>`
          : `<span class="sub">No policy on file.</span>`}</div>
      </div></div></div>`;
}

// ---------- Network ----------
const NETWORK_VIEWS = [
  { id: "providers", label: "All providers", test: () => true },
  { id: "in", label: "In-network", test: (p) => p.inNetwork },
  { id: "out", label: "Out-of-network", test: (p) => !p.inNetwork },
];
const npiOf = (c) => (c.raw && c.raw.billingProvider && c.raw.billingProvider.npi) || "";
function networkNav(active) {
  $("#nav").innerHTML = navHTML([
    { title: "Providers", items: NETWORK_VIEWS.map((v) => ({ label: v.label, href: "#/network/" + v.id, n: A.providers.filter(v.test).length, on: active === v.id })) },
    { title: "Networks", items: [{ label: "Networks & contracts", href: "#/network/networks", n: A.networks.length, on: active === "networks" }] },
    { title: "Work", items: [{ label: "Vendor intake", off: "Provider files from network vendors aren't built." }] },
  ]);
}
const networkChip = (p) => p.inNetwork
  ? `<span class="chip APPROVED">${esc([...new Set(p.participations.filter((x) => x.current).map((x) => x.network))].join(", "))}</span>`
  : `<span class="chip">Out-of-network</span>`;
function networkList(view) {
  if (view === "networks") return networksPage();
  const v = NETWORK_VIEWS.find((x) => x.id === view) || NETWORK_VIEWS[0];
  networkNav(v.id);
  const rows = A.providers.filter(v.test).filter((p) => hit(p.name, p.npi, ...p.tins.map((t) => t.tin), ...p.tins.map((t) => t.billingName)));
  const body = rows.length ? `<div class="tablewrap"><table class="list"><thead><tr><th>Provider</th><th>NPI</th><th>Bills as</th><th>Taxonomy</th><th>Network</th><th class="r">Claims</th></tr></thead><tbody>` +
    rows.map((p) => `<tr data-go="#/provider/${esc(p.npi)}"><td><span class="id">${esc(p.name)}</span> <span class="sub">${words(p.kind)}</span></td>
      <td class="mono">${esc(p.npi)}</td><td>${p.tins.map((t) => `${esc(t.billingName)} <span class="sub mono">${esc(t.tin)}</span>`).join("<br>")}</td>
      <td class="mono">${p.taxonomies.map(esc).join(", ")}</td><td>${networkChip(p)}</td>
      <td class="r num">${S.claims.filter((c) => npiOf(c) === p.npi).length}</td></tr>`).join("") + `</tbody></table></div>` : empty("No providers", "");
  $("#main").innerHTML = page(v.label, rows.length, `<div class="panel">${body}</div>`);
}
function providerPage(npi) {
  networkNav(null);
  const p = A.providers.find((x) => x.npi === npi);
  if (!p) return void ($("#main").innerHTML = empty("No provider with NPI " + npi, ""));
  const parts = p.participations.length ? p.participations.map((x) => `<a class="box ${x.current ? "" : "dim"}" href="#/network/networks"><b>${esc(x.network)}</b>
      <small>${esc(x.contract)}</small><small>TIN <span class="mono">${esc(x.tin)}</span> · ${esc(range(x.start, x.end))}</small></a>`).join("")
    : `<span class="sub">No network participation. Claims price on the plan's out-of-network basis.</span>`;
  $("#main").innerHTML = `
    <a class="back" href="#/network">← Providers</a>
    <div class="claimhead"><div><h1>${esc(p.name)} <span style="vertical-align:4px">${networkChip(p)}</span></h1>
      <div class="who">NPI <span class="mono">${esc(p.npi)}</span> · ${words(p.kind)} · ${words(p.status)}${p.taxonomies.length ? ` · taxonomy <span class="mono">${p.taxonomies.map(esc).join(", ")}</span>` : ""}</div></div></div>
    <div class="cols"><div><div class="panel">
        <div class="section"><div class="label">Network participation</div><div class="boxes">${parts}</div></div>
        <div class="section"><div class="label">Claims billed</div>${claimsTable(S.claims.filter((c) => npiOf(c) === p.npi))}</div></div>
    </div><div><div class="panel">
        <div class="section"><div class="label">Bills as</div>${p.tins.map((t) => `<div class="kv wide"><span class="mono">${esc(t.tin)}</span><span>${esc(t.billingName)} <span class="sub">${esc(range(t.start, t.end))}</span></span></div>`).join("") || `<span class="sub">No TIN on file.</span>`}</div>
        <div class="section"><div class="label">Locations</div>${p.locations.map((l) => `<div style="font-size:13px">${esc(l.address)}${l.accepting ? ` <span class="chip APPROVED">Accepting patients</span>` : ""}</div>`).join("") || `<span class="sub">None on file.</span>`}</div>
    </div></div></div>`;
}
const METHOD = { FEE_SCHEDULE: "Fee schedule", PERCENT_OF_BILLED: "of billed", PERCENT_OF_MEDICARE: "of Medicare", FLAT_RATE: "flat rate", PER_DIEM: "per diem", CASE_RATE: "case rate" };
function networksPage() {
  networkNav("networks");
  const term = (t, i) => `<div class="box plain"><small>Term ${i + 1}${t.scope ? " · " + esc(t.scope) : ""}</small>
    <b>${t.method === "FEE_SCHEDULE" ? `Fee schedule <span class="mono sub">${esc(t.detail)}</span>` : `${esc(t.detail)} ${esc(METHOD[t.method] || words(t.method))}`}</b></div>`;
  const body = A.networks.map((n) => `<div class="panel" style="margin-bottom:16px">
      <div class="section"><div class="planhead"><b>${esc(n.name)}</b><span class="chip">${words(n.kind)}</span><span class="sub">${esc(n.vendor)} · ${n.providers} provider${n.providers === 1 ? "" : "s"}</span></div></div>
      ${A.contracts.filter((c) => c.networkId === n.id).map((c) => `<div class="section"><div class="planhead"><b>${esc(c.name)}</b><span class="sub">${esc(range(c.start, c.end))}</span></div>
        <div class="boxes" style="margin-top:10px">${c.terms.map(term).join("")}</div></div>`).join("") || `<div class="section sub">No contracts.</div>`}</div>`).join("");
  $("#main").innerHTML = page("Networks & contracts", A.networks.length, body || empty("No networks", ""));
}

// ---------- Finance ----------
const PAY_VIEWS = [
  { id: "ready", label: "Ready to pay", hot: true, test: (c) => c.actions.includes("select") },
  { id: "run", label: "In payment run", hot: true, test: (c) => c.payment && c.payment.state === "SELECTED" },
  { id: "issued", label: "Issued", test: (c) => c.payment && c.payment.state === "ISSUED" },
  { id: "cleared", label: "Cleared", test: (c) => c.payment && c.payment.state === "CLEARED" },
  { id: "returned", label: "Returned & voided", test: (c) => c.payment && (c.payment.state === "RETURNED" || c.payment.state === "VOIDED") },
];
const payAmount = (c) => c.payment ? c.payment.paid : c.paid;
const PAY_CHIP = { SELECTED: "todo", ISSUED: "APPROVED", CLEARED: "APPROVED", RETURNED: "DENIED", VOIDED: "DENIED" };
function basisLabel(p) {
  const months = Math.round((Date.parse(p.end) - Date.parse(p.start)) / (86400000 * 30.44));
  return `${p.unlimitedRunIn ? "∞" : months + p.runInMonths}/${months + p.runOutMonths}`;
}
function financePage(view) {
  const v = PAY_VIEWS.find((x) => x.id === view);
  $("#nav").innerHTML = navHTML([
    { title: "Payments", items: PAY_VIEWS.map((x) => ({ label: x.label, href: "#/finance/" + x.id, n: S.claims.filter(x.test).length, hot: x.hot, on: !!v && v.id === x.id })) },
    { title: "Stop-loss", items: [{ label: "Policies", href: "#/finance/stoploss", n: A.stopLoss.length, on: view === "stoploss" }] },
    { title: "Work", items: [
      { label: "Employer funding", off: "Requesting and receiving the employer's money for a payment run isn't built." },
      { label: "Bank reconciliation", off: "Matching bank activity to payments isn't built; clear and return are recorded by hand on the claim." },
    ] },
  ]);
  if (view === "stoploss") return stopLossPage();
  const cur = v || PAY_VIEWS[0];
  const sum = (x) => S.claims.filter(x.test).reduce((n, c) => n + payAmount(c), 0);
  const tiles = PAY_VIEWS.map((x) => `<a class="tile ${x.id === cur.id ? "on" : ""}" href="#/finance/${x.id}"><small>${esc(x.label)}</small>
    <b class="num">${usd(sum(x))}</b><small>${S.claims.filter(x.test).length} claim${S.claims.filter(x.test).length === 1 ? "" : "s"}</small></a>`).join("");
  const rows = S.claims.filter(cur.test);
  const tools = cur.id === "ready" && rows.length ? `<button class="btn primary" data-pay="select">Select all for payment</button>`
    : cur.id === "run" && rows.length ? `<button class="btn primary" data-pay="release">Release payment run</button>` : "";
  const body = rows.length ? `<div class="tablewrap"><table class="list"><thead><tr><th>Claim</th><th>Payee</th><th>Member</th><th>Paid</th><th>Trace</th><th>Remittance</th><th class="r">Amount</th><th>Payment</th></tr></thead><tbody>` +
    rows.slice().reverse().map((c) => {
      const p = c.payment || {};
      const state = p.state ? `<span class="chip ${PAY_CHIP[p.state] || ""}">${words(p.state)}</span>` : `<span class="chip">Not selected</span>`;
      return `<tr data-claim="${esc(c.id)}"><td><span class="id num">${esc(c.number)}</span>${c.outcome === "DENIED" ? ` <span class="chip DENIED">denied</span>` : ""}</td>
        <td>${esc(c.provider)}</td><td>${esc(c.member)}</td><td>${p.date ? esc(day(p.date)) : `<span class="sub">–</span>`}</td>
        <td class="mono">${esc(p.trace || "")}</td><td class="mono">${esc(p.remit || "")}</td><td class="r num">${usd(payAmount(c))}</td><td>${state}</td></tr>`;
    }).join("") + `</tbody></table></div>` : empty("Nothing here", "");
  $("#main").innerHTML = `<div class="tiles six">${tiles}</div>` + page(cur.label, rows.length, `<div class="panel">${body}</div>`, tools);
}

function stopLossPage() {
  const policy = (p) => {
    // The incurred and paid windows on one axis, from the earliest date a
    // claim can be incurred to the latest it can be paid.
    const add = (iso, months) => { const d = new Date(iso + "T00:00:00Z"); d.setUTCMonth(d.getUTCMonth() + months); return d.getTime(); };
    const a = add(p.start, -p.runInMonths), b = add(p.end, p.runOutMonths) + 86400000, pct = (t) => (t - a) / (b - a) * 100;
    const win = (label, from, to, cls, text) => `<div class="win"><span>${label}</span><div class="cov tall"><i class="${cls}" style="left:${pct(from)}%;width:${pct(to) - pct(from)}%"></i>
      <u style="left:${Math.max(0, Math.min(100, pct(Date.parse(todayISO()))))}%" title="Today"></u></div><span class="sub">${text}</span></div>`;
    const end = Date.parse(p.end) + 86400000;
    const exposure = p.specific.length
      ? p.specific.map((x) => { const w = Math.min(100, Math.round(x.paid / x.deductible * 100));
          return `<div class="meter"><div class="row"><span>${esc(x.member)}</span><span class="num">${usd(x.paid)} <span class="sub">/ ${usd(x.deductible)}</span></span></div><div class="track2"><i class="${w >= 100 ? "full" : ""}" style="width:${w}%"></i></div></div>`; }).join("")
      : `<div class="hollow">No exposure recorded<small>Paid claims aren't posted to stop-loss yet.</small></div>`;
    return `<div class="panel" style="margin-bottom:16px">
      <div class="section"><div class="planhead"><b>${esc(p.customer)}</b><span class="chip">${esc(basisLabel(p))}</span>
          <span class="sub">${esc(p.carrier)} · <span class="mono">${esc(p.number)}</span> · ${esc(p.covers)}</span></div>
        <div style="margin-top:14px">${win("Incurred", a, end, "", p.unlimitedRunIn ? `any date – ${esc(day(p.end))}` : `${esc(day(new Date(a).toISOString().slice(0, 10)))} – ${esc(day(p.end))}`)}
          ${win("Paid", Date.parse(p.start), b, "paid", `${esc(day(p.start))} – ${esc(day(new Date(b - 86400000).toISOString().slice(0, 10)))}`)}</div></div>
      <div class="section"><div class="label">Specific</div><div class="boxes">
        <div class="box plain"><small>Deductible per member</small><b class="num">${usd(p.deductible)}</b></div>
        ${p.lasers.map((l) => `<a class="box" href="#/member/${esc(l.memberId)}" title="${esc(l.note)}"><small>Laser · ${esc(l.member || l.memberId)}</small><b class="num">${usd(l.deductible)}</b></a>`).join("")}</div></div>
      <div class="section"><div class="label">Aggregate · monthly factor per subscriber</div><div class="boxes">
        ${p.factors.map((f) => `<div class="box plain"><small>${esc(TIER[f.tier] || words(f.tier))}</small><b class="num">${usd(f.monthly)}</b></div>`).join("")}
        <div class="box plain"><small>Minimum attachment</small><b class="num">${usd(p.minimumAttachment)}</b></div></div></div>
      <div class="section"><div class="label">Exposure & filings</div>${exposure}</div></div>`;
  };
  $("#main").innerHTML = page("Stop-loss policies", A.stopLoss.length, A.stopLoss.map(policy).join("") || empty("No policies", ""));
}

// ---------- Files ----------
const FILE_KINDS = [
  { id: "837", label: "837 claims", dir: "Inbound", none: `Load one in <a href="/claims-intake/" target="_blank">Claims Intake ↗</a>, which shares this sandbox.` },
  { id: "834", label: "834 enrollment", dir: "Inbound", none: `834s load in <a href="/enrollment-intake/" target="_blank">Enrollment Intake ↗</a>, which has its own sandbox.` },
  { id: "999", label: "999 acknowledgments", dir: "Outbound", none: "One goes back for every inbound file." },
  { id: "835", label: "835 remittances", dir: "Outbound", none: `Releasing a payment run here doesn't write the 835 yet. See <a href="/remittance/" target="_blank">Remittance ↗</a>.` },
];
function filesPage(kind) {
  const k = FILE_KINDS.find((x) => x.id === kind);
  const n = (id) => A.files.filter((f) => f.kind === id).length;
  $("#nav").innerHTML = navHTML([{ title: "Files", items: [{ label: "All files", href: "#/files", n: A.files.length, on: !k }] }].concat(
    ["Inbound", "Outbound"].map((dir) => ({ title: dir, items: FILE_KINDS.filter((x) => x.dir === dir).map((x) => ({ label: x.label, href: "#/files/" + x.id, n: n(x.id), on: !!k && k.id === x.id })) }))));
  const rows = A.files.filter((f) => !k || f.kind === k.id);
  const stamp = (t) => new Date(t).toLocaleString("en-US", { month: "short", day: "numeric", hour: "numeric", minute: "2-digit", timeZone: "UTC" });
  const status = (f) => f.rejected ? `<span class="chip ${f.loaded ? "todo" : "DENIED"}">${f.loaded ? "Partly accepted" : "Rejected"}</span>` : `<span class="chip APPROVED">${f.direction === "OUT" ? "Sent" : "Accepted"}</span>`;
  const counts = (f) => f.kind === "999" ? `${f.loaded} set${f.loaded === 1 ? "" : "s"} accepted${f.rejected ? `, ${f.rejected} rejected` : ""}`
    : `${f.loaded} ${f.kind === "834" ? "member loop" : "claim"}${f.loaded === 1 ? "" : "s"}${f.rejected ? `, ${f.rejected} not loaded` : ""}`;
  const go = (f) => f.kind === "837" || (f.kind === "999" && f.answers) ? ` data-open="/claims-intake/#/job/${esc(f.id)}"` : "";
  const body = rows.length ? `<div class="tablewrap"><table class="list"><thead><tr><th></th><th>File</th><th>Type</th><th>Trading partner</th><th>Control #</th><th>Contents</th><th>Status</th><th class="r">When</th></tr></thead><tbody>` +
    rows.map((f) => `<tr${go(f)}><td class="dir ${f.direction}">${f.direction === "IN" ? "↓ in" : "↑ out"}</td><td><span class="id">${esc(f.name)}</span></td><td><span class="chip">${esc(f.kind)}</span></td>
      <td class="mono">${esc(f.party)}</td><td class="mono">${esc(f.icn || "")}</td><td>${counts(f)}</td><td>${status(f)}</td><td class="r num">${esc(stamp(f.time))}</td></tr>`).join("") + `</tbody></table></div>`
    : empty(k ? "No " + k.label : "No files yet", k ? k.none : FILE_KINDS[0].none);
  const tiles = FILE_KINDS.map((x) => `<a class="tile ${k && k.id === x.id ? "on" : ""}" href="#/files/${x.id}"><small>${x.dir === "Inbound" ? "↓" : "↑"} ${esc(x.label)}</small><b class="num">${n(x.id)}</b></a>`).join("");
  $("#main").innerHTML = `<div class="tiles">${tiles}</div>` + page(k ? k.label : "All files", rows.length, `<div class="panel">${body}</div>`);
}

// ---------- clicks ----------
document.addEventListener("click", async (ev) => {
  const go = ev.target.closest("[data-go]");
  if (go) return void (location.hash = go.dataset.go);
  const open = ev.target.closest("[data-open]");
  if (open) return void window.open(open.dataset.open, "_blank");
  const pay = ev.target.closest("[data-pay]");
  if (pay) {
    pay.disabled = true;
    const res = await act({ type: pay.dataset.pay });
    if (res && res.ok) location.hash = pay.dataset.pay === "select" ? "#/finance/run" : "#/finance/issued";
  }
});
