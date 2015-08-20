.PHONY: all
all: build

.PHONY: build
build: gatewaysshd

gatewaysshd: $(shell find . -iname '*.go' -print)
	godep go build github.com/ziyan/gatewaysshd/cmd/gatewaysshd

.PHONY: test
test: build
	godep go test ./...

.PHONY: docker
docker: test
	docker build -t ziyan/gatewaysshd .

.PHONY: save
save:
	godep save ./...

.PHONY: format
format:
	gofmt -l -w cmd pkg

