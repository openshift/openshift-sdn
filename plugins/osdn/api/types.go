package api

import (
	pconfig "k8s.io/kubernetes/pkg/proxy/config"

	cnitypes "github.com/containernetworking/cni/pkg/types"
)

type FilteringEndpointsConfigHandler interface {
	pconfig.EndpointsConfigHandler
	Start(baseHandler pconfig.EndpointsConfigHandler) error
}

const NodeConfigPath string = "/var/run/openshift-sdn/nodeConfig.json"

// Written to /var/run/openshift-sdn/nodeConfig.json after the node's
// subnet is retrieved from the master controller.
type CNINodeConfig struct {
	ClusterNetwork string `json:"clusterNetwork"`
	NodeNetwork    string `json:"nodeNetwork"`
	NodeGateway    string `json:"nodeGateway"`
	MTU            uint   `json:"mtu"`
}

// Standard CNI network configuration block written to /etc/cni/net.d
// which is picked up by the Kubernetes CNI network plugin and used to
// run our openshift-sdn CNI plugin
type CNINetConfig struct {
	cnitypes.NetConf
	MasterKubeConfig string `json:"masterKubeConfig"`
	Multitenant      bool   `json:"multitenant"`
}
