FROM busybox

ADD gatewaysshd /bin/gatewaysshd

USER nobody

ENTRYPOINT ["/bin/gatewaysshd"]
