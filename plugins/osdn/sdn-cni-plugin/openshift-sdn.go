package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/openshift/openshift-sdn/plugins/osdn/api"

	oclient "github.com/openshift/origin/pkg/client"
	configapi "github.com/openshift/origin/pkg/cmd/server/api"

	kclient "k8s.io/kubernetes/pkg/client/unversioned"
	knetwork "k8s.io/kubernetes/pkg/kubelet/network"
	kbandwidth "k8s.io/kubernetes/pkg/util/bandwidth"

	"github.com/containernetworking/cni/pkg/ip"
	"github.com/containernetworking/cni/pkg/ipam"
	"github.com/containernetworking/cni/pkg/ns"
	"github.com/containernetworking/cni/pkg/skel"

	"github.com/vishvananda/netlink"
)

const (
	sdnScript   = "openshift-sdn-ovs"
	setUpCmd    = "setup"
	tearDownCmd = "teardown"
	statusCmd   = "status"
	updateCmd   = "update"

	AssignMacVlanAnnotation string = "pod.network.openshift.io/assign-macvlan"

	interfaceName      = knetwork.DefaultInterfaceName
	ipamConfigTemplate = `{
  "cniVersion": "0.1.0",
  "name": "openshift-sdn",
  "type": "openshift-sdn",
  "ipam": {
    "type": "host-local",
    "subnet": "%s",
    "routes": [
      { "dst": "0.0.0.0/0", "gw": "%s" },
      { "dst": "%s" }
    ]
  }
}`
)

func getNodeConfig() (*api.CNINodeConfig, error) {
	// Try few times up to 15 seconds
	retries := 150
	retryInterval := 100 * time.Millisecond
	for i := 0; i < retries; i++ {
		network := &api.CNINodeConfig{}
		conf, err := ioutil.ReadFile(api.NodeConfigPath)
		if err == nil {
			if err := json.Unmarshal(conf, network); err == nil {
				return network, nil
			}
		}
		time.Sleep(retryInterval)
	}

	return nil, fmt.Errorf("Timed out waiting for openshift to be ready")
}

type podInfo struct {
	Vnid        uint
	Privileged  bool
	Annotations map[string]string
}

func readPodInfo(originClient *oclient.Client, kubeClient *kclient.Client, cniArgs string, multitenant bool) (*podInfo, error) {
	var namespace, name string
	for _, arg := range strings.Split(cniArgs, ";") {
		parts := strings.Split(arg, "=")
		if len(parts) != 2 {
			return nil, fmt.Errorf("Invalid CNI_ARG '%s'", arg)
		}
		switch parts[0] {
		case "K8S_POD_NAMESPACE":
			namespace = strings.TrimSpace(parts[1])
		case "K8S_POD_NAME":
			name = strings.TrimSpace(parts[1])
		}
	}

	if namespace == "" || name == "" {
		return nil, fmt.Errorf("Missing pod namespace or name")
	}

	info := &podInfo{}

	if multitenant {
		netNamespace, err := originClient.NetNamespaces().Get(namespace)
		if err != nil {
			return nil, err
		}
		info.Vnid = netNamespace.NetID
	}

	// FIXME: does this ensure the returned pod lives on this node?
	pod, err := kubeClient.Pods(namespace).Get(name)
	if err != nil {
		return nil, fmt.Errorf("Failed to read pod %s/%s: %v", err)
	}

	for _, container := range pod.Spec.Containers {
		if container.SecurityContext.Privileged != nil && *container.SecurityContext.Privileged {
			info.Privileged = true
			break
		}
	}

	info.Annotations = pod.Annotations
	return info, nil
}

func getBandwidth(pi *podInfo) (string, string, error) {
	ingress, egress, err := kbandwidth.ExtractPodBandwidthResources(pi.Annotations)
	if err != nil {
		return "", "", fmt.Errorf("failed to parse pod bandwidth: %v", err)
	}
	var ingressStr, egressStr string
	if ingress != nil {
		ingressStr = fmt.Sprintf("%d", ingress.Value())
	}
	if egress != nil {
		egressStr = fmt.Sprintf("%d", egress.Value())
	}
	return ingressStr, egressStr, nil
}

func wantsMacvlan(pi *podInfo) (bool, error) {
	val, found := pi.Annotations[AssignMacVlanAnnotation]
	if !found || val != "true" {
		return false, nil
	}
	if pi.Privileged {
		return true, nil
	}
	return false, fmt.Errorf("Pod has %q annotation but is not privileged", AssignMacVlanAnnotation)
}

func getPodInfo(args *skel.CmdArgs) (*api.CNINodeConfig, string, string, string, bool, error) {
	n, err := loadNetConf(args.StdinData)
	if err != nil {
		return nil, "", "", "", false, err
	}

	originClient, _, err := configapi.GetOpenShiftClient(n.MasterKubeConfig)
	if err != nil {
		return nil, "", "", "", false, err
	}
	kubeClient, _, err := configapi.GetKubeClient(n.MasterKubeConfig)
	if err != nil {
		return nil, "", "", "", false, err
	}

	nodeConfig, err := getNodeConfig()
	if err != nil {
		return nil, "", "", "", false, err
	}

	podInfo, err := readPodInfo(originClient, kubeClient, args.Args, n.Multitenant)
	if err != nil {
		return nil, "", "", "", false, err
	}

	vnid := strconv.FormatUint(uint64(podInfo.Vnid), 10)

	ingress, egress, err := getBandwidth(podInfo)
	if err != nil {
		return nil, "", "", "", false, err
	}

	macvlan, err := wantsMacvlan(podInfo)
	if err != nil {
		return nil, "", "", "", false, err
	}

	return nodeConfig, vnid, ingress, egress, macvlan, nil
}

// Returns host veth name, container veth MAC, and pod IP
func getVethInfo(netns, containerIfname string) (netlink.Link, string, string, error) {
	var (
		peerIfindex int
		contVeth    netlink.Link
		err         error
		podIP       string
	)

	containerNs, err := ns.GetNS(netns)
	if err != nil {
		return nil, "", "", fmt.Errorf("Failed to get container netns: %v", err)
	}
	defer containerNs.Close()

	err = containerNs.Do(func(ns.NetNS) error {
		contVeth, err = netlink.LinkByName(containerIfname)
		if err != nil {
			return err
		}
		peerIfindex = contVeth.Attrs().ParentIndex

		addrs, err := netlink.AddrList(contVeth, syscall.AF_INET)
		if err != nil {
			return fmt.Errorf("failed to get container IP addresses: %v", err)
		}
		if len(addrs) == 0 {
			return fmt.Errorf("container had no addresses")
		}
		podIP = addrs[0].IP.String()

		return nil
	})
	if err != nil {
		return nil, "", "", fmt.Errorf("Failed to inspect container interface: %v", err)
	}

	hostVeth, err := netlink.LinkByIndex(peerIfindex)
	if err != nil {
		return nil, "", "", fmt.Errorf("Failed to get host veth: %v", err)
	}

	return hostVeth, contVeth.Attrs().HardwareAddr.String(), podIP, nil
}

func addMacvlan(netns string) error {
	var defIface netlink.Link
	var err error

	// Find interface with the default route
	routes, _ := netlink.RouteList(nil, netlink.FAMILY_V4)
	for _, r := range routes {
		if r.Dst == nil {
			defIface, err = netlink.LinkByIndex(r.LinkIndex)
			if err != nil {
				return fmt.Errorf("Failed to get default route interface: %v", err)
			}
		}
	}
	if defIface == nil {
		return fmt.Errorf("Failed to find default route interface")
	}

	containerNs, err := ns.GetNS(netns)
	if err != nil {
		return fmt.Errorf("Failed to get container netns: %v", err)
	}
	defer containerNs.Close()

	return containerNs.Do(func(ns.NetNS) error {
		err := netlink.LinkAdd(&netlink.Macvlan{
			LinkAttrs: netlink.LinkAttrs{
				MTU:         defIface.Attrs().MTU,
				Name:        "macvlan0",
				ParentIndex: defIface.Attrs().Index,
			},
			Mode: netlink.MACVLAN_MODE_PRIVATE,
		})
		if err != nil {
			return fmt.Errorf("failed to create macvlan interface: %v", err)
		}
		l, err := netlink.LinkByName("macvlan0")
		if err != nil {
			return fmt.Errorf("failed to create find macvlan interface: %v", err)
		}
		err = netlink.LinkSetUp(l)
		if err != nil {
			return fmt.Errorf("failed to set macvlan interface up: %v", err)
		}
		return nil
	})
}

func getIPAMConfig(nodeConfig *api.CNINodeConfig) []byte {
	return []byte(fmt.Sprintf(ipamConfigTemplate, nodeConfig.NodeNetwork, nodeConfig.NodeGateway, nodeConfig.ClusterNetwork))
}

func isScriptError(err error) bool {
	_, ok := err.(*exec.ExitError)
	return ok
}

// Get the last command (which is prefixed with "+" because of "set -x") and its output
func getScriptError(output []byte) string {
	lines := strings.Split(string(output), "\n")
	for n := len(lines) - 1; n >= 0; n-- {
		if strings.HasPrefix(lines[n], "+") {
			return strings.Join(lines[n:], "\n")
		}
	}
	return string(output)
}

func loadNetConf(bytes []byte) (*api.CNINetConfig, error) {
	n := &api.CNINetConfig{}
	if err := json.Unmarshal(bytes, n); err != nil {
		return nil, fmt.Errorf("failed to load netconf: %v", err)
	}
	return n, nil
}

func cmdAdd(args *skel.CmdArgs) error {
	nodeConfig, vnid, ingress, egress, macvlan, err := getPodInfo(args)
	if err != nil {
		return err
	}

	// Run IPAM so we can set up container veth
	ipamConfig := getIPAMConfig(nodeConfig)
	os.Setenv("CNI_ARGS", "")
	result, err := ipam.ExecAdd("host-local", ipamConfig)
	if err != nil {
		return fmt.Errorf("Failed to run CNI IPAM ADD: %v", err)
	}
	if result.IP4 == nil || result.IP4.IP.IP.To4() == nil {
		return fmt.Errorf("Failed to obtain IP address from CNI IPAM")
	}

	var hostVeth, contVeth netlink.Link
	err = ns.WithNetNSPath(args.Netns, func(hostNS ns.NetNS) error {
		hostVeth, contVeth, err = ip.SetupVeth(interfaceName, int(nodeConfig.MTU), hostNS)
		if err != nil {
			return fmt.Errorf("Failed to create container veth: %v", err)
		}
		// refetch to get hardware address and other properties
		contVeth, err = netlink.LinkByIndex(contVeth.Attrs().Index)
		if err != nil {
			return fmt.Errorf("Failed to fetch container veth: %v", err)
		}

		// Clear out gateway to prevent ConfigureIface from adding the cluster
		// subnet via the gateway
		result.IP4.Gateway = nil
		if err = ipam.ConfigureIface(interfaceName, result); err != nil {
			return fmt.Errorf("Failed to configure container IPAM: %v", err)
		}

		lo, err := netlink.LinkByName("lo")
		if err == nil {
			err = netlink.LinkSetUp(lo)
		}
		if err != nil {
			return fmt.Errorf("Failed to configure container loopback: %v", err)
		}
		return nil
	})
	if err != nil {
		ipam.ExecDel("host-local", ipamConfig)
		return err
	}

	if macvlan {
		if err := addMacvlan(args.Netns); err != nil {
			ipam.ExecDel("host-local", ipamConfig)
			return err
		}
	}

	contVethMac := contVeth.Attrs().HardwareAddr.String()
	podIP := result.IP4.IP.String()
	out, err := exec.Command(sdnScript, setUpCmd, hostVeth.Attrs().Name, contVethMac, podIP, vnid, ingress, egress).CombinedOutput()
	if isScriptError(err) {
		ipam.ExecDel("host-local", ipamConfig)
		return fmt.Errorf("Error running network setup script:\nhostVethName %s, contVethMac %s, podIP %s, vnid %s, ingress %s, egress %s\n %s", hostVeth.Attrs().Name, contVethMac, podIP, vnid, ingress, egress, out)
	} else if err != nil {
		ipam.ExecDel("host-local", ipamConfig)
		return err
	}

	return result.Print()
}

func cmdUpdate(args *skel.CmdArgs) error {
	_, vnid, ingress, egress, _, err := getPodInfo(args)
	if err != nil {
		return err
	}

	hostVeth, contVethMac, podIP, err := getVethInfo(args.Netns, args.IfName)
	if err != nil {
		return err
	}

	out, err := exec.Command(sdnScript, updateCmd, hostVeth.Attrs().Name, contVethMac, podIP, vnid, ingress, egress).CombinedOutput()

	if isScriptError(err) {
		return fmt.Errorf("Error running network update script: %s", getScriptError(out))
	} else if err != nil {
		return err
	}

	return nil
}

func cmdDel(args *skel.CmdArgs) error {
	nodeConfig, _, _, _, _, err := getPodInfo(args)
	if err != nil {
		return err
	}

	hostVeth, contVethMac, podIP, err := getVethInfo(args.Netns, args.IfName)
	if err != nil {
		return err
	}

	// The script's teardown functionality doesn't need the VNID
	out, err := exec.Command(sdnScript, tearDownCmd, hostVeth.Attrs().Name, contVethMac, podIP, "-1").CombinedOutput()

	if isScriptError(err) {
		return fmt.Errorf("Error running network teardown script: %s", getScriptError(out))
	} else if err != nil {
		return err
	}

	// Run IPAM to release the IP address lease
	os.Setenv("CNI_ARGS", "")
	if err := ipam.ExecDel("host-local", getIPAMConfig(nodeConfig)); err != nil {
		return fmt.Errorf("Failed to run CNI IPAM DEL: %v", err)
	}

	return nil
}

func main() {
	addFunc := cmdAdd

	// CNI doesn't yet have an UPDATE command, so fake it
	cmd := os.Getenv("CNI_COMMAND")
	if cmd == "UPDATE" {
		addFunc = cmdUpdate
		os.Setenv("CNI_COMMAND", "ADD")
	}

	skel.PluginMain(addFunc, cmdDel)
}
