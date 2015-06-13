#!/bin/bash

set -ex

lock_file=/var/lock/openshift-sdn.lock
subnet_gateway=$1
subnet=$2
cluster_subnet=$3
subnet_mask_len=$4
tun_gateway=$5
printf 'Container network is "%s"; local host has subnet "%s" and gateway "%s".\n' "${cluster_subnet}" "${subnet}" "${subnet_gateway}"
TUN=tun0

# Synchronize code execution with a file lock.
function lockwrap() {
    (
    flock 200
    "$@"
    ) 200>${lock_file}
}

function setup() {
    # clear config file
    rm -f /etc/openshift-sdn/config.env

    ## docker
    if [[ -z "${DOCKER_NETWORK_OPTIONS}" ]]
    then
        DOCKER_NETWORK_OPTIONS='-b=lbr0 --mtu=1450'
    fi

    mkdir -p /run/openshift-sdn
    cat <<EOF > /run/openshift-sdn/docker-network
# This file has been modified by openshift-sdn. Please modify the
# DOCKER_NETWORK_OPTIONS variable in /etc/sysconfig/openshift-node if this
# is an integrated install or /etc/sysconfig/openshift-sdn-node if this is a
# standalone install.

DOCKER_NETWORK_OPTIONS='${DOCKER_NETWORK_OPTIONS}'
EOF

    systemctl daemon-reload
    systemctl restart docker.service

    # disable iptables for lbr0
    # for kernel version 3.18+, module br_netfilter needs to be loaded upfront
    # for older ones, br_netfilter may not exist, but is covered by bridge (bridge-utils)
    modprobe br_netfilter || true 
    sysctl -w net.bridge.bridge-nf-call-iptables=0

    mkdir -p /etc/openshift-sdn
    echo "export OPENSHIFT_SDN_TAP1_ADDR=${tun_gateway}" >& "/etc/openshift-sdn/config.env"
    echo "export OPENSHIFT_CLUSTER_SUBNET=${cluster_subnet}" >> "/etc/openshift-sdn/config.env"
}

set -e

lockwrap setup
