.PHONY: gen lint breaking test check demo screenshot herdr pg pg-stop

# Go code, plus a descriptor set that keeps the .proto comments (the Go
# code drops them) for field explanations in the demos. gen/ isn't
# committed (only gen/embed.go is): run this after cloning and after
# changing or merging protos.
gen:
	buf generate
	buf build --as-file-descriptor-set -o gen/descriptors.binpb

lint: gen
	buf lint
	go vet ./...

# Compare protos against main to catch wire/JSON-breaking changes.
breaking:
	buf breaking --against ".git#branch=main"

test: gen
	go test ./...

check: gen lint test

# Demos on http://localhost:8080. Everything runs on the local Postgres,
# started if needed (go run ./cmd/pg url prints how to connect).
demo: gen
	go run ./cmd/demo

# docs/domain-map.png, the domain map embedded in the README. Run it after
# changing the map (cmd/demo/web/index.html) and commit the image with it.
# Screenshots the demo running at SHOT_URL (start one with `make demo`)
# with headless Chrome.
CHROME ?= /Applications/Google Chrome.app/Contents/MacOS/Google Chrome
SHOT_URL ?= http://localhost:8080/
screenshot:
	"$(CHROME)" --headless=new --disable-gpu --hide-scrollbars --force-device-scale-factor=2 \
		--window-size=1200,1250 --virtual-time-budget=3000 \
		--screenshot=docs/domain-map.png $(SHOT_URL) 2>/dev/null

# The worktrees bar in the Herdr app (tools/herdr): every worktree as a chip,
# new / open / merge to main / push main / sync all / delete, and up to four
# open side by side.
herdr:
	sh tools/herdr/install.sh

# The local Postgres (embedded, port 5433; data outside every checkout, see
# internal/common/pgdb). Tests and the demo start it if it isn't running;
# it keeps running until stopped.
pg:
	go run ./cmd/pg start

pg-stop:
	go run ./cmd/pg stop
