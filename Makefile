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
test:
	@go test -coverprofile=coverage.txt -covermode=atomic -v ./...

test-unit:
	@go test -v ./rag/... ./pkg/...

test-integration:
	@INTEGRATION=true go test -v ./test/integration/...

test-e2e: prepare-e2e run-e2e clean-e2e

test-all: test-unit test-integration

test-coverage:
	@go test -coverprofile=coverage.txt -covermode=atomic -v ./...
	@go tool cover -html=coverage.txt -o coverage.html

clean:
	@rm -rf localrecall

docker-build:
	@docker build -t $(IMAGE) .

docker-push:
	@docker push $(IMAGE)

docker-compose-up:
	@docker compose up -d

prepare-e2e: docker-build docker-compose-up

clean-e2e:
	@docker compose -f docker-compose.yml down

test-e2e: prepare-e2e run-e2e clean-e2e

run-e2e:
	E2E=true go test -v ./test/e2e/...
