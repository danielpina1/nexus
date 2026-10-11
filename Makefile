BUF ?= buf

.PHONY: gen lint backend backend-test web web-dev ios-project all

all: gen backend web

## Regenerate Go / TypeScript / Swift API code from proto/
gen:
	$(BUF) lint
	rm -rf backend/gen web/src/gen ios/NexusKit/Sources/NexusAPI/Generated
	$(BUF) generate

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
