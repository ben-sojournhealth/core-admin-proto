.PHONY: gen lint breaking test check demo

gen:
	buf generate

lint:
	buf lint
	go vet ./...

# Compare protos against main to catch wire/JSON-breaking changes.
breaking:
	buf breaking --against ".git#branch=main"

test:
	go test ./...

check: lint gen test
	git diff --exit-code -- gen

# Claims sandbox UI on http://localhost:8080
demo:
	go run ./cmd/demo
