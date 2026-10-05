.PHONY: setup subtitle-tools doctor media-fixture dev db check test test-integration build up down help

setup:
	node scripts/setup.mjs
	npm --prefix frontend ci
	go -C backend mod download

subtitle-tools:
	node scripts/setup.mjs --subtitle-tools

doctor:
	node scripts/native.mjs

media-fixture:
	node scripts/setup.mjs --media-fixture

dev:
	node scripts/dev.mjs

db:
	docker compose -f compose.yaml -f compose.dev.yaml up -d --wait postgres

SUBTITLE_HELPER := $(abspath bin/subtitle-tools/bin/ffsubsync)
MEDIA_VIDEO := $(abspath bin/test-media/real-helper-v1.mkv)
TEST_HELPERS = $(if $(wildcard $(SUBTITLE_HELPER)),TEST_FFSUBSYNC_PATH="$(SUBTITLE_HELPER)") $(if $(wildcard $(MEDIA_VIDEO)),TEST_MEDIA_VIDEO="$(MEDIA_VIDEO)")

check:
	npm --prefix frontend run lint
	npm --prefix frontend run build
	go -C backend vet ./...
	$(TEST_HELPERS) go -C backend test ./...

test:
	$(TEST_HELPERS) go -C backend test ./...

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

help:
	@echo "make setup             create .env, install frontend, Go, and subtitle dependencies"
	@echo "make subtitle-tools    install ffsubsync into the ignored bin/subtitle-tools venv"
	@echo "make doctor            report missing or outdated native tools with install hints"
	@echo "make media-fixture     rebuild the synthetic video used by real-helper tests"
	@echo "make dev               run PostgreSQL, the API, and Vite"
	@echo "make db                start PostgreSQL only"
	@echo "make check             lint, frontend build, Go vet and tests"
	@echo "make test              Go tests only"
	@echo "make test-integration  Go tests with a temporary PostgreSQL database"
	@echo "make build             build bin/constellarr"
	@echo "make up / make down    start or stop the Compose stack"
