FROM debian:wheezy

WORKDIR /data
ENTRYPOINT ["/opt/bin/gatewaysshd"]
ADD gatewaysshd /opt/bin/gatewaysshd

ONBUILD ADD id_rsa.ca.pub id_rsa.host-cert.pub id_rsa.host /data/
ONBUILD RUN chown -R root:root /data && chmod -R a+r /data
ONBUILD USER nobody

