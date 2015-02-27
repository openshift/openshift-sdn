## SDN solution for OpenShift

openshift-sdn configures an overlay network for a Docker cluster, using Open
vSwitch (OVS). This is still a work in progress. Do not use it in production.

The openshift-sdn daemon runs in one of two modes: either master or minion.

If launched without the -minion flag, openshift-sdn operates as a master.  In
this mode, openshift-sdn watches its registry, stored in etcd.  When a minion is
added to this registry, openshift-sdn will allocate an unused subnet from the
container network (hardcoded for now to 10.1.0.0/16) and store this subnet in
the registry.  When a minion is deleted from the registry, openshift-sdn will
delete the registry entry for the subnet and consider the subnet available to be
allocated again.

If launched with the -minion flag, openshift-sdn will first register the local
host as a minion in the aforementioned registry so that an openshift-sdn master
will allocate a subnet to the minion.  Next, openshift-sdn will configure OVS,
Docker, and the network stack on the local host with a VxLAN through which the
containers on the local host can access the containers of all other
participating minions on the container network (10.1.0.0/16).  Finally,
openshift-sdn will monitor the registry to add and remove OpenFlow rules to the
local host's VxLAN configuration as the openshift-sdn master adds and deletes
subnets.

Note that openshift-sdn in master mode does not configure the local (master)
host to have access to the container network; if it is desirable for
openshift-sdn to do so (for example, if the same host is both a master and a
minion), then it is necessary to run two instances of openshift-sdn on the
host: one instance in master mode and one in minion mode.

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

##### Manually add minions to a master

Steps to create manually create an OpenShift cluster with openshift-sdn. This requires that each machine (master, minions) have compiled `openshift` and `openshift-sdn` already. Check [here](https://github.com/openshift/origin) for OpenShift instructions. Ensure 'openvswitch' is installed and running (`yum install -y openvswitch && systemctl enable openvswitch && systemctl start openvswitch`). Also verify that the `DOCKER_OPTIONS` variable is unset in your environment, or set to a known-working value (e.g. `DOCKER_OPTIONS='-b=lbr0 --mtu=1450 --selinux-enabled'`). If you don't know what to put there, it's probably best to leave it unset. :)

On OpenShift master,

	$ openshift start master [--nodes=node1]  # start the master openshift server (also starts the etcd server by default) with an optional list of nodes
	$ openshift-sdn           # assumes etcd is running at localhost:4001

To add a node to the cluster, do the following on the node:

	$ openshift-sdn -etcd-endpoints=http://openshift-master:4001 -minion -public-ip=<10.10....> -hostname <hostname>
	where, 
		-etcd-endpoints	: reach the etcd db here
		-minion 	: run it in minion mode (will watch etcd servers for new minion subnets)
		-public-ip	: use this field for suggesting the publicly reachable IP address of this minion
		-hostname	: the name that will be used to register the minion with openshift-master
	$ openshift start node --master=https://openshift-master:8443

Back on the master, to finally register the node:

	Create a json file for the new minion resource
        $ cat <<EOF > mininon-1.json
	{
		"kind":"Minion", 
		"id":"openshift-minion-1",
	 	"apiVersion":"v1beta1"
	}
	EOF
	where, openshift-minion-1 is a hostname that is resolvable from the master (or, create an entry in /etc/hosts and point it to the public-ip of the minion).
	$ openshift cli create -f minion-1.json

Done. Repeat last two pieces to add more nodes. Create new pods from the master (or just docker containers on the minions), and see that the pods are indeed reachable from each other. 


##### Detailed operation

To elaborate on the network configuration that openshift-sdn performs in minion
mode, it makes use of five network devices:

 - br0, an OVS bridge device;
 - lbr0, a Linux bridge device;
 - vlinuxbr and vovsbr, two Linux peer virtual Ethernet interfaces; and
 - vxlan0, the OVS VxLAN device that provides access to remote minions.

On initialization in minion mode, openshift-sdn creates lbr0 and configures
Docker to use lbr0 as the bridge for containers; creates br0 in OVS; creates
the vlinuxbr and vovsbr peer interfaces, which provide a point-to-point
connection for the purpose of moving packets between the regular Linux
networking stack and OVS; adds vlinuxbr to lbr0 and vovsbr to br0 (on port 9);
and adds vxlan0 to br0 (on port 10).

As openshift-sdn sees subnets added to and deleted from the registry by the
master, it adds and deletes OpenFlow rules on br0 to route packets
appropriately: packets with a destination IP address on the local minion's
subnet go to vovsbr (port 9 on br0) and thus to vlinuxbr, the local bridge, and
ultimately the local container; whereas packets with a destination IP address
on a remote minion's subnet go to vxlan0 (port 10 on br0) and thus out onto the
network.

To illustrate, suppose we have two containers A and B where the peer virtual
Ethernet device for container A's eth0 is named vethA and the peer for container
B's eth0 is named vethB.  (If Docker's use of peer virtual Ethernet devices is
not already familiar to you, see https://docs.docker.com/articles/networking/
for details on networking in Docker.)

Now suppose first that container A is on the local host and container B is also
on the local host.  Then the flow of packets from container A to container B is
as follows:

eth0 (in A's netns) -> vethA -> lbr0 -> vlinuxbr -> vovsbr -> br0 -> vovsbr ->
vlinuxbr -> lbr0 -> vethB -> eth0 (in B's netns).

Next suppose instead that container A is on the local host and container B is on
a remote host on the same container network.  Then the flow of packets from
container A to container B is as follows:

eth0 (in A's netns) -> vethA -> lbr0 -> vlinuxbr -> vovsbr -> br0 -> vxlan0 ->
network\* -> vxlan0 -> br0 -> vovsbr -> vlinuxbr -> lbr0 -> vethB -> eth0 (in
B's netns).

\* After this point, device names refer to devices on container B's host.

##### OpenShift? PaaS? Can I have a 'plain setup' just for Docker?

Someone needs to register that new nodes have joined the cluster. And instead of using OpenShift/Kubernetes to do that, we can use 'openshift-sdn' itself. Use '-sync' flag for that.

Steps:

1. Run etcd somewhere, and run the openshift-sdn master to watch it in sync mode. 

		$ systemctl start etcd
		$ openshift-sdn -master -sync  # use -etcd-endpoints=http://target:4001 if etcd is not running locally

2. To add a node, make sure the 'hostname/dns' is reachable from the machine that is running 'openshift-sdn master'. Then start the openshift-sdn in minion mode with sync flag.

		$ openshift-sdn -minion -sync -etcd-endpoints=http://master-host:4001 -hostname=minion-1-dns -public-ip=<public ip that the hostname resolves to>

Done. Add more nodes by repeating step 2. All nodes should have a docker bridge (lbr0) that is part of the overlay network.

#### Gotchas..

Some requirements, some silly errors.

 - openshift-sdn fails with errors around ovs-vsctl.. 
	yum -y install openvswitch && systemctl enable openvswitch && systemctl start openvswitch
 - openshift-sdn fails to start with errors relating to 'network not up' etc.
	systemctl stop NetworkManager # that fella is nosy, does not like mint new bridges
 - openshift-sdn fails to start saying cannot reach etcd endpoints
	etcd not running really or not listening on public interface? That machine not reachable possibly? -etcd-endpoints=https?? without ssl being supplied? Remove the trailing '/' from the url maybe?
 - openshift-sdn is up, I think I got the subnet, but my pings do not work
	It may take a while for the ping to work (blame the docker linux bridge, optimizations coming). Check that all nodes' hostnames on master are resolvable and to the correct IP addresses. Last, but not the least - firewalld (switch it off and check, and then punch a hole for vxlan please).

#### Performance Note

The current design has a long path for packets directed for the overlay network.
There are two veth-pairs, a linux bridge, and then the OpenVSwitch, that cause a drop in performance of about 40%

Hand-crafted solutions that eliminate the long-path to just a single veth-pair bring the performance close to the wire. The performance has been measured using sockperf.

  | openshift-sdn | openshift-sdn (optimized) | without overlay
--- | --------- | ------- | ------
Latency | 112us | 84us | 82us

#### TODO

 - Add more options, so that users can choose the subnet to give to the cluster. The default is hardcoded today to "10.1.0.0/16"
 - Performance enhancements, as discussed above
 - Usability without depending on openshift

