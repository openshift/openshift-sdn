#!/bin/bash

set -ex

subnet_gateway=$1
subnet=$2
container_network=$3
subnet_mask_len=$4
printf 'Container network is "%s"; local host has subnet "%s" and gateway "%s".\n' "${container_network}" "${subnet}" "${subnet_gateway}"
TUN=tun0

## openvswitch
ovs-vsctl del-br br0 || true
ovs-vsctl add-br br0 -- set Bridge br0 fail-mode=secure
ovs-vsctl set bridge br0 protocols=OpenFlow13
ovs-vsctl del-port br0 vxlan0 || true
ovs-vsctl add-port br0 vxlan0 -- set Interface vxlan0 type=vxlan options:remote_ip="flow" options:key="flow" ofport_request=10
ovs-vsctl add-port br0 ${TUN} -- set Interface ${TUN} type=internal ofport_request=2

ip addr add ${subnet_gateway}/${subnet_mask_len} dev ${TUN}
ip link set ${TUN} up
ip route del ${subnet} dev ${TUN} proto kernel scope link src ${subnet_gateway} || true
ip route del ${container_network} dev ${TUN} proto kernel scope link src ${subnet_gateway}

## iptables
iptables -t nat -D POSTROUTING -s ${container_network} ! -d ${container_network} -j MASQUERADE || true
iptables -t nat -A POSTROUTING -s ${container_network} ! -d ${container_network} -j MASQUERADE
iptables -D INPUT -p udp -m multiport --dports 4789 -m comment --comment "001 vxlan incoming" -j ACCEPT || true
iptables -D INPUT -i ${TUN} -m comment --comment "traffic from docker for internet" -j ACCEPT || true
lineno=$(iptables -nvL INPUT --line-numbers | grep "state RELATED,ESTABLISHED" | awk '{print $1}')
iptables -I INPUT $lineno -p udp -m multiport --dports 4789 -m comment --comment "001 vxlan incoming" -j ACCEPT
iptables -I INPUT $((lineno+1)) -i ${TUN} -m comment --comment "traffic from docker for internet" -j ACCEPT
