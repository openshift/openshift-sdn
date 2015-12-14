#!/bin/bash

set -ex

lock_file=/var/lock/openshift-sdn.lock
mtu=$1

# Synchronize code execution with a file lock.
function lockwrap() {
    (
    flock 200
    "$@"
    ) 200>${lock_file}
}

function docker_network_config() {
    if [ -z "${DOCKER_NETWORK_OPTIONS}" ]; then
	DOCKER_NETWORK_OPTIONS="-b=lbr0 --mtu=${mtu}"
    fi

    local conf=/run/openshift-sdn/docker-network
    case "$1" in
	check)
	    if ! grep -q -s "DOCKER_NETWORK_OPTIONS='${DOCKER_NETWORK_OPTIONS}'" $conf; then
		return 1
	    fi
	    return 0
	    ;;

	update)
		mkdir -p $(dirname $conf)
		cat <<EOF > $conf
# This file has been modified by openshift-sdn.

DOCKER_NETWORK_OPTIONS='${DOCKER_NETWORK_OPTIONS}'
EOF

		systemctl daemon-reload
		systemctl restart docker.service
	    ;;
    esac
}

function setup_required() {
    if ! docker_network_config check; then
        return 0
    fi
    return 1
}

function setup() {
    ## docker
    docker_network_config update
}

set +e
if ! setup_required; then
    echo "SDN setup not required."
    exit 140
fi
set -e

lockwrap setup
