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
    systemctl daemon-reload
    systemctl restart docker.service

    # disable iptables for lbr0
    # for kernel version 3.18+, module br_netfilter needs to be loaded upfront
    # for older ones, br_netfilter may not exist, but is covered by bridge (bridge-utils)
    modprobe br_netfilter || true 
    sysctl -w net.bridge.bridge-nf-call-iptables=0
}

set -e

lockwrap setup
