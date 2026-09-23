.PHONY: run build test vet lint
run:
	go run .
build:
	go build -o bin/backend .
test:
	go test -race ./...
vet:
	go vet ./...
lint:
	golangci-lint run
