package osdn

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	log "github.com/golang/glog"

	"github.com/openshift/openshift-sdn/pkg/netutils"
	"github.com/openshift/openshift-sdn/plugins/osdn/api"
	"github.com/openshift/openshift-sdn/plugins/osdn/hostport"

	osclient "github.com/openshift/origin/pkg/client"
	osapi "github.com/openshift/origin/pkg/sdn/api"

	kapi "k8s.io/kubernetes/pkg/api"
	kclient "k8s.io/kubernetes/pkg/client/unversioned"
	kubeletTypes "k8s.io/kubernetes/pkg/kubelet/container"
	knetwork "k8s.io/kubernetes/pkg/kubelet/network"
	ktypes "k8s.io/kubernetes/pkg/types"
	kexec "k8s.io/kubernetes/pkg/util/exec"
	kubeutilnet "k8s.io/kubernetes/pkg/util/net"
	utilruntime "k8s.io/kubernetes/pkg/util/runtime"
	utilwait "k8s.io/kubernetes/pkg/util/wait"
	"k8s.io/kubernetes/pkg/watch"

	cniinvoke "github.com/appc/cni/pkg/invoke"
	cnitypes "github.com/containernetworking/cni/pkg/types"
)

type OsdnNode struct {
	multitenant        bool
	registry           *Registry
	localIP            string
	hostName           string
	podNetworkReady    chan struct{}
	podsByUID          map[ktypes.UID]*kapi.Pod
	vnids              vnidMap
	iptablesSyncPeriod time.Duration
	host               knetwork.Host
	masterKubeConfig   string
	mtu                uint
	hostportHandler    hostport.HostportHandler
}

// Called by higher layers to create the plugin SDN node instance
func NewNodePlugin(pluginName string, osClient *osclient.Client, kClient *kclient.Client, hostname string, selfIP string, iptablesSyncPeriod time.Duration, mtu uint, masterKubeConfig string) (*OsdnNode, error) {
	if !IsOpenShiftNetworkPlugin(pluginName) {
		return nil, nil
	}

	log.Infof("Initializing SDN node of type %q with configured hostname %q (IP %q), iptables sync period %q", pluginName, hostname, selfIP, iptablesSyncPeriod.String())
	if hostname == "" {
		output, err := kexec.New().Command("uname", "-n").CombinedOutput()
		if err != nil {
			return nil, err
		}
		hostname = strings.TrimSpace(string(output))
		log.Infof("Resolved hostname to %q", hostname)
	}
	if selfIP == "" {
		var err error
		selfIP, err = netutils.GetNodeIP(hostname)
		if err != nil {
			log.V(5).Infof("Failed to determine node address from hostname %s; using default interface (%v)", hostname, err)
			defaultIP, err := kubeutilnet.ChooseHostInterface()
			if err != nil {
				return nil, err
			}
			selfIP = defaultIP.String()
			log.Infof("Resolved IP address to %q", selfIP)
		}
	}

	if err := writeCNIConfig(masterKubeConfig, IsOpenShiftMultitenantNetworkPlugin(pluginName)); err != nil {
		return nil, err
	}

	plugin := &OsdnNode{
		multitenant:        IsOpenShiftMultitenantNetworkPlugin(pluginName),
		registry:           newRegistry(osClient, kClient),
		localIP:            selfIP,
		hostName:           hostname,
		vnids:              newVnidMap(),
		podNetworkReady:    make(chan struct{}),
		iptablesSyncPeriod: iptablesSyncPeriod,
		masterKubeConfig:   masterKubeConfig,
		mtu:                mtu,
		hostportHandler:    hostport.NewHostportHandler(),
		podsByUID:          make(map[ktypes.UID]*kapi.Pod),
	}
	return plugin, nil
}

func getCNIConfig(masterKubeConfig string, multitenant bool) ([]byte, error) {
	return json.Marshal(&api.CNINetConfig{
		NetConf: cnitypes.NetConf{
			Name: "openshift-sdn",
			Type: "openshift-sdn",
		},
		MasterKubeConfig: masterKubeConfig,
		Multitenant:      multitenant,
	})
}

const cniConfigPath string = "/etc/cni/net.d/80-openshift-sdn.conf"

func writeCNIConfig(masterKubeConfig string, multitenant bool) error {
	cniConfig, err := getCNIConfig(masterKubeConfig, multitenant)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(path.Dir(cniConfigPath), 0700); err != nil {
		return fmt.Errorf("failed to create CNI config directory: %v", err)
	}

	if err := ioutil.WriteFile(cniConfigPath, cniConfig, 0700); err != nil {
		return fmt.Errorf("failed to create CNI config file: %v", err)
	}

	return nil
}

func writeNodeConfig(ni *NetworkInfo, localSubnet *osapi.HostSubnet, mtu uint) error {
	_, ipnet, err := net.ParseCIDR(localSubnet.Subnet)
	nodeConfig, err := json.Marshal(&api.CNINodeConfig{
		ClusterNetwork: ni.ClusterNetwork.String(),
		NodeNetwork:    localSubnet.Subnet,
		NodeGateway:    netutils.GenerateDefaultGateway(ipnet).String(),
		MTU:            mtu,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal node config: %v", err)
	}

	dirName := path.Dir(api.NodeConfigPath)
	if err := os.RemoveAll(dirName); err != nil {
		return fmt.Errorf("failed to removing openshift-sdn run directory: %v", err)
	}
	if err := os.MkdirAll(dirName, 0700); err != nil {
		return fmt.Errorf("failed to create openshift-sdn run directory: %v", err)
	}

	if err := ioutil.WriteFile(api.NodeConfigPath, nodeConfig, 0400); err != nil {
		return fmt.Errorf("failed to create node config file: %v", err)
	}

	return nil
}

func (node *OsdnNode) Start() error {
	ni, err := node.registry.GetNetworkInfo()
	if err != nil {
		return fmt.Errorf("Failed to get network information: %v", err)
	}

	nodeIPTables := newNodeIPTables(ni.ClusterNetwork.String(), node.iptablesSyncPeriod)
	if err := nodeIPTables.Setup(); err != nil {
		return fmt.Errorf("Failed to set up iptables: %v", err)
	}

	networkChanged, localSubnet, err := node.SubnetStartNode(node.mtu)
	if err != nil {
		return err
	}

	if err := writeNodeConfig(ni, localSubnet, node.mtu); err != nil {
		return err
	}

	if node.multitenant {
		if err := node.VnidStartNode(); err != nil {
			return err
		}
	}

	if networkChanged {
		pods, err := node.GetLocalPods(kapi.NamespaceAll)
		if err != nil {
			return err
		}
		for _, p := range pods {
			containerID := getPodContainerID(&p)
			err = node.UpdatePod(p.Namespace, p.Name, kubeletTypes.ContainerID{ID: containerID})
			if err != nil {
				log.Warningf("Could not update pod %q (%s): %s", p.Name, containerID, err)
			}
		}
	}

	go utilwait.Forever(node.watchLocalPods, 0)

	node.markPodNetworkReady()

	return nil
}

func (node *OsdnNode) trackRunningPod(pod *kapi.Pod) bool {
	if pod.Status.PodIP == "" {
		return false
	}

log.V(2).Infof("########## tracking pod %#v", pod)
	podInfo, ok := node.podsByUID[pod.UID]
	if !ok || podInfo.UID != pod.UID {
		node.podsByUID[pod.UID] = pod
		return true
	}
	return false
}

func (node *OsdnNode) unTrackRunningPod(pod *kapi.Pod) {
	delete(node.podsByUID, pod.UID)
}

// Returns a list of pods running on this node and each pod's IP address.  Assumes
// PodSpecs retrieved from the runtime include the name and ID of containers in
// each pod.
func (node *OsdnNode) getRunningPods() ([]*hostport.RunningPod, error) {
	runningPods := make([]*hostport.RunningPod, 0)
	for _, pod := range node.podsByUID {
		podIP := net.ParseIP(pod.Status.PodIP)
		if podIP == nil {
			continue
		}
		runningPods = append(runningPods, &hostport.RunningPod{
			Pod: pod,
			IP:  podIP,
		})
	}
	return runningPods, nil
}

func (node *OsdnNode) watchLocalPods() {
	eventQueue := node.registry.RunLocalPodsEventQueue(node.hostName)

	for {
		eventType, obj, err := eventQueue.Pop()
		if err != nil {
			utilruntime.HandleError(fmt.Errorf("EventQueue failed for local pods: %v", err))
			return
		}
		pod := obj.(*kapi.Pod)

		log.V(5).Infof("Watch %s event for LocalPod %q", strings.Title(string(eventType)), pod.ObjectMeta.Name)
		switch eventType {
		case watch.Added, watch.Modified:
			if node.trackRunningPod(pod) {
				// Open any hostports the pod's containers want
				runningPods, err := node.getRunningPods()
				if err != nil {
					log.Warningf("Failed to gather running local pods: %v", err)
				}

				log.V(2).Infof("########## opening hostports for pod %s/%s", pod.UID, pod.Status.PodIP)
				newPod := &hostport.RunningPod{Pod: pod, IP: net.ParseIP(pod.Status.PodIP)}
				if err := node.hostportHandler.OpenPodHostportsAndSync(newPod, "tun0", runningPods); err != nil {
					log.Warningf("Failed to sync hostports: %v", err)
				}
			}
		case watch.Deleted:
			node.unTrackRunningPod(pod)
			runningPods, err := node.getRunningPods()
			if err != nil {
				log.Warningf("Failed to gather running local pods: %v", err)
				continue
			}
			if err := node.hostportHandler.SyncHostports("tun0", runningPods); err != nil {
				log.Warningf("Failed to sync hostports: %v", err)
			}
		}
	}
}

// FIXME: this should eventually go into kubelet via a CNI UPDATE/CHANGE action
// See https://github.com/containernetworking/cni/issues/89
func (node *OsdnNode) UpdatePod(namespace string, name string, id kubeletTypes.ContainerID) error {
	const pluginPath = "/opt/cni/bin/openshift-sdn"

	cniConfig, err := getCNIConfig(node.masterKubeConfig, node.multitenant)
	if err != nil {
		return err
	}

	args := &cniinvoke.Args{
		Command:     "UPDATE",
		ContainerID: id.String(),
		NetNS:       "/blahblah/foobar", // plugin finds out namespace itself
		PluginArgs: [][2]string{
			{"K8S_POD_NAMESPACE", namespace},
			{"K8S_POD_NAME", name},
			{"K8S_POD_INFRA_CONTAINER_ID", id.String()},
		},
		IfName: knetwork.DefaultInterfaceName,
		Path:   filepath.Dir(pluginPath),
	}

	if _, err := cniinvoke.ExecPluginWithResult(pluginPath, cniConfig, args); err != nil {
		return fmt.Errorf("failed to update pod network: %v", err)
	}

	return nil
}

func (node *OsdnNode) GetLocalPods(namespace string) ([]kapi.Pod, error) {
	return node.registry.GetRunningPods(node.hostName, namespace)
}

func (node *OsdnNode) markPodNetworkReady() {
	close(node.podNetworkReady)
}

func (node *OsdnNode) WaitForPodNetworkReady() error {
	logInterval := 10 * time.Second
	numIntervals := 12 // timeout: 2 mins

	for i := 0; i < numIntervals; i++ {
		select {
		// Wait for StartNode() to finish SDN setup
		case <-node.podNetworkReady:
			return nil
		case <-time.After(logInterval):
			log.Infof("Waiting for SDN pod network to be ready...")
		}
	}
	return fmt.Errorf("SDN pod network is not ready(timeout: 2 mins)")
}
