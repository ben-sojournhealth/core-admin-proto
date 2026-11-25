// The worktrees bar: every checkout of the shown space's repo as a chip, the
// operations on them, and up to four open side by side in the space's
// "worktrees" tab.
//
//   chip click   select it, and show its pane: opening one unless four are open
//   chip ✕       close its pane (open ones only)
//   pane focus   selects the checkout that pane is in
//   Open/Close   a pane in that checkout running claude (resumes its last
//                conversation); closing the pane leaves the checkout alone
//   Merge        merge its commits into the local main branch (fast-forward
//                when possible); uncommitted changes stay behind. Then SETUP
//                on main. A conflict aborts and leaves main as it was; a
//                trial merge on every refresh marks the chip "⚠ conflicts"
//                beforehand and turns Merge off.
//   Demo         open its demo server's page in the browser, starting the
//                server (background terminal demo-<checkout>) if it isn't
//                up; each checkout has its own port, shown on its chip
//   Delete       remove the checkout and its branch (asks again first)
//   New          a branch and checkout from main, SETUP, then open it
//   Sync all     fast-forward every clean checkout that is only behind main,
//                then SETUP in each; the others are listed, not touched
//   Push main    git push from the main checkout
//
// Lives in the repo (tools/herdr/worktrees); `make herdr` links it into Herdr.

import { View, div } from "gpui-kit";
import { h_flex, InputState, Input } from "gpui-base";
import { run, spaces, panes, focused, exec, hc, home, appHome, readTextFile, theme, notify, checking } from "herdr";

// After a checkout is made or moved, and on main after a merge.
const SETUP = "make gen && go build ./...";
// What an opened checkout runs: its last conversation there, else a new one.
const AGENT = ["sh", "-c", "claude --continue || exec claude"];
// A checkout's demo server: started with this in the checkout, it serves on
// the checkout's own port, kept in PORTS in git's common directory
// (cmd/demo/checkout.go).
const DEMO = ["make", "demo"];
const PORTS = "coreadmin-demo-ports.json";
const MAX_OPEN = 4;
const TAB = "worktrees";
const READ = ["--no-optional-locks", "-c", "core.fsmonitor=false"];

async function git(cwd, args, timeout = 60_000) {
  const r = await exec("git", args, { cwd, timeout });
  return { ok: r.code === 0, out: (r.stdout ?? "").trim(), err: (r.stderr || r.stdout || "").trim() };
}

// SETUP in a checkout: ok, or the last lines it printed.
async function setup(cwd) {
  const r = await exec("sh", ["-c", `${SETUP} 2>&1`], { cwd, timeout: 600_000 });
  return r.code === 0 ? "" : (r.stdout || r.stderr || "").trim().split("\n").slice(-6).join("\n") || `${SETUP} failed`;
}

// {checkout path: port}; {} when absent or mid-write.
async function demoPorts(repo) {
  try { return JSON.parse((await readTextFile(`${repo.main}/.git/${PORTS}`)) || "{}"); } catch { return {}; }
}
const url = (port) => `http://localhost:${port}/`;
const serving = async (port) => (await exec("curl", ["-s", "-o", "/dev/null", "-m", "1", url(port)])).code === 0;

const slug = (branch) => branch.replace(/[^\p{L}\p{M}\p{N}._-]+/gu, "-").replace(/^-+|-+$/g, "");
const inside = (p, dir) => p === dir || p.startsWith(dir + "/");

// Where a space is now: its first terminal's live folder, else where it was made.
function place(s) {
  if (!s) return null;
  const tabs = new Set((s.tabs ?? []).map((c) => c.id));
  return panes().find((p) => tabs.has(p.tab) && p.cwd)?.cwd || s.folder || null;
}

// The same setting Herdr's own New worktree uses.
async function worktreeRoot() {
  const raw = await readTextFile(`${appHome()}/settings.json`);
  let dir = (raw ? JSON.parse(raw) : {})?.worktrees?.directory ?? "~/.herdr/worktrees";
  if (dir === "~" || dir.startsWith("~/")) dir = home() + dir.slice(1);
  return dir.replace(/\/+$/, "");
}

export default class Worktrees extends View {
  init(_props, cx) {
    this.repo = undefined; // { main, name, base }; null: the shown space isn't in git
    this.list = []; // [{ path, branch, main, behind, ahead, dirty }]
    this.selected = null; // a checkout's path
    this.confirm = null; // the path a second Delete removes
    this.busy = "";
    this.message = "";
    this.error = false;
    this.naming = false;
    this.input = InputState.new({ placeholder: "branch name" });
    this.input.on("submit", (_e, cx) => this.create(cx));
    if (checking()) return;
    this.refresh(cx);
    cx.timer.every(5000, (cx) => this.refresh(cx));
  }

  // Reads the shown space's repo and every checkout's position against main.
  refresh(cx) {
    if (this.loading || this.busy) return;
    this.loading = true;
    cx.spawn(async (cx) => {
      try {
        const folder = place(spaces().find((s) => s.active));
        const repo = folder ? await this.repoOf(folder) : null;
        if (!repo) { this.repo = null; this.list = []; return; }
        const raw = await git(repo.main, ["worktree", "list", "--porcelain"]);
        const list = [];
        for (const block of raw.out.split(/\n\n+/)) {
          const lines = block.split("\n");
          const path = lines.find((l) => l.startsWith("worktree "))?.slice(9);
          if (!path || lines.some((l) => l === "bare" || l.startsWith("prunable"))) continue;
          const branch = lines.find((l) => l.startsWith("branch "))?.slice(7).replace(/^refs\/heads\//, "") ?? null;
          list.push({ path, branch, main: list.length === 0 });
        }
        repo.base = list[0]?.branch ?? "main";
        const ports = await demoPorts(repo);
        await Promise.all(list.map(async (w) => {
          const status = await git(w.path, [...READ, "status", "--porcelain"]);
          w.dirty = status.ok && status.out !== "";
          // main: against its upstream (what Push sends); a checkout: against main.
          const range = w.main ? "@{upstream}...HEAD" : `${repo.base}...${w.branch}`;
          const counts = w.branch ? await git(w.main ? w.path : repo.main, ["rev-list", "--left-right", "--count", range]) : { ok: false };
          [w.behind, w.ahead] = counts.ok ? counts.out.split(/\s+/).map(Number) : [0, 0];
          w.conflicts = w.main || !w.ahead || !w.behind ? [] : await this.conflicts(repo, w.branch);
          w.port = ports[w.path] ?? null;
          w.serving = w.port ? await serving(w.port) : false;
        }));
        this.repo = repo;
        this.list = list;
        if (!list.some((w) => w.path === this.selected)) this.selected = null;
      } catch (e) {
        this.message = String(e);
        this.error = true;
      } finally {
        this.loading = false;
        cx.notify();
      }
    });
  }

  // The files a merge of branch into main would conflict in: a trial merge
  // (git merge-tree) that writes no checkout, index or ref.
  async conflicts(repo, branch) {
    const r = await exec("git", ["merge-tree", "--write-tree", "--name-only", repo.base, branch], { cwd: repo.main, timeout: 60_000 });
    if (r.code !== 1) return []; // 0: merges cleanly; anything else: couldn't tell
    const lines = (r.stdout ?? "").split("\n");
    const end = lines.indexOf("", 1);
    return lines.slice(1, end < 0 ? undefined : end).filter(Boolean);
  }

  async repoOf(folder) {
    const common = await git(folder, ["rev-parse", "--path-format=absolute", "--git-common-dir"]);
    if (!common.ok) return null;
    const main = common.out.endsWith("/.git") ? common.out.slice(0, -5) : common.out;
    return { main, name: main.split("/").pop() };
  }

  // The active space's worktrees tab, and its terminal panes in order.
  grid() {
    const space = spaces().find((s) => s.active);
    const tab = space?.tabs.find((t) => t.name === TAB);
    return { space, tab, panes: tab ? panes().filter((p) => p.tab === tab.id && p.terminal) : [] };
  }

  // The grid pane working in a checkout (the deepest checkout holding its folder).
  paneOf(w, grid = this.grid()) {
    return grid.panes.find((p) => p.cwd && inside(p.cwd, w.path) &&
      !this.list.some((o) => o.path.length > w.path.length && inside(p.cwd, o.path)));
  }

  open(w, argv = AGENT) {
    const g = this.grid();
    if (!g.space) return;
    if (!g.tab) return run("tab.new", { name: TAB, space: g.space.id, folder: w.path, argv });
    const n = g.panes.length;
    if (n >= MAX_OPEN) return this.say(`${MAX_OPEN} are open: close one first`, true);
    // 1 → two side by side → one and two → 2×2.
    const [target, side] = n === 0 ? [null, "tab"] : n === 1 ? [g.panes[0], "right"] : n === 2 ? [g.panes[1], "down"] : [g.panes[0], "down"];
    run("tab.select", { id: g.tab.id });
    if (target) run("pane.focus", { pane: target.pane });
    run("terminal.open", { folder: w.path, argv, place: side });
  }

  close(w) {
    const p = this.paneOf(w);
    if (p) run("pane.close", { pane: p.pane });
  }

  select(w) {
    this.selected = w.path;
    this.confirm = null;
    if (w.conflicts?.length) this.say(`${w.branch} conflicts with ${this.repo.base} in ${w.conflicts.join(", ")}: merge ${this.repo.base} into it there`);
    const g = this.grid(), p = this.paneOf(w, g);
    if (p) {
      run("tab.select", { id: g.tab.id });
      run("pane.focus", { pane: p.pane });
    } else if (g.panes.length < MAX_OPEN) {
      this.open(w);
    }
  }

  say(message, error = false) {
    this.message = message;
    this.error = error;
    if (error) notify({ title: "Worktrees", body: message, level: "error" });
  }

  // Runs one operation at a time, showing what it's doing.
  act(cx, label, work) {
    if (this.busy) return;
    this.busy = label;
    this.message = "";
    this.error = false;
    cx.notify();
    cx.spawn(async (cx) => {
      try { await work(); } catch (e) { this.say(String(e), true); }
      this.busy = "";
      cx.notify();
      this.refresh(cx);
    });
  }

  create(cx) {
    const branch = this.input.value().trim();
    if (!branch || !this.repo) return;
    const repo = this.repo;
    this.act(cx, `creating ${branch}`, async () => {
      const path = `${await worktreeRoot()}/${repo.name}/${slug(branch)}`;
      const add = await git(repo.main, ["worktree", "add", "-b", branch, path, repo.base], 600_000);
      if (!add.ok) return this.say(add.err, true);
      this.naming = false;
      this.input.set_value("");
      this.selected = path;
      const failed = await setup(path);
      if (failed) this.say(`${branch}: ${failed}`, true);
      else this.say(`${branch} created`);
      this.open({ path }, ["claude"]);
    });
  }

  merge(cx, w) {
    const repo = this.repo;
    if (!w.ahead) return this.say(`${w.branch} has no commits to merge`);
    this.act(cx, `merging ${w.branch}`, async () => {
      const on = await git(repo.main, ["branch", "--show-current"]);
      if (on.out !== repo.base) return this.say(`the main checkout is on ${on.out || "a detached HEAD"}, not ${repo.base}`, true);
      const r = await git(repo.main, ["merge", "--no-edit", w.branch], 120_000);
      if (!r.ok) {
        await git(repo.main, ["merge", "--abort"]);
        return this.say(`merge of ${w.branch} stopped, main unchanged: ${r.err}`, true);
      }
      const failed = await setup(repo.main);
      if (failed) this.say(`merged ${w.branch}; on main: ${failed}`, true);
      else this.say(`merged ${w.branch} into ${repo.base}`);
    });
  }

  // Opens the checkout's demo in the browser, starting its server first if
  // it isn't running (a background terminal, `demo-<checkout>`).
  demo(cx, w) {
    const repo = this.repo;
    const name = `demo-${w.main ? "main" : w.path.split("/").pop()}`.toLowerCase().replace(/[^a-z0-9_-]+/g, "-").slice(0, 32);
    this.act(cx, `demo ${w.branch ?? ""}`, async () => {
      const port = async () => (await demoPorts(repo))[w.path] ?? null;
      let p = await port();
      if (!(p && (await serving(p)))) {
        // A checkout from before per-checkout ports would take main's 8080.
        if ((await readTextFile(`${w.path}/cmd/demo/checkout.go`)) === null) {
          return this.say(`${w.branch}'s demo has no port of its own yet: Sync it (or merge ${repo.base} into it) first`, true);
        }
        const state = async () => {
          const r = await hc(["get", name, "--json"]);
          try { return r.code === 0 ? JSON.parse(r.stdout).state : null; } catch { return null; }
        };
        if ((await state()) !== "live") {
          const r = await hc(["run", "--bg", "--no-parent", "--name", name, "--cwd", w.path, "--", ...DEMO]);
          if (r.code !== 0) return this.say(`couldn't start the demo: ${(r.stderr || r.stdout).trim()}`, true);
        }
        // It builds first (make gen, go run): up to two minutes, unless it exits.
        this.busy = `starting demo ${w.branch ?? ""}`;
        for (let i = 0; i < 120; i++) {
          p = await port();
          if (p && (await serving(p))) break;
          if ((await state()) !== "live") {
            const out = (await hc(["read", name])).stdout.trim().split("\n").slice(-3).join(" · ");
            return this.say(`the demo stopped: ${out}`, true);
          }
          await exec("sleep", ["1"]);
        }
        if (!(p && (await serving(p)))) return this.say(`the demo didn't come up in two minutes: hc read ${name}`, true);
      }
      await exec("open", [url(p)]);
      this.say(`demo ${w.branch ?? ""} on :${p}`);
    });
  }

  push(cx) {
    const repo = this.repo;
    this.act(cx, "pushing", async () => {
      const r = await git(repo.main, ["push"], 120_000);
      if (r.ok) this.say(`pushed ${repo.base}`);
      else this.say(r.err, true);
    });
  }

  sync(cx) {
    const repo = this.repo;
    const behind = this.list.filter((w) => !w.main && w.behind > 0);
    const ready = behind.filter((w) => !w.dirty && !w.ahead);
    const skipped = behind.filter((w) => w.dirty || w.ahead).map((w) => w.branch);
    if (!behind.length) return this.say("every checkout is up to date");
    this.act(cx, `syncing ${ready.length}`, async () => {
      const failed = [];
      for (const w of ready) {
        this.busy = `syncing ${w.branch}`;
        const r = await git(w.path, ["merge", "--ff-only", repo.base], 120_000);
        const bad = r.ok ? await setup(w.path) : r.err;
        if (bad) failed.push(`${w.branch}: ${bad}`);
      }
      const parts = [`synced ${ready.length - failed.length}`];
      if (skipped.length) parts.push(`skipped ${skipped.join(", ")} (own commits or changes)`);
      if (failed.length) this.say([...parts, ...failed].join(" · "), true);
      else this.say(parts.join(" · "));
    });
  }

  remove(cx, w) {
    const repo = this.repo;
    if (this.confirm !== w.path) {
      this.confirm = w.path;
      const risks = [w.dirty && "uncommitted changes", w.ahead && `${w.ahead} commit${w.ahead > 1 ? "s" : ""} not in ${repo.base}`].filter(Boolean);
      return this.say(`Delete ${w.branch}${risks.length ? `: ${risks.join(" and ")} will be lost` : ""}? Delete again to confirm`, risks.length > 0);
    }
    this.confirm = null;
    this.act(cx, `deleting ${w.branch}`, async () => {
      this.close(w);
      const r = await git(repo.main, ["worktree", "remove", "--force", w.path], 600_000);
      if (!r.ok) return this.say(r.err, true);
      const b = await git(repo.main, ["branch", "-D", w.branch]);
      if (!b.ok) return this.say(b.err, true);
      this.selected = null;
      this.say(`deleted ${w.branch}`);
    });
  }

  // The checkout a folder is in (the deepest that holds it).
  worktreeAt(folder) {
    if (!folder) return null;
    return this.list.filter((w) => inside(folder, w.path)).sort((a, b) => b.path.length - a.path.length)[0] ?? null;
  }

  // Focusing a pane selects its checkout, so the bar's buttons act on the
  // checkout you're looking at. Only on a focus change: a chip clicked
  // without opening (four open) keeps its selection until focus moves.
  followFocus() {
    if (!this.list.length) return; // not read yet: follow once it is
    const f = focused();
    const key = f?.pane ?? f?.terminal ?? null;
    if (key === this.lastFocus) return;
    this.lastFocus = key;
    const w = this.worktreeAt(f?.cwd);
    if (w && w.path !== this.selected) {
      this.selected = w.path;
      this.confirm = null;
    }
  }

  render(cx) {
    this.followFocus();
    const t = theme();
    const statusColor = { working: t.working, blocked: t.blocked, done: t.done, idle: t.idle };
    const grid = this.grid();
    const sel = this.list.find((w) => w.path === this.selected);
    const selOpen = sel && this.paneOf(sel, grid);

    const button = (id, label, enabled, onClick, danger = false) =>
      div().id(id).flex_none().h(26).px(10).flex().items_center().rounded(6).text_size(12)
        .bg(t.raised).text_color(!enabled ? t.dim : danger ? t.blocked : t.foreground)
        .when(enabled, (el) => el.cursor_pointer().hover((s) => s.bg(t.hover)).on_click((_e, cx) => { onClick(cx); cx.notify(); }))
        .child(label);

    const chip = (w) => {
      const pane = this.paneOf(w, grid);
      const on = w.path === this.selected;
      return h_flex().id(`wt-${w.path}`).flex_none().h(28).px(10).gap(6).items_center().rounded(7).cursor_pointer()
        .border_1().border_color(on ? t.accent : pane ? t.border : t.chrome)
        .bg(pane ? t.selected : t.surface).hover((s) => s.bg(t.hover))
        .on_click((_e, cx) => { this.select(w); cx.notify(); })
        .when(Boolean(pane?.status), (el) => el.child(div().w(7).h(7).rounded(4).bg(statusColor[pane.status] ?? t.unknown)))
        .child(div().text_color(pane || on ? t.foreground : t.muted).child(w.main ? `${w.branch ?? "main"} (main checkout)` : w.branch ?? "detached"))
        .when(w.behind > 0, (el) => el.child(div().text_size(11).text_color(t.dim).child(`↓${w.behind}`)))
        .when(w.ahead > 0, (el) => el.child(div().text_size(11).text_color(t.accent).child(`↑${w.ahead}`)))
        .when(w.dirty, (el) => el.child(div().text_size(11).text_color(t.blocked).child("●")))
        .when(w.conflicts?.length > 0, (el) => el.child(div().text_size(11).text_color(t.blocked).child("⚠ conflicts")))
        .when(w.serving, (el) => el.child(div().text_size(11).text_color(t.done).child(`:${w.port}`)))
        // Open: a little x closes its pane (the checkout stays).
        .when(Boolean(pane), (el) => el.child(div().id(`x-${w.path}`).ml(2).w(16).h(16).flex().items_center().justify_center()
          .rounded(4).text_size(11).text_color(t.dim).cursor_pointer().hover((s) => s.bg(t.raised).text_color(t.foreground))
          .on_mouse_down("left", (_e, cx) => cx.stop_propagation())
          .on_click((_e, cx) => { cx.stop_propagation(); this.close(w); cx.notify(); })
          .child("✕")));
    };

    const sep = () => div().flex_none().w(1).h(20).bg(t.border);
    const mainW = this.list.find((w) => w.main);
    const idle = !this.busy && Boolean(this.repo);

    return h_flex().id("worktrees").size_full().px(10).gap(8).items_center().bg(t.chrome).text_size(12)
      .when(this.repo === null, (el) => el.child(div().text_color(t.dim).child("This space isn't in a git repository")))
      .when(Boolean(this.repo), (el) => el
        .child(h_flex().id("chips").flex_1().min_w(0).gap(6).items_center().overflow_x_scrollbar().children(this.list.map(chip)))
        .child(sep())
        .child(button("open", selOpen ? "Close" : "Open", idle && Boolean(sel), () => (selOpen ? this.close(sel) : this.open(sel))))
        .child(button("demo", "Demo", idle && Boolean(sel), (cx) => this.demo(cx, sel)))
        .child(button("merge", "Merge", idle && Boolean(sel && !sel.main && sel.ahead && !sel.conflicts?.length), (cx) => this.merge(cx, sel)))
        .child(button("delete", this.confirm && this.confirm === sel?.path ? "Delete!" : "Delete", idle && Boolean(sel && !sel.main), (cx) => this.remove(cx, sel), true))
        .child(sep())
        .when(this.naming, (el) => el.child(div().id("name").flex_none().w(170).h(26).px(8).flex().items_center().rounded(6).border_1().border_color(t.accent).bg(t.background)
          .on_mouse_down("left", (_e, cx) => cx.stop_propagation())
          .child(Input.new(this.input).h_full().text_size(12).text_color(t.foreground))))
        .child(button("new", this.naming ? "Cancel" : "New", idle, () => { this.naming = !this.naming; if (this.naming) this.input.focus(); }))
        .child(button("sync", "Sync all", idle, (cx) => this.sync(cx)))
        .child(button("push", mainW?.ahead ? `Push main ↑${mainW.ahead}` : "Push main", idle && Boolean(mainW?.ahead), (cx) => this.push(cx))))
      .when(Boolean(this.busy), (el) => el.child(h_flex().flex_none().gap(6).text_color(t.muted)
        .child(div().frames(["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"], 90)).child(this.busy)))
      .when(!this.busy && Boolean(this.message), (el) => el.child(div().flex_none().max_w(360).truncate().text_color(this.error ? t.blocked : t.muted).child(this.message)));
  }
}
