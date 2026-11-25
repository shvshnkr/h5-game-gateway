SHELL := /bin/bash
BIN := bin

.PHONY: help build test test-integration vet up down migrate logs smoke clean

help:
	@echo "make build            - build gateway + provider-mock into bin/"
	@echo "make test             - unit tests (no infra)"
	@echo "make test-integration - full tests against dockerized mysql/redis"
	@echo "make up               - start the whole stack (docker compose)"
	@echo "make migrate          - apply migrations into the running mysql"
	@echo "make smoke            - end-to-end API smoke test"
	@echo "make down             - stop the stack"
	@echo "make logs             - follow compose logs"

build:
	mkdir -p $(BIN)
	go build -o $(BIN)/gateway ./cmd/gateway
	go build -o $(BIN)/provider-mock ./cmd/provider-mock
	go build -o $(BIN)/migrate ./cmd/migrate

vet:
	go vet ./...

test:
	go test ./... -count=1

test-integration:
	TEST_MYSQL_DSN="gateway:gateway@tcp(127.0.0.1:3306)/gateway_test?multiStatements=true" \
	TEST_REDIS_ADDR="127.0.0.1:6379" \
	go test ./internal/gateway/ -count=1 -v -run Integration

up:
	docker compose up -d --build
	@sleep 2
	$(MAKE) migrate
	@echo "→ open http://localhost:8080"

down:
	docker compose down

migrate:
	go run ./cmd/migrate migrations

logs:
	docker compose logs -f --tail=50

smoke:
	bash scripts/smoke.sh

clean:
	rm -rf $(BIN)
