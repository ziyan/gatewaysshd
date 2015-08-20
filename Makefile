.PHONY: all
all: build

.PHONY: build
build: gatewaysshd

.PHONY: gatewaysshd
gatewaysshd:
	@godep go build github.com/ziyan/gatewaysshd/cmd/gatewaysshd

.PHONY: format
format:
	@gofmt -l -w cmd pkg

.PHONY: save
save:
	@godep save ./...

.PHONY: test
test:
	@godep go test ./...

.PHONY: docker
docker: build
	@docker build -t ziyan/gatewaysshd .

