.PHONY: check fmt test vet

check: fmt vet test

fmt:
	go fmt ./...

vet:
	go vet ./...

test:
	go test ./...
