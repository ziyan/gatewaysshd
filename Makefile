override BUILD_DIR := build
override DOCKER_TAG := ziyan/gatewaysshd:latest
override GOFMTARGS := $(shell find . -mindepth 1 -maxdepth 1 -type d -not \( -path ./.git -or -path ./vendor -or -path ./build \)) $(shell find . -mindepth 1 -maxdepth 1 -type f -iname '*.go')

.PHONY: all
all: format build test

# format code
.PHONY: format
format:
	gofmt -l -w ${GOFMTARGS}

# check that code has been formatted
.PHONY: check
check:
	@if [ ! -z "$$(gofmt -l -e -d ${GOFMTARGS})" ]; then \
		gofmt -l -e -d ${GOFMTARGS}; \
		echo "ERROR: Please format your code before you commit and push!" >&2; \
		exit 1; \
	fi

# compile
.PHONY: generate
generate:
	@CGO_ENABLED=0 go generate -mod=readonly ./...

.PHONY: build
build: gatewaysshd

gatewaysshd: $(shell find . -iname '*.go') generate
	mkdir -p ${BUILD_DIR}
	CGO_ENABLED=0 go build -mod=readonly -o ${BUILD_DIR}/gatewaysshd -ldflags="-X main.Commit=$$(git describe --match=NeVeRmAtCh --always --abbrev=40 --dirty)" .
	objcopy --strip-all ${BUILD_DIR}/gatewaysshd

# run lint
.PHONY: lint
lint:
	@set -e; \
	if ! hash golangci-lint >/dev/null 2>&1; then \
		go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.57.2; \
	fi; \
	golangci-lint run --modules-download-mode=readonly

# run tests
.PHONY: test
test: generate
	@set -e; \
	if ! hash gotestsum >/dev/null 2>&1; then \
		go install gotest.tools/gotestsum@v1.12.0; \
	fi; \
	if hash docker >/dev/null 2>&1; then \
		CONTAINER="$$(docker run \
			-d \
			--rm \
			-e POSTGRES_DB=gatewaysshd \
			-e POSTGRES_USER=gatewaysshd \
			-e POSTGRES_PASSWORD=gatewaysshd \
			-e POSTGRES_HOST_AUTH_METHOD=trust \
			postgres)"; \
		trap "docker kill $${CONTAINER} >/dev/null 2>&1" EXIT; \
		until docker exec $${CONTAINER} pg_isready >/dev/null 2>&1; do \
			sleep 1; \
		done; \
		IP="$$(docker inspect --format '{{ .NetworkSettings.IPAddress }}' $${CONTAINER})"; \
		export GATEWAYSSHD_TEST_DATABASE_HOST="$${IP}"; \
	fi; \
	gotestsum --format testname -- -mod=readonly -cover -coverprofile=${BUILD_DIR}/coverage.out ./...; \
	go tool cover -html=${BUILD_DIR}/coverage.out -o ${BUILD_DIR}/coverage.html; \
	go tool cover -func=${BUILD_DIR}/coverage.out

# benchmark tests
.PHONY: benchmark
benchmark:
	@go test -mod=readonly -bench=. ./...

# watch for code change and compile
.PHONY: watch
watch:
	@set -e; \
	while true; do \
		inotifywait --quiet --recursive --event modify --event delete --event move .; \
		$(MAKE) format lint build || true; \
	done
# make docker image
.PHONY: docker
docker: build
	@mkdir -p ${BUILD_DIR}
	@cp Dockerfile ${BUILD_DIR}
	@docker build -t ${DOCKER_TAG} ${BUILD_DIR}

# generate static documents
.PHONY: docs
docs:
	@set -e; \
	rm -rf ${BUILD_DIR}/docs; \
	mkdir -p ${BUILD_DIR}/docs; \
	SERVERPKG="$$(grep ^module go.mod | awk '{print $$2}')"; \
	SERVERPORT="$$(shuf -i 62000-65000 -n 1)"; \
	SERVERURL="http://127.0.0.1:$${SERVERPORT}"; \
	godoc -v -http=127.0.0.1:$${SERVERPORT}& SERVERPID="$$!"; \
	trap "kill $${SERVERPID}" EXIT; \
	while ! wget --quiet --output-document=/dev/null $${SERVERURL}/pkg/$${SERVERPKG}/; do \
		sleep 0.1; \
	done; \
	cd ${BUILD_DIR}/docs; \
	wget \
		--execute robots=off \
		--mirror \
		--quiet \
		--no-host-directories \
		--no-use-server-timestamps \
		--no-parent \
		--convert-links \
		--adjust-extension \
		--page-requisites \
		--span-hosts \
		--domains 127.0.0.1 \
		--exclude-directories src/$${SERVERPKG}/${BUILD_DIR}/,src/$${SERVERPKG}/vendor/ \
		$${SERVERURL}/lib/godoc/ \
		$${SERVERURL}/pkg/$${SERVERPKG}/ \
		$${SERVERURL}/src/$${SERVERPKG}/ || true; \
	find -type f -exec sed -i -- "s,$${SERVERURL},,g" {} \;
