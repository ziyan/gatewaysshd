FROM gcr.io/distroless/static-debian13:nonroot

# the distroless nonroot base does not default the working directory to /, so
# set it explicitly: the daemon's relative default paths (id_rsa.ca.pub,
# id_rsa, id_rsa.pub, geoip.mmdb) resolve against the working directory, and
# deployments mount those files at the container root.
WORKDIR /

COPY gatewaysshd /bin/gatewaysshd

ENTRYPOINT ["/bin/gatewaysshd"]
