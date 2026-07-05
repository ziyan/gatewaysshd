FROM --platform=$BUILDPLATFORM golang:1.25 AS builder

ARG TARGETOS
ARG TARGETARCH
ARG VERSION
ARG COMMIT

WORKDIR /src

COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -mod=vendor \
    -ldflags "-s -w ${VERSION:+-X main.Version=${VERSION}} ${COMMIT:+-X main.Commit=${COMMIT}}" \
    -o /out/gatewaysshd .

FROM gcr.io/distroless/static-debian13:nonroot

# the distroless nonroot base does not default the working directory to /, so
# set it explicitly: the daemon's relative default paths (id_rsa.ca.pub,
# id_rsa, id_rsa.pub, geoip.mmdb) resolve against the working directory, and
# deployments mount those files at the container root.
WORKDIR /

COPY --from=builder /out/gatewaysshd /bin/gatewaysshd

ENTRYPOINT ["/bin/gatewaysshd"]
