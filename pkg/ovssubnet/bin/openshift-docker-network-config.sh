#!/bin/bash

set -ex

mtu=$1

DOCKER_NETWORK_OPTIONS="-b=lbr0 --mtu=${mtu}"

if [ -f /.dockerinit ]; then
    # Assume supervisord-managed docker for docker-in-docker deployments
    conf=/etc/supervisord.conf
    if [ ! -f $conf ]; then
	echo "Running in docker but /etc/supervisord.conf not found." >&2
	exit 1
    fi

    if grep -q -s "DOCKER_DAEMON_ARGS=\"${DOCKER_NETWORK_OPTIONS}\"" $conf; then
	exit 0
    fi
    echo "Docker networking options have changed; manual restart required." >&2
    sed -i.bak -e \
	"s+\(DOCKER_DAEMON_ARGS=\)\"\"+\1\"${DOCKER_NETWORK_OPTIONS}\"+" \
	$conf

else

    # Otherwise assume systemd-managed docker
    conf=/run/openshift-sdn/docker-network
    if grep -q -s "DOCKER_NETWORK_OPTIONS='${DOCKER_NETWORK_OPTIONS}'" $conf; then
	exit 0
    fi

    mkdir -p /run/openshift-sdn
    cat <<EOF > /run/openshift-sdn/docker-network
# This file has been modified by openshift-sdn.

DOCKER_NETWORK_OPTIONS='${DOCKER_NETWORK_OPTIONS}'
EOF

    systemctl daemon-reload
    systemctl restart docker.service

    # disable iptables for lbr0
    # for kernel version 3.18+, module br_netfilter needs to be loaded upfront
    # for older ones, br_netfilter may not exist, but is covered by bridge (bridge-utils)
    #
    # This operation is assumed to have been performed in advance
    # for docker-in-docker deployments.
    modprobe br_netfilter || true
    sysctl -w net.bridge.bridge-nf-call-iptables=0
fi
