SQLC_IMAGE := sqlc/sqlc:1.29.0
DEV_ENV := APP_ENV=development \
	DATABASE_URL=postgres://postgres:postgres@localhost:5432/wallet?sslmode=disable \
	RABBITMQ_URL=amqp://guest:guest@localhost:5672/

.PHONY: generate test lint run up down e2e

generate:
	docker run --rm -u $$(id -u):$$(id -g) -v $(CURDIR):/src -w /src $(SQLC_IMAGE) generate

test:
	go test ./...

lint:
	test -z "$$(gofmt -l .)"
	go vet ./...
	if command -v golangci-lint >/dev/null; then golangci-lint run; fi

run:
	$(DEV_ENV) go run ./cmd/wallet-api

up:
	docker compose up -d

down:
	docker compose down

e2e:
	scripts/e2e.sh
