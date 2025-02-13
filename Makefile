VERSION := $(shell git describe --tags --abbrev=0 2>/dev/null || git --no-pager log --no-decorate -n 1 --pretty=%h)

export CGO_ENABLED?=0

IMAGE?=quay.io/mudler/localrag:latest

print-version:
	@echo "Version: ${VERSION}"

build:
	@go build -v -ldflags "-X github.com/mudler/localrag/internal/versioning.ApplicationVersion=${VERSION}" -o ./localrag ./

run: build
	@./localrag

.PHONY: test
test:
	@go test -coverprofile=coverage.txt -covermode=atomic -v ./...

clean:
	@rm -rf localrag

docker-build:
	@docker build -t $(IMAGE) .

docker-compose-up:
	@docker compose up -d

prepare-e2e: docker-build docker-compose-up

clean-e2e:
	@docker compose -f docker-compose.yml down

test-e2e: prepare-e2e run-e2e clean-e2e

run-e2e:
	E2E=true go test -v ./test/e2e/...