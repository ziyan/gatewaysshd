GOFMTARGS := $(shell find . -mindepth 1 -maxdepth 1 -type d -not -path ./vendor) main.go version.go

.PHONY: all
all: format build test

.PHONY: format
format:
	gofmt -l -w ${GOFMTARGS}

.PHONY: check
check:
	@if [ ! -z "$$(gofmt -l -e -d ${GOFMTARGS})" ]; then \
		gofmt -l -e -d ${GOFMTARGS}; \
		echo "ERROR: Please run make format in go directory before you commit and push!" >&2; \
		exit 1; \
	fi

.PHONY: build
build: gatewaysshd

gatewaysshd: $(shell find . -iname '*.go')
	CGO_ENABLED=0 go build -mod=vendor -o gatewaysshd -ldflags="-X main.Commit=$$(git describe --match=NeVeRmAtCh --always --abbrev=40 --dirty)" .
	objcopy --strip-all gatewaysshd

.PHONY:
test:
	go test -mod=vendor -cover -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	go tool cover -func=coverage.out

.PHONY:
benchmark:
	go test -mod=vendor -bench=. ./...

.PHONY:
watch:
	while true; do \
		inotifywait --quiet --recursive --event modify --event delete --event move --exclude \\.git .; \
		$(MAKE) format build && killall -HUP gatewaysshd >/dev/null 2>&1; \
	done

.PHONY:
docker: build
	docker build -t ziyan/gatewaysshd:latest .
