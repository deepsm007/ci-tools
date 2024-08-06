FROM registry.access.redhat.com/ubi9/ubi-minimal:latest

ADD ci-operator-configresolver /usr/bin/ci-operator-configresolver
RUN rm -rf /etc/rhsm-host 
RUN microdnf -y install graphviz && microdnf clean all
RUN ln -s /run/secrets/rhsm /etc/rhsm-host 
ENTRYPOINT ["/usr/bin/ci-operator-configresolver"]
