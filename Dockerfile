FROM busybox

ADD gatewaysshd /bin/gatewaysshd
ENTRYPOINT ["/bin/gatewaysshd"]

ONBUILD ADD id_rsa.ca.pub id_rsa.host-cert.pub id_rsa.host /
ONBUILD RUN chmod -R a+r id_rsa.ca.pub id_rsa.host-cert.pub id_rsa.host
ONBUILD USER nobody

