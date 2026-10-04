.PHONY: setup dev db check test test-integration build up down

setup:
	node scripts/setup.mjs
	npm --prefix frontend ci
	go -C backend mod download

dev:
	node scripts/dev.mjs

db:
	docker compose -f compose.yaml -f compose.dev.yaml up -d --wait postgres

check:
	npm --prefix frontend run lint
	npm --prefix frontend run build
	go -C backend vet ./...
	go -C backend test ./...

test:
	go -C backend test ./...

test-integration:
	node scripts/dev.mjs --test

build:
	npm --prefix frontend run build
	find backend/internal/web/dist -mindepth 1 ! -name .gitkeep -delete
	cp -R frontend/dist/. backend/internal/web/dist/
	mkdir -p bin
	CGO_ENABLED=1 go -C backend build -trimpath -ldflags="-s -w" -o ../bin/constellarr ./cmd/constellarr

up:
	docker compose up -d --build --wait

down:
	docker compose -f compose.yaml -f compose.dev.yaml down
