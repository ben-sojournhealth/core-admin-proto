#!/bin/sh
# Installs the worktrees bar into the Herdr app (`make herdr`):
#   - copies tools/herdr/worktrees into Herdr's mods folder (a link isn't
#     enough: Herdr reloads a mod on changes in its mods folder, and edits
#     here land outside it), so rerun it after editing the mod;
#   - makes a "worktrees" layout: Herdr's main layout with the bar under the
#     tab strip, and switches to it. Rerunning keeps an existing one.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
home=${HERDR_APP_HOME:-$HOME/.config/herdr-app}

mod="$home/mods/worktrees"
[ -L "$mod" ] && rm -f "$mod" # an earlier install linked it
mkdir -p "$mod"
cp "$here/worktrees/main.js" "$here/worktrees/mod.json" "$mod/"
herdr-app check "$mod"

if [ ! -f "$home/layouts/worktrees.json" ]; then
  herdr-app do layout.clone '{"name":"main","as":"worktrees"}' >/dev/null
  python3 - "$home/layouts/worktrees.json" <<'EOF'
import json, sys
path = sys.argv[1]
layout = json.load(open(path))

def find(node, id):
    if node.get("id") == id:
        return node
    for child in node.get("children", []):
        hit = find(child, id)
        if hit:
            return hit

work = find(layout, "work")
at = next(i for i, c in enumerate(work["children"]) if c.get("id") == "tabs")
work["children"].insert(at + 1, {
    "id": "worktrees",
    "type": "block",
    "size": {"px": 44},
    "content": {"mod": "worktrees"},
})
json.dump(layout, open(path, "w"), indent=2)
EOF
fi
herdr-app do layout.switch '{"name":"worktrees"}' >/dev/null
echo "worktrees bar installed; layout: worktrees"
