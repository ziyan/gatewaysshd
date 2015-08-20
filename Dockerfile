FROM debian:wheezy

ADD gatewaysshd /opt/bin/gatewaysshd

WORKDIR /data
VOLUME ["/data"]

ENTRYPOINT ["/opt/bin/gatewaysshd"]

