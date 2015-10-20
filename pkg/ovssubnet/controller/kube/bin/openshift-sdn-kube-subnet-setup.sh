#!/bin/bash

set -ex

local_subnet_gateway=$1
local_subnet_cidr=$2
local_subnet_mask_len=$3
cluster_network_cidr=$4
service_network_cidr=$5
mtu=$6
printf 'Container network is "%s"; local host has subnet "%s", mtu "%d" and gateway "%s".\n' "${cluster_network_cidr}" "${local_subnet_cidr}" "${mtu}" "${local_subnet_gateway}"
TUN=tun0

function setup() {
    # clear config file
    rm -f /etc/openshift-sdn/config.env

    ## openvswitch
    ovs-vsctl del-br br0 || true
    ovs-vsctl add-br br0 -- set Bridge br0 fail-mode=secure
    ovs-vsctl set bridge br0 protocols=OpenFlow13
    ovs-vsctl del-port br0 vxlan0 || true
    ovs-vsctl add-port br0 vxlan0 -- set Interface vxlan0 type=vxlan options:remote_ip="flow" options:key="flow" ofport_request=1
    ovs-vsctl add-port br0 ${TUN} -- set Interface ${TUN} type=internal ofport_request=2

    ip link del vlinuxbr || true
    ip link add vlinuxbr type veth peer name vovsbr
    ip link set vlinuxbr up
    ip link set vovsbr up
    ip link set vlinuxbr txqueuelen 0
    ip link set vovsbr txqueuelen 0

    ovs-vsctl del-port br0 vovsbr || true
    ovs-vsctl add-port br0 vovsbr -- set Interface vovsbr ofport_request=9

    ## linux bridge
    ip link set lbr0 down || true
    brctl delbr lbr0 || true
    brctl addbr lbr0
    ip addr add ${local_subnet_gateway}/${local_subnet_mask_len} dev lbr0
    ip link set lbr0 up
    brctl addif lbr0 vlinuxbr

    # setup tun address
    ip addr add ${local_subnet_gateway}/${local_subnet_mask_len} dev ${TUN}
    ip link set ${TUN} up
    ip route add ${cluster_network_cidr} dev ${TUN} proto kernel scope link

    # Cleanup docker0 since docker won't do it
    ip link set docker0 down || true
    brctl delbr docker0 || true

    # enable IP forwarding for ipv4 packets
    sysctl -w net.ipv4.ip_forward=1
    sysctl -w net.ipv4.conf.${TUN}.forwarding=1

    # delete the subnet routing entry created because of lbr0
    ip route del ${local_subnet_cidr} dev lbr0 proto kernel scope link src ${local_subnet_gateway} || true

    mkdir -p /etc/openshift-sdn
    echo "export OPENSHIFT_CLUSTER_SUBNET=${cluster_network_cidr}" >> "/etc/openshift-sdn/config.env"
}

setup
