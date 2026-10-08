.PHONY: build test vet lint migrate run-api run-redirect run-consumer infra

build:
	go build -o bin/ ./cmd/...

test:
	go test ./...

vet:
	go vet ./...

lint:
	golangci-lint run ./...

migrate:
	go run ./cmd/migrate

run-api:
	go run ./cmd/api

run-redirect:
	go run ./cmd/redirect

run-consumer:
	go run ./cmd/consumer

# MongoDB dùng chung với Portal + Redis/Kafka của Service
infra:
	docker compose -f ../am-shortlink-sale-portal/deploy/docker-compose.yml up -d mongo
	docker compose -f deploy/docker-compose.yml up -d
