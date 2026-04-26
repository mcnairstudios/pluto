BINARY    := pluto
IMAGE     := gavinmcnair/pluto
TAG       := latest
BUILDER   := mybuilder
PLATFORMS := linux/amd64,linux/arm64
VERSION   := $(shell git rev-parse --short HEAD)$(shell git diff --quiet || echo -dirty-$(shell date +%s))

.PHONY: build test docker-build docker-local run logs clean

## Local build
build:
	go build -ldflags="-s -w -X main.buildVersion=$(VERSION)" -o $(BINARY) .

## Run all tests
test:
	go test ./...

## Build multi-arch Docker image and push to Docker Hub
docker-build:
	docker buildx build --builder $(BUILDER) --platform $(PLATFORMS) \
		--build-arg VERSION=$(VERSION) \
		-t $(IMAGE):$(TAG) --push .

## Build local Docker image only (current arch, no push)
docker-local:
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE):$(TAG) .

## Run locally
run: build
	./$(BINARY)

## Clean build artifacts
clean:
	rm -f $(BINARY)
