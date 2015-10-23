package flannelmt

import (
	"encoding/json"
	"fmt"
	"path"
	"strconv"

	"github.com/golang/glog"

	knetwork "k8s.io/kubernetes/pkg/kubelet/network"
	kubeletTypes "k8s.io/kubernetes/pkg/kubelet/types"
	utilexec "k8s.io/kubernetes/pkg/util/exec"

	"github.com/openshift/openshift-sdn/plugins/osdn"
	"github.com/openshift/openshift-sdn/plugins/osdn/api"
	oskserver "github.com/openshift/origin/pkg/cmd/server/kubernetes"

	"github.com/coreos/go-etcd/etcd"
)

const (
	CLUSTER_NETWORK_NAME = "~~flannel-ovs-cluster-network~~"
)

type flannelmtPlugin struct {
	osdn.OvsController

	etcdClient       *etcd.Client
	registry         *osdn.Registry
	flowController   osdn.FlowController
	hostSubnetLength uint
}

func NetworkPluginName() string {
	return "redhat/openshift-ovs-flannelmt"
}

func CreatePlugin(registry *osdn.Registry, etcdClient *etcd.Client, hostname string, selfIP string) (api.OsdnPlugin, oskserver.FilteringEndpointsConfigHandler, error) {
	fmp := &flannelmtPlugin{
		etcdClient:     etcdClient,
		registry:       registry,
		flowController: NewFlowController(),
	}

	if err := fmp.BaseInit(registry, fmp.flowController, fmp, hostname, selfIP); err != nil {
		return nil, nil, err
	}

	return fmp, registry, nil
}

func (plugin *flannelmtPlugin) PluginStartMaster(clusterNetworkCIDR string, clusterBitsPerSubnet uint, serviceNetworkCIDR string) error {
	plugin.hostSubnetLength = clusterBitsPerSubnet

	// add the flannel cluster network config so nodes know what subnets to grab
	if err := plugin.writeFlannelClusterNetwork(clusterNetworkCIDR, clusterBitsPerSubnet, serviceNetworkCIDR); err != nil {
		return fmt.Errorf("failed to add flannel cluster network config: %v", err)
	}

	if err := plugin.VnidStartMaster(); err != nil {
		return err
	}

	return nil
}

func (plugin *flannelmtPlugin) PluginStartNode(mtu uint) error {
	if err := plugin.VnidStartNode(); err != nil {
		return err
	}

	if err := plugin.flowController.Setup("", "", "", mtu); err != nil {
		return err
	}

	return nil
}

type FlannelClusterNetwork struct {
	Network       string
	SubnetLen     uint
	ClusterConfig FlannelClusterConfig `json:"Backend"`
}

type FlannelClusterConfig struct {
	Type            string
	ServicesNetwork string
}

func (plugin *flannelmtPlugin) writeFlannelClusterNetwork(clusterNetworkCIDR string, clusterBitsPerSubnet uint, serviceNetworkCIDR string) error {
	fc := FlannelClusterConfig{
		Type:            "ovs",
		ServicesNetwork: serviceNetworkCIDR,
	}

	cn := &FlannelClusterNetwork{
		Network:       clusterNetworkCIDR,
		SubnetLen:     32 - clusterBitsPerSubnet,
		ClusterConfig: fc,
	}

	netJson, err := json.Marshal(cn)
	if err != nil {
		return err
	}

	key := path.Join("/coreos.com/network", CLUSTER_NETWORK_NAME, "config")
	glog.Infof("##### Writing flannel cluster network key %s config '%s'", key, string(netJson))
	_, err = plugin.etcdClient.Set(key, string(netJson), 0)
	if err != nil {
		return err
	}

	return nil
}

//-----------------------------------------------

const (
	setUpCmd    = "setup"
	tearDownCmd = "teardown"
	statusCmd   = "status"
)

func (plugin *flannelmtPlugin) getExecutable() string {
	return "openshift-sdn-flannelmt"
}

func (plugin *flannelmtPlugin) Init(host knetwork.Host) error {
	return nil
}

func (plugin *flannelmtPlugin) Name() string {
	return NetworkPluginName()
}

func (plugin *flannelmtPlugin) SetUpPod(namespace string, name string, id kubeletTypes.DockerID) error {
	err := plugin.WaitForPodNetworkReady()
	if err != nil {
		return err
	}

	vnid, found := plugin.VNIDMap[namespace]
	if !found {
		return fmt.Errorf("Error fetching VNID for namespace: %s", namespace)
	}
	vnidstr := strconv.FormatUint(uint64(vnid), 10)

	out, err := utilexec.New().Command(plugin.getExecutable(), setUpCmd, namespace, name, string(id), vnidstr).CombinedOutput()
	glog.V(5).Infof("SetUpPod 'flannelmt' network plugin output: %s, %v", string(out), err)
	return err
}

func (plugin *flannelmtPlugin) TearDownPod(namespace string, name string, id kubeletTypes.DockerID) error {
	// The script's teardown functionality doesn't need the VNID
	out, err := utilexec.New().Command(plugin.getExecutable(), tearDownCmd, namespace, name, string(id), "-1").CombinedOutput()
	glog.V(5).Infof("TearDownPod 'flannelmt' network plugin output: %s, %v", string(out), err)
	return err
}

func (plugin *flannelmtPlugin) Status(namespace string, name string, id kubeletTypes.DockerID) (*knetwork.PodNetworkStatus, error) {
	return nil, nil
}
