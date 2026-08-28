.PHONY: build test web run release-snapshot

web:
	cd web && npm ci && npm run build

build: web
	go build -trimpath -ldflags "-s -w" -o scanchat .

test:
	go test ./...

run:
	go run .

release-snapshot:
	goreleaser release --snapshot --clean
