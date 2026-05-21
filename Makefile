# Livepeer Video Gateway — root Makefile
#
# Root targets for the Go gateway, the three Lit SPAs, and the
# compose stack (db + minio + livepeer daemons).

.DEFAULT_GOAL := help

# ── Docker image publishing ─────────────────────────────────────────
# Matches the convention used by sibling repos (capability-broker,
# payment-daemon, service-registry-daemon): manual publish with
# multi-arch buildx, pushed to tztcloud/* on Docker Hub. Authenticate
# first with `docker login docker.io -u <your-dockerhub-username>`.
IMAGE ?= tztcloud/livepeer-video-gateway
TAG   ?= dev

.PHONY: help install build lint test dev down logs clean smoke web site-ui portal-ui admin-ui \
        go-build go-test go-lint go-tidy proto sqlc \
        docker-build docker-publish

help:
	@echo "Livepeer Video Gateway — root targets"
	@echo ""
	@echo "  make install        pnpm install for web/* (Go has no install step)"
	@echo "  make build          go build gateway + pnpm -r build for web/*"
	@echo "  make lint           go vet + pnpm -r lint"
	@echo "  make test           go test + pnpm -r test"
	@echo ""
	@echo "  make dev            bring up gateway + db + rustfs via docker compose"
	@echo "  make dev-livepeer   same as dev, plus payer + resolver daemons"
	@echo "  make down           tear down dev compose stack"
	@echo "  make logs           tail dev compose logs"
	@echo "  make smoke          end-to-end smoke test against the dev stack"
	@echo ""
	@echo "  make web            start site + portal + admin dev servers"
	@echo "  make site-ui        site dev server (:3000)"
	@echo "  make portal-ui      portal dev server (:3001)"
	@echo "  make admin-ui       admin dev server (:3002)"
	@echo ""
	@echo "  make go-build       go build ./..."
	@echo "  make go-test        go test ./..."
	@echo "  make go-lint        go vet ./..."
	@echo "  make go-tidy        go mod tidy"
	@echo "  make proto          regenerate protoc-gen-go stubs into gateway/gen/proto/"
	@echo "  make sqlc           regenerate sqlc queries into gateway/gen/db/"
	@echo ""
	@echo "  make docker-build TAG=v1.3.0"
	@echo "                      build the gateway image as tztcloud/livepeer-video-gateway:<TAG>"
	@echo "  make docker-publish TAG=v1.3.0"
	@echo "                      build multi-arch + push to tztcloud/* on Docker Hub"
	@echo "                      (requires \`docker login docker.io\` first)"
	@echo ""
	@echo "  make clean          remove node_modules, build artifacts, compose volumes"

install:
	pnpm install --frozen-lockfile

build: go-build
	pnpm -r build

lint: go-lint
	pnpm -r lint

test: go-test
	pnpm -r test

dev:
	docker compose up -d db rustfs rustfs-bootstrap rustfs-cors gateway

dev-livepeer:
	docker compose --profile livepeer up -d

down:
	docker compose down

logs:
	docker compose logs -f --tail=200

smoke:
	./scripts/smoke.sh

web:
	@trap 'kill 0' INT TERM EXIT; \
		( cd web/site && node dev-server.js ) & \
		( cd web/portal && node dev-server.js ) & \
		( cd web/admin && node dev-server.js ) & \
		wait

site-ui:
	cd web/site && node dev-server.js

portal-ui:
	cd web/portal && node dev-server.js

admin-ui:
	cd web/admin && node dev-server.js

go-build:
	cd gateway && go build -o bin/gateway ./cmd/gateway

go-test:
	cd gateway && go test ./...

go-lint:
	cd gateway && go vet ./...

go-tidy:
	cd gateway && go mod tidy

proto:
	./scripts/gen-proto.sh

sqlc:
	cd gateway && sqlc generate

clean:
	rm -rf gateway/bin gateway/gen
	pnpm -r exec -- rm -rf node_modules dist dist-test 2>/dev/null || true
	docker compose down -v 2>/dev/null || true

# ── Docker image: build + publish ───────────────────────────────────
# docker-build: single-arch (host's arch) for quick local testing.
#   make docker-build TAG=v1.3.0
# docker-publish: multi-arch (linux/amd64 + linux/arm64), pushed.
#   make docker-publish TAG=v1.3.0
# Requires `docker login docker.io` first; refuses to push :dev.

docker-build:
	docker build -t $(IMAGE):$(TAG) -f gateway/Dockerfile .
	@echo "built $(IMAGE):$(TAG)"

docker-publish:
	@if [ "$(TAG)" = "dev" ]; then \
		echo "refusing to publish :dev — set TAG (e.g. make docker-publish TAG=v1.3.0)"; \
		exit 1; \
	fi
	@# Default Docker driver doesn't support multi-arch — ensure a
	@# docker-container buildx builder exists for cross-arch builds.
	@docker buildx inspect multiarch >/dev/null 2>&1 || \
		docker buildx create --name multiarch --driver docker-container --bootstrap
	docker buildx build --builder multiarch \
		--platform linux/amd64,linux/arm64 \
		--push \
		-t $(IMAGE):$(TAG) \
		-t $(IMAGE):latest \
		-f gateway/Dockerfile \
		.
	@echo "published $(IMAGE):$(TAG) (and :latest)"
