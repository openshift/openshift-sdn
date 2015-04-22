package ovs_subnet

import (
	"fmt"
	log "github.com/golang/glog"
	"net"
	"sync"
	"strings"
	"path"

	"github.com/openshift/openshift-sdn/pkg/netutils"
	"github.com/coreos/go-etcd/etcd"

	netutils_server "github.com/openshift/openshift-sdn/pkg/netutils/server"
)

type PubnetIpam struct {
	pnIpam          *netutils.IPAllocator
	gateway		string
	mux             sync.Mutex
	etcdCli		*etcd.Client
	etcdPath	string
}

func NewPubnetIpamServer(publicNetwork string,
                         etcdPeers []string,
                         etcdPath string,
                         etcdCertfile string,
                         etcdKeyfile string,
                         etcdCafile string) (*PubnetIpam, error) {
	var ipam *netutils.IPAllocator
	var gateway string

	if publicNetwork != "" {
		pnNet, pnBegin, pnEnd, pnGateway, err := parsePublicNetwork(publicNetwork)
		if err != nil {
			return nil, err
		}
		ipam, err = netutils.NewIPAllocatorBounded(pnNet.String(), pnBegin, pnEnd)
		if err != nil {
			return nil, err
		}
		gateway = pnGateway
	}

	etcdCli, err := etcd.NewTLSClient(etcdPeers, etcdCertfile, etcdKeyfile, etcdCafile)
	if err != nil {
		return nil, err
	}

	return &PubnetIpam{
		pnIpam:          ipam,
		gateway:	 gateway,
		etcdCli:	 etcdCli,
		etcdPath:	 etcdPath,
	}, nil
}

func NewPubnetIpamClient(etcdPeers []string,
                         etcdPath string,
                         etcdCertfile string,
                         etcdKeyfile string,
                         etcdCafile string) (*PubnetIpam, error) {
	etcdCli, err := etcd.NewTLSClient(etcdPeers, etcdCertfile, etcdKeyfile, etcdCafile)
	if err != nil {
		return nil, err
	}

	return &PubnetIpam{
		etcdCli:	 etcdCli,
		etcdPath:	 etcdPath,
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

func (self *PubnetIpam) StartServer(ipAddress string) error {
	addr := net.ParseIP(ipAddress)
	if addr == nil {
		return fmt.Errorf("Failed to parse server IP address: ", ipAddress)
	}

	serverKey := path.Join(self.etcdPath, "PublicNetworkIpamServer")
	gatewayKey := path.Join(self.etcdPath, "PublicNetworkGateway")

	self.mux.Lock()
	defer self.mux.Unlock()

	if self.pnIpam == nil {
		// Disable public network IPAM
		self.etcdCli.Delete(serverKey, false)
		self.etcdCli.Delete(gatewayKey, false)
		return nil
	}

	uri := "http://" + ipAddress + ":9081"
	_, err := self.etcdCli.Create(serverKey, uri, 0)
	if err != nil {
		log.Warningf("Found existing network configuration, overwriting it.")
		_, err = self.etcdCli.Update(serverKey, uri, 0)
		if err != nil {
			log.Errorf("Failed to write Network configuration to etcd: %v", err)
			return err
		}
	}

	_, err = self.etcdCli.Create(gatewayKey, self.gateway, 0)
	if err != nil {
		log.Warningf("Found existing gateway, overwriting it.")
		_, err = self.etcdCli.Update(gatewayKey, self.gateway, 0)
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
	key := path.Join(self.etcdPath, keyName)

	self.mux.Lock()
	defer self.mux.Unlock()

	resp, err := self.etcdCli.Get(key, false, false)
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

func (self *PubnetIpam) GetServerUri() (string, error) {
	return self.GetEtcdKey("PublicNetworkIpamServer")
}

func (self *PubnetIpam) GetGateway() (string, error) {
	return self.GetEtcdKey("PublicNetworkGateway")
}

