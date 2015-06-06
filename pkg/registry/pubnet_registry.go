package registry

import (
	"fmt"
	log "github.com/golang/glog"
	"net"
	"strings"

	"github.com/coreos/go-etcd/etcd"
	"github.com/openshift/openshift-sdn/pkg/api"
	"github.com/openshift/openshift-sdn/pkg/netutils"
	netutils_server "github.com/openshift/openshift-sdn/pkg/netutils/server"
)

type PubnetIpam struct {
	pnIpam  *netutils.IPAllocator
	gateway string
	cli     *EtcdClient
}

func NewPubnetServer(config *api.EtcdConfig, publicNetwork string) (api.PubnetRegistryServer, error) {
	var ipam *netutils.IPAllocator
	var gateway string

	if publicNetwork != "" {
		net, begin, end, gw, err := parsePublicNetwork(publicNetwork)
		if err != nil {
			return nil, err
		}
		ipam, err = netutils.NewIPAllocatorBounded(net.String(), begin, end)
		if err != nil {
			return nil, err
		}
		gateway = gw
	}

	cli, err := NewEtcdClient(config)
	if err != nil {
		return nil, err
	}

	return &PubnetIpam{
		pnIpam:  ipam,
		gateway: gateway,
		cli:     cli,
	}, nil
}

func NewPubnetClient(config *api.EtcdConfig) (api.PubnetRegistryClient, error) {
	cli, err := NewEtcdClient(config)
	if err != nil {
		return nil, err
	}

	return &PubnetIpam{
		cli: cli,
	}, nil
}

func parsePublicNetwork(publicNetwork string) (*net.IPNet, string, string, string, error) {
	a := strings.Split(publicNetwork, "-")
	if len(a) < 2 {
		return nil, "", "", "", fmt.Errorf("publicNetwork had no '-' separator")
	}
	b := strings.Split(a[1], "/")
	if len(b) < 2 {
		return nil, "", "", "", fmt.Errorf("publicNetwork had no '/' separator")
	}
	pnBegin := a[0]
	pnEnd := b[0]

	c := strings.Split(b[1], "+")
	if len(b) < 2 {
		return nil, "", "", "", fmt.Errorf("publicNetwork had no '+' separator")
	}
	prefix := c[0]
	gateway := c[1]

	_, pnNet, err := net.ParseCIDR(pnBegin + "/" + prefix)
	if err != nil {
		return nil, "", "", "", err
	}
	endIp, _, err := net.ParseCIDR(pnEnd + "/" + prefix)
	if err != nil {
		return nil, "", "", "", err
	}
	if !pnNet.Contains(endIp) {
		return nil, "", "", "", fmt.Errorf("publicNetwork end IP not contained in start IP network")
	}

	gwIP := net.ParseIP(gateway)
	if gwIP == nil {
		return nil, "", "", "", fmt.Errorf("publicNetwork had no gateway")
	}

	return pnNet, pnBegin, pnEnd, gateway, nil
}

func (self *PubnetIpam) Start(ipAddress string) error {
	addr := net.ParseIP(ipAddress)
	if addr == nil {
		return fmt.Errorf("Failed to parse server IP address: ", ipAddress)
	}

	serverKey := self.cli.getKeyPath("PublicNetworkIpamServer")
	gatewayKey := self.cli.getKeyPath("PublicNetworkGateway")

	if self.pnIpam == nil {
		// Disable public network IPAM
		self.cli.client().Delete(serverKey, false)
		self.cli.client().Delete(gatewayKey, false)
		return nil
	}

	uri := "http://" + ipAddress + ":9081"
	_, err := self.cli.client().Create(serverKey, uri, 0)
	if err != nil {
		log.Warningf("Found existing network configuration, overwriting it.")
		_, err = self.cli.client().Update(serverKey, uri, 0)
		if err != nil {
			log.Errorf("Failed to write Network configuration to etcd: %v", err)
			return err
		}
	}

	_, err = self.cli.client().Create(gatewayKey, self.gateway, 0)
	if err != nil {
		log.Warningf("Found existing gateway, overwriting it.")
		_, err = self.cli.client().Update(gatewayKey, self.gateway, 0)
		if err != nil {
			log.Errorf("Failed to write gateway to etcd: %v", err)
			return err
		}
	}

	// listen and serve does not return the control
	go netutils_server.ListenAndServeNetutilServer(self.pnIpam, addr, uint(9081), nil)

	return nil
}

func (self *PubnetIpam) GetEtcdKey(keyName string) (string, error) {
	key := self.cli.getKeyPath(keyName)

	resp, err := self.cli.client().Get(key, false, false)
	if err != nil {
		// Attempt to distinguish between "key does not exist" errors and
		// other errors talking to etcd
		if e, ok := err.(*etcd.EtcdError); ok {
			if e.ErrorCode == 100 {
				return "", nil
			}
		}
		return "", err
	}
	return resp.Node.Value, nil
}

func (self *PubnetIpam) GetServerUriAndGateway() (string, string, error) {
	uri, err := self.GetEtcdKey("PublicNetworkIpamServer")
	if err != nil {
		return "", "", err
	}
	gw, err := self.GetEtcdKey("PublicNetworkGateway")
	if err != nil {
		return "", "", err
	}

	return uri, gw, nil
}
