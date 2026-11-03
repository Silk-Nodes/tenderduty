# Build and install tenderduty. Needs Go 1.26 or later.
FLAGS := -ldflags '-s -w' -trimpath

.PHONY: build install test vulncheck docker

build:
	go build $(FLAGS) -o tenderduty .

# installs to $(go env GOPATH)/bin, usually ~/go/bin
install:
	go install $(FLAGS) .

test:
	go test ./...

# fails on any advisory our code reaches that docs/security-exceptions.md does not accept
vulncheck:
	scripts/vulncheck.sh

docker:
	docker build -t tenderduty .
