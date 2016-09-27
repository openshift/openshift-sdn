#!/bin/bash
#set -o errexit
set -o nounset
set -o pipefail
#set -x

# This scripts finds the ipf deployments in the cluster and
# analyzes the configuration

# get all of nodes (some subset may be in use by ipf delpoyments)
nodes=$(oc get nodes | gawk '{ print $1 }' | grep -v NAME)

# find the ipf deployments (they have OPENSHIFT_HA_VRRP_ID_OFFSET env)
dcs=$(oc get dc | grep -v NAME | gawk '{ print $1 }')
for dc in ${dcs}; do
    is_ipf=$(oc get dc ${dc} -o yaml | grep OPENSHIFT_HA_VRRP_ID_OFFSET)
    if [[ $? -ne 0 ]]; then
        continue
    fi
    # get the selectors
    selector=$(oc get dc ${dc} --template={{.spec.selector}} | sed 's/map\[//
s/\]//')
    # find nodes with the selector
    for node in ${nodes}; do
        oc get nodes ${node} --template={{.metadata.labels}} | grep "${selector}" > /dev/null
        if [[ $? -eq 0 ]]; then
            node_set=$(echo "${node_set} ${node}")
        fi
    done

    # get the VIPs from the dc
    next_env=0
    vips=""
    envmap=$(oc get dc  ${dc} --template={{.spec.template.spec.containers}} | grep OPENSHIFT_HA_VIRTUAL_IPS)
    for env in ${envmap}; do
        if [[ ${next_env} -eq 1 ]]; then
            vip=$(echo "${env}" | sed 's/value://
s/\]//')
            vip_range=$(echo "${vip}" | gawk -F '.' '{ print $4 }' | gawk -F '-' '{ print $1 " " $2 }')
            vip_base=$(echo "${vip}" | gawk -F '.' '{ print $1 "." $2 "." $3 "." }' )
            vip_route=$(echo "${vip}" | gawk -F '.' '{ print $1 "." $2 }' )
            for v in $(seq -s ' ' ${vip_range}); do
                vips=${vips}" "${vip_base}${v}
            done
            break
        fi
        echo "${env}" | grep 'OPENSHIFT_HA_VIRTUAL_IPS' > /dev/null
        if [[ $? -eq 0 ]]; then
            next_env=1
        fi
    done

    # Here is the set of things to test
    echo "dc: ${dc}"
    echo "nodes: ${node_set}"
    echo "vips: ${vips}"
    echo ""

    # ---- TESTS ---- #

    # --------- verify setup ---------- #
    for n in ${node_set}; do
        # --- get the iptables multicast rule ----
        echo "iptable rule on ${n} to allow 224.0.0.18/32"
        ssh ${n} iptables-save | grep 224
        # -- verify keepalived is running on each node
        output=$(ssh ${n} ps ax | grep "keepalived" | wc -l)
        case ${output} in
            0)
            echo "Could not ssh to ${n}"
            ;;
            1)
            echo "keepalived is not running on node ${n}"
            ;;
            *)
            echo "keepalived is running on node ${n}"
        esac
    done

    # Verify route to nodes
    echo ""
    echo "route to VIP"
    hostname=$(hostname)
    hostip=$(host ${hostname} | gawk '{ print $4 }')
    hostdev=$(ip route | gawk '/${hostip}/ { print $3 }')
    echo "from ${hostip}"
    ip route | grep ${vip_route}
    if [[ $? -ne 1 ]]; then
        echo "need route to nodes"
    fi
    for n in ${node_set}; do
        echo "from ${n}"
        ssh ${n} ip route | grep ${vip_route}
        if [[ $? -ne 0 ]]; then
            echo "need route to nodes"
        fi
    done

    # find which VIP is served by which node
    echo ""
    echo "Report which node is serving which VIP"
    for n in ${node_set}; do
        echo "from ${n}"
        for v in ${vips}; do
            ssh ${n} ip a | grep ${v}
        done
    done


    # Test each node in the set with ping to all nodes in the set
    echo ""
    echo "TEST each node in set can ping all other nodes in set"
    for n1 in ${node_set}; do
        for n2 in ${node_set}; do
            ssh ${n1} ping -c1 -w1 ${n2} | grep "0 received"
            if [ $? -eq 0 ]; then
                echo "${n1} ping ${n2}   --- FAILED"
            else
                echo "${n1} ping ${n2}   --- PASSED"
            fi
        done
    done

    # Test each node in the set ping all VIPs
    echo ""
    echo "TEST that each node in set can ping all VIPs"
    # first ping from the host that is running the script
    for n2 in ${vips}; do
        ping -c1 -w1 ${n2} | grep "0 received"
        if [[ $? -eq 0 ]]; then
            echo "${hostip} ping ${n2}   --- FAILED"
        else
            echo "${hostip} ping ${n2}   --- PASSED"
        fi
    done
    # Next ping from each node in the set to all other nodes in the set
    for n1 in ${node_set}; do
        for n2 in ${vips}; do
            ssh ${n1} ping -c1 -w1 ${n2} | grep "0 received"
            if [[ $? -eq 0 ]]; then
                echo "${n1} ping ${n2}   --- FAILED"
            else
                echo "${n1} ping ${n2}   --- PASSED"
            fi
        done
    done

    # get the logs from the ifp pods
    echo ""
    echo "Get the log from each pod ipf pod"
    pods=$(oc get po | grep ${dc} | gawk '{ print $1 }')
    echo "${pods}"
    for pod in ${pods}; do
        nodeName=$(oc get po ${pod} --template={{.spec.nodeName}})
        echo "pod: ${pod}    node: ${nodeName}"
        oc logs ${pod} | grep VRRP_Inst\\\|Netli | grep -v :
    done

done


exit 0
