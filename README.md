## SDN solutions for Openshift

Software to get an overlay network up and running for a Kubernetes cluster.

#### Build and Install

	$ git clone https://github.com/openshift/openshift-sdn
	$ cd openshift-sdn
	$ make clean        # optional
	$ make              # build
	$ make install      # installs in /usr/bin

#### Try it out

##### Use vagrant, pre-define a cluster, and bring it up

Create an openshift cluster on your desktop using vagrant:

	$ git clone https://github.com/openshift/origin
	$ cd origin
	$ make clean
	$ export OPENSHIFT_DEV_CLUSTER=1
	$ export OPENSHIFT_NUM_MINIONS=2
	$ export OPENSHIFT_SDN=ovs-simple
	$ vagrant up

##### Manually add nodes to a master

Steps to create manually create an OpenShift cluster with openshift-sdn. This requires that each machine (master, nodes) have compiled `openshift` and `openshift-sdn` already. Check [here](https://github.com/openshift/origin) for OpenShift instructions. Ensure 'openvswitch' is installed and running (`yum install -y openvswitch && systemctl enable openvswitch && systemctl start openvswitch`). Also verify that the `DOCKER_OPTIONS` variable is unset in your environment, or set to a known-working value (e.g. `DOCKER_OPTIONS='-b=lbr0 --mtu=1450 --selinux-enabled'`). If you don't know what to put there, it's probably best to leave it unset. :)

On OpenShift master,

	$ openshift start master [--nodes=node1]  # start the master openshift server (also starts the etcd server by default) with an optional list of nodes
	$ openshift-sdn           # assumes etcd is running at localhost:4001

To add a node to the cluster, do the following on the node:

	$ openshift-sdn -etcd-endpoints=http://openshift-master:4001 -node -public-ip=<10.10....> -hostname <hostname>
	where, 
		-etcd-endpoints	: reach the etcd db here
		-node 	        : run it in node mode (will watch etcd servers for new node subnets)
		-public-ip	: use this field for suggesting the publicly reachable IP address of this node
		-hostname	: the name that will be used to register the node with openshift-master
	$ openshift start node --master=https://openshift-master:8443

Back on the master, to finally register the node:

	Create a json file for the new node resource
        $ cat <<EOF > node-1.json
	{
		"kind":"Node",
		"id":"openshift-minion-1",
		"apiVersion":"v1"
	}
	EOF
	where, openshift-minion-1 is a hostname that is resolvable from the master (or, create an entry in /etc/hosts and point it to the public-ip of the node).
	$ openshift cli create -f node-1.json

Done. Repeat last two pieces to add more nodes. Create new pods from the master (or just docker containers on the nodes), and see that the pods are indeed reachable from each other.


#### Gotchas..

Some requirements, some silly errors.

 - openshift-sdn fails with errors around ovs-vsctl.. 
	yum -y install openvswitch && systemctl enable openvswitch && systemctl start openvswitch
 - openshift-sdn fails to start saying cannot reach etcd endpoints
	etcd not running really or not listening on public interface? That machine not reachable possibly? -etcd-endpoints=https?? without ssl being supplied? Remove the trailing '/' from the url maybe?
 - openshift-sdn is up, I think I got the subnet, but my pings do not work
	It may take a while for the ping to work (blame the docker linux bridge, optimizations coming). Check that all nodes' hostnames on master are resolvable and to the correct IP addresses. Last, but not the least - firewalld (switch it off and check, and then punch a hole for vxlan please).
