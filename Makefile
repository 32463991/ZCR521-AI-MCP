SHELL := /bin/sh

.PHONY: test race schema build package verify clean

test:
	go test ./...

race:
	go test -race ./...

schema:
	go run ./cmd/zcr521d schema --output schemas/tools.json

build:
	sh ./scripts/build.sh

package:
	python3 scripts/package.py --repo . --module-stage build/module --bridge-dir build/bridge --dist dist --version 0.01 --epoch 1785340800

verify:
	python3 scripts/verify_module.py dist/ZCR521-Android-AI-MCP-v0.01-universal.zip

clean:
	go clean -cache
