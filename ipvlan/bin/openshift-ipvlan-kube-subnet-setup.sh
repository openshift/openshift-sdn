#!/bin/bash

set -ex

## docker
if [[ -z "${DOCKER_OPTIONS}" ]]
then
    DOCKER_OPTIONS='-b=none --mtu=1450 --selinux-enabled'
fi

if ! grep -q "^OPTIONS='${DOCKER_OPTIONS}'" /etc/sysconfig/docker
then
    cat <<EOF > /etc/sysconfig/docker
# This file has been modified by openshift-sdn. Please modify the
# DOCKER_OPTIONS variable in the /etc/sysconfig/openshift-sdn-node,
# /etc/sysconfig/openshift-sdn-master or /etc/sysconfig/openshift-sdn
# files (depending on your setup).

OPTIONS='${DOCKER_OPTIONS}'
EOF
fi
systemctl daemon-reload
systemctl restart docker.service

mkdir -p /etc/openshift-sdn
