BUF ?= buf

.PHONY: mac-selftest gen ios-tokens lint backend backend-test web web-dev ios-project all mac-setup mac-services ios run-local

all: gen backend web

## Regenerate Go / TypeScript / Swift API code from proto/
gen:
	$(BUF) lint
	rm -rf backend/gen web/src/gen ios/NexusKit/Sources/NexusAPI/Generated
	$(BUF) generate
	node scripts/gen-ios-tokens.mjs

## Regenerate the iOS design tokens (Swift + AccentColor) from design/tokens.json
ios-tokens:
	node scripts/gen-ios-tokens.mjs

lint:
	$(BUF) lint
	cd backend && go vet ./...

## Backend (Go)
backend:
	cd backend && go build -o bin/ ./cmd/...

backend-test:
	cd backend && go test ./...

## Web dashboard (React + Tailwind)
web:
	cd web && npm ci && npm run build

web-dev:
	cd web && npm run dev

## iOS (requires macOS + Xcode + XcodeGen)
ios-project:
	cd ios && xcodegen generate

## macOS: "everything on one MacBook" (docs/SETUP_MAC.md)
## Pass flags with ARGS, e.g. make mac-setup ARGS=--dry-run
mac-setup:
	scripts/mac/setup.sh $(ARGS)

## Install/refresh the launchd agents (com.nexus.hub, com.nexus.node)
mac-services:
	scripts/mac/service.sh install all $(ARGS)

## Configure signing, check wire formats, generate and open the iOS project
ios:
	scripts/mac/ios-build.sh $(ARGS)

## Throw-away Hub + node in the foreground (macbook profile, temp dir, port 7701)
run-local:
	scripts/mac/run-local.sh $(ARGS)

## Test everything on this Mac (temporary Hub + node, subscriptions, iOS compile) and write a report
mac-selftest:
	scripts/mac/selftest.sh $(ARGS)
