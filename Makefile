.PHONY: fmt vet lint test test-unit test-integration test-chaos bench bench-load helm-lint build clean

SERVICE_NAME := rag-kb-catalog-service

fmt:
	gofmt -s -w .

vet:
	go vet ./...

lint:
	golangci-lint run ./...

test:
	go test -v -race ./...

test-unit:
	go test -v -race -coverprofile=coverage-unit.out ./internal/... ./test/unit/...

test-integration:
	go test -v -race ./test/integration/...

test-chaos:
	go test -v -race ./test/chaos/...

bench:
	go test -v -bench=. -run=^$$ ./test/benchmark/...

bench-load:
	go test -v -run=^TestLoad ./test/benchmark/...

helm-lint:
	helm lint deployments/helm/rag-kb-catalog-service

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/$(SERVICE_NAME) ./cmd/catalog-service

clean:
	rm -rf bin/ coverage-*.out
