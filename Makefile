.PHONY: all
all: build

.PHONY: build
build: gatewaysshd

gatewaysshd: $(shell find . -iname '*.go' -print)
	CGO_ENABLED=0 godep go build github.com/ziyan/gatewaysshd
	objcopy --strip-all gatewaysshd

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
	gofmt -l -w gateway cli
