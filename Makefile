.PHONY: gen lint breaking test check demo pg pg-stop

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

# The local Postgres (embedded, port 5433; data outside every checkout, see
# internal/common/pgdb). Tests and the demo start it if it isn't running;
# it keeps running until stopped.
pg:
	go run ./cmd/pg start

pg-stop:
	go run ./cmd/pg stop
