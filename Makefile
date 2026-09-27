GO := $(CURDIR)/.tools/go/bin/go
BUF := $(CURDIR)/.tools/bin/buf
GO_ENV := GOCACHE=$(CURDIR)/.cache/go-build GOMODCACHE=$(CURDIR)/.cache/go-mod

.PHONY: tools proto-generate proto-lint proto-breaking proto-baseline go-tidy go-test relay-build protocol-check csharp-compat

tools:
	./scripts/bootstrap-tools.sh

# ponytail: v1 has one generated file per language; use a manifest sync if that changes.
proto-generate: tools
	@set -eu; \
	  mkdir -p "$(CURDIR)/.cache"; \
	  stage=$$(mktemp -d "$(CURDIR)/.cache/proto-generate.XXXXXX"); \
	  trap 'rm -rf -- "$$stage"' 0 1 2 15; \
	  $(BUF) generate --output "$$stage"; \
	  go_output="$$stage/gen/go/relay/v1/relay.pb.go"; \
	  csharp_output="$$stage/unity/RelaySample/Assets/Relay/Generated/Relay.cs"; \
	  test -s "$$go_output"; \
	  test -s "$$csharp_output"; \
	  mv "$$go_output" "$(CURDIR)/gen/go/relay/v1/relay.pb.go"; \
	  mv "$$csharp_output" "$(CURDIR)/unity/RelaySample/Assets/Relay/Generated/Relay.cs"

proto-lint: tools
	$(BUF) lint

proto-breaking: tools
	$(BUF) breaking --against api/relay/v1/relay-v1.binpb

proto-baseline: tools
	$(BUF) build -o api/relay/v1/relay-v1.binpb

go-tidy: tools
	$(GO_ENV) $(GO) mod tidy

go-test: tools
	$(GO_ENV) $(GO) test ./...

relay-build: tools
	mkdir -p $(CURDIR)/out
	$(GO_ENV) $(GO) build -o $(CURDIR)/out/lobby-relay ./cmd/lobby-relay

csharp-compat:
	dotnet restore --artifacts-path $(CURDIR)/out/dotnet --locked-mode test/compat/csharp/Relay.Protocol.Compat.csproj
	dotnet run --artifacts-path $(CURDIR)/out/dotnet --no-restore --project test/compat/csharp/Relay.Protocol.Compat.csproj -- internal/protocol/testdata/v1-golden.json

protocol-check: proto-lint proto-breaking proto-generate
	git diff --exit-code -- api/relay/v1 gen/go/relay/v1 unity/RelaySample/Assets/Relay/Generated
	test -z "$$(git ls-files --others --exclude-standard -- api/relay/v1 gen/go/relay/v1 unity/RelaySample/Assets/Relay/Generated)"
	$(GO_ENV) $(GO) test ./internal/protocol
	$(MAKE) csharp-compat
