# Build and install tenderduty. Needs Go 1.26 or later.
FLAGS := -ldflags '-s -w' -trimpath

.PHONY: build install test docker

build:
	go build $(FLAGS) -o tenderduty .

# installs to $(go env GOPATH)/bin, usually ~/go/bin
install:
	go install $(FLAGS) .

test:
	go test ./...

docker:
	docker build -t tenderduty .
