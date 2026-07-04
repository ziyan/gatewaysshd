FROM gcr.io/distroless/static-debian13:nonroot

COPY gatewaysshd /bin/gatewaysshd

ENTRYPOINT ["/bin/gatewaysshd"]
