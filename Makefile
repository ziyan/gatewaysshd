.PHONY: all
all: build

.PHONY: build
build: gatewaysshd

gatewaysshd: $(shell find . -iname '*.go' -print)
	CGO_ENABLED=0 go build github.com/ziyan/gatewaysshd
	objcopy --strip-all gatewaysshd

.PHONY: test
test: build
	go test -v ./...

.PHONY: docker
docker: test
	docker build -t ziyan/gatewaysshd .

.PHONY: format
format:
	gofmt -l -w gateway cli

.PHONY: clean
clean:
	rm -f gatewaysshd
