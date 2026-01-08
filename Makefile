VERSION := $(shell git describe --tags --abbrev=0 2>/dev/null || git --no-pager log --no-decorate -n 1 --pretty=%h)

export CGO_ENABLED?=0

IMAGE?=quay.io/mudler/localrecall:latest

print-version:
	@echo "Version: ${VERSION}"

build:
	@go build -v -ldflags "-X github.com/mudler/localrecall/internal/versioning.ApplicationVersion=${VERSION}" -o ./localrecall ./

run: build
	@./localrecall

.PHONY: test test-unit test-integration test-e2e test-all test-coverage
.PHONY: start-localai stop-localai wait-localai
.PHONY: start-test-services stop-test-services

# Start LocalAI service for unit/integration tests
start-localai:
	@echo "Starting LocalAI service..."
	@docker compose up -d localai
	@$(MAKE) wait-localai

# Stop LocalAI service
stop-localai:
	@echo "Stopping LocalAI service..."
	@docker compose stop localai || true

# Wait for LocalAI to be ready
wait-localai:
	@echo "Waiting for LocalAI to be ready..."
	@timeout=120; \
	while [ $$timeout -gt 0 ]; do \
		if command -v curl >/dev/null 2>&1; then \
			if curl -f http://localhost:8081/health >/dev/null 2>&1 || curl -f http://localhost:8081/ready >/dev/null 2>&1 || curl -f http://localhost:8081/ >/dev/null 2>&1 || curl -f http://localhost:8081/v1/models >/dev/null 2>&1; then \
				echo "LocalAI is ready"; \
				break; \
			fi; \
		else \
			if docker compose ps localai 2>/dev/null | grep -q "Up"; then \
				sleep 5; \
				if docker compose exec -T localai curl -f http://localhost:8080/health >/dev/null 2>&1 || docker compose exec -T localai curl -f http://localhost:8080/v1/models >/dev/null 2>&1; then \
					echo "LocalAI is ready"; \
					break; \
				fi; \
			fi; \
		fi; \
		sleep 2; \
		timeout=$$((timeout - 2)); \
	done; \
	if [ $$timeout -le 0 ]; then \
		echo "Error: LocalAI did not become ready in time"; \
		docker compose logs localai | tail -20; \
		exit 1; \
	fi

# Start all test services (LocalAI and PostgreSQL from docker-compose)
start-test-services:
	@echo "Starting test services..."
	@docker compose up -d localai postgres || true
	@$(MAKE) wait-localai
	@echo "Waiting for PostgreSQL to be ready..."
	@timeout=120; \
	while [ $$timeout -gt 0 ]; do \
		if docker compose exec -T postgres pg_isready -U localrecall >/dev/null 2>&1; then \
			echo "PostgreSQL is ready"; \
			break; \
		fi; \
		sleep 2; \
		timeout=$$((timeout - 2)); \
	done; \
	if [ $$timeout -le 0 ]; then \
		echo "Error: PostgreSQL did not become ready in time"; \
		docker compose logs postgres | tail -20; \
		exit 1; \
	fi

# Stop all test services
stop-test-services:
	@echo "Stopping test services..."
	@docker compose stop localai postgres || true

test:
	@go test -coverprofile=coverage.txt -covermode=atomic -v ./...

test-unit: start-test-services
	@echo "Running unit tests..."
	@LOCALAI_ENDPOINT=http://localhost:8081 go test -v ./rag/... ./pkg/...; \
	test_exit=$$?; \
	$(MAKE) stop-test-services; \
	exit $$test_exit

test-integration: start-test-services
	@echo "Running integration tests..."
	@INTEGRATION=true LOCALAI_ENDPOINT=http://localhost:8081 go test -v ./test/integration/...; \
	test_exit=$$?; \
	$(MAKE) stop-test-services; \
	exit $$test_exit

test-e2e: prepare-e2e run-e2e clean-e2e

test-all: start-test-services
	@echo "Running all tests..."
	@LOCALAI_ENDPOINT=http://localhost:8081 go test -v ./rag/... ./pkg/...; \
	unit_exit=$$?; \
	INTEGRATION=true LOCALAI_ENDPOINT=http://localhost:8081 go test -v ./test/integration/...; \
	integration_exit=$$?; \
	$(MAKE) stop-test-services; \
	if [ $$unit_exit -ne 0 ] || [ $$integration_exit -ne 0 ]; then \
		exit 1; \
	fi

test-coverage: start-localai
	@echo "Running tests with coverage..."
	@LOCALAI_ENDPOINT=http://localhost:8081 go test -coverprofile=coverage.txt -covermode=atomic -v ./rag/... ./pkg/...; \
	test_exit=$$?; \
	go tool cover -html=coverage.txt -o coverage.html || true; \
	$(MAKE) stop-localai; \
	exit $$test_exit

clean:
	@rm -rf localrecall

docker-build:
	@docker build -t $(IMAGE) .

docker-push:
	@docker push $(IMAGE)

docker-compose-up:
	@docker compose up -d

prepare-e2e: docker-build
	@echo "Starting E2E test services..."
	@docker compose up -d
	@echo "Waiting for services to be ready..."
	@$(MAKE) wait-localai
	@echo "Waiting for PostgreSQL to be ready (if present)..."
	@if docker compose ps postgres 2>/dev/null | grep -q "postgres"; then \
		timeout=60; \
		while [ $$timeout -gt 0 ]; do \
			if docker compose exec -T postgres pg_isready -U localrecall >/dev/null 2>&1; then \
				echo "PostgreSQL is ready"; \
				break; \
			fi; \
			sleep 2; \
			timeout=$$((timeout - 2)); \
		done; \
		if [ $$timeout -le 0 ]; then \
			echo "Warning: PostgreSQL did not become ready in time"; \
		fi; \
	else \
		echo "PostgreSQL service not found in docker-compose, skipping..."; \
	fi
	@echo "Waiting for RAG server to be ready..."
	@timeout=120; \
	while [ $$timeout -gt 0 ]; do \
		if command -v curl >/dev/null 2>&1; then \
			if curl -f http://localhost:8080/api/collections >/dev/null 2>&1; then \
				echo "RAG server is ready"; \
				break; \
			fi; \
		else \
			if docker compose ps ragserver 2>/dev/null | grep -q "Up"; then \
				echo "RAG server container is running (assuming ready)"; \
				sleep 5; \
				break; \
			fi; \
		fi; \
		sleep 2; \
		timeout=$$((timeout - 2)); \
	done; \
	if [ $$timeout -le 0 ]; then \
		echo "Warning: RAG server did not become ready in time, but continuing..."; \
	fi

clean-e2e:
	@docker compose -f docker-compose.yml down

clean-test-services: stop-test-services
	@echo "Cleaning up test services..."
	@docker compose rm -f localai postgres || true

run-e2e:
	@E2E=true LOCALAI_ENDPOINT=http://localhost:8081 LOCALRECALL_ENDPOINT=http://localhost:8080 go test -v ./test/e2e/...
