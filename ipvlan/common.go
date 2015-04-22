package ipvlan

import (
	"errors"
	"fmt"
	log "github.com/golang/glog"
	"net"
	"os"
	"os/exec"
	"time"

	"github.com/openshift/openshift-sdn/pkg/api"
	"github.com/openshift/openshift-sdn/pkg/netutils"

	netutils_server "github.com/openshift/openshift-sdn/pkg/netutils/server"
)

type IpvlanController struct {
	subnetRegistry  api.SubnetRegistry
	localIP         string
	localSubnet     *api.Subnet
	hostName        string
	subnetAllocator *netutils.SubnetAllocator
	sig             chan struct{}
	layer           uint
	pubnetServer    api.PubnetRegistryServer
	pubnetClient    api.PubnetRegistryClient
}

func NewIpvlanController(sub api.SubnetRegistry,
	hostname string,
	selfIP string,
	layer uint,
	pubnetServer api.PubnetRegistryServer,
	pubnetClient api.PubnetRegistryClient) (*IpvlanController, error) {
	if selfIP == "" {
		addrs, err := net.LookupIP(hostname)
		if err != nil {
			log.Errorf("Failed to lookup IP Address for %s", hostname)
			return nil, err
		}
		selfIP = addrs[0].String()
	}
	log.Infof("Self IP: %s.", selfIP)

	if pubnetServer != nil || pubnetClient != nil {
		return &IpvlanController{
			subnetRegistry:  sub,
			localIP:         selfIP,
			hostName:        hostname,
			localSubnet:     nil,
			subnetAllocator: nil,
			sig:             make(chan struct{}),
			layer:           layer,
			pubnetServer:    pubnetServer,
			pubnetClient:    pubnetClient,
		}, nil
	} else if layer == 3 {
		return nil, fmt.Errorf("ipvlan layer3 requires the publicNetwork option")
	}

	// Layer2 DHCP
	return &IpvlanController{
		subnetRegistry:  sub,
		localIP:         selfIP,
		hostName:        hostname,
		localSubnet:     nil,
		subnetAllocator: nil,
		sig:             make(chan struct{}),
		layer:           2,
	}, nil
}

func (self *IpvlanController) StartMaster(sync bool, containerNetwork string, containerSubnetLength uint) error {
	// wait a minute for etcd to come alive
	status := self.subnetRegistry.CheckEtcdIsAlive(60)
	if !status {
		log.Errorf("Etcd not running?")
		return errors.New("Etcd not reachable. Sync cluster check failed.")
	}
	// initialize the minion key
	if sync {
		err := self.subnetRegistry.InitMinions()
		if err != nil {
			log.Infof("Minion path already initialized.")
		}
	}

	// initialize the subnet key?
	err := self.subnetRegistry.InitSubnets()
	subrange := make([]string, 0)
	if err != nil {
		subnets, err := self.subnetRegistry.GetSubnets()
		if err != nil {
			log.Errorf("Error in initializing/fetching subnets: %v", err)
			return err
		}
		for _, sub := range *subnets {
			subrange = append(subrange, sub.Sub)
		}
	}

	err = self.subnetRegistry.WriteNetworkConfig(containerNetwork, containerSubnetLength)
	if err != nil {
		return err
	}

	self.subnetAllocator, err = netutils.NewSubnetAllocator(containerNetwork, containerSubnetLength, subrange)
	if err != nil {
		return err
	}

	err = self.ServeExistingMinions()
	if err != nil {
		log.Warningf("Error initializing existing minions: %v", err)
		// no worry, we can still keep watching it.
	}
	go self.watchMinions()

	log.Infof("Starting Public Network IPAM on local IP " + self.localIP)
	self.pubnetServer.Start(self.localIP)

	return nil
}

func (self *IpvlanController) ServeExistingMinions() error {
	minions, err := self.subnetRegistry.GetMinions()
	if err != nil {
		return err
	}

	for _, minion := range *minions {
		_, err := self.subnetRegistry.GetSubnet(minion)
		if err == nil {
			// subnet already exists, continue
			continue
		}
		err = self.AddNode(minion)
		if err != nil {
			return err
		}
	}
	return nil
}

func (self *IpvlanController) AddNode(minion string) error {
	sn, err := self.subnetAllocator.GetNetwork()
	if err != nil {
		log.Errorf("Error creating network for minion %s.", minion)
		return err
	}
	var minionIP string
	ip := net.ParseIP(minion)
	if ip == nil {
		addrs, err := net.LookupIP(minion)
		if err != nil {
			log.Errorf("Failed to lookup IP address for minion %s: %v", minion, err)
			return err
		}
		minionIP = addrs[0].String()
		if minionIP == "" {
			return fmt.Errorf("Failed to obtain IP address from minion label: %s", minion)
		}
	} else {
		minionIP = ip.String()
	}
	sub := &api.Subnet{
		Minion: minionIP,
		Sub:    sn.String(),
	}
	self.subnetRegistry.CreateSubnet(minion, sub)
	if err != nil {
		log.Errorf("Error writing subnet to etcd for minion %s: %v", minion, sn)
		return err
	}
	return nil
}

func (self *IpvlanController) DeleteNode(minion string) error {
	sub, err := self.subnetRegistry.GetSubnet(minion)
	if err != nil {
		log.Errorf("Error fetching subnet for minion %s for delete operation.", minion)
		return err
	}
	_, ipnet, err := net.ParseCIDR(sub.Sub)
	if err != nil {
		log.Errorf("Error parsing subnet for minion %s for deletion: %s", minion, sub.Sub)
		return err
	}
	self.subnetAllocator.ReleaseNetwork(ipnet)
	return self.subnetRegistry.DeleteSubnet(minion)
}

func (self *IpvlanController) syncWithMaster() error {
	return self.subnetRegistry.CreateMinion(self.hostName, self.localIP)
}

func (self *IpvlanController) manageLocalIpam(ipnet *net.IPNet, containerNetwork string) (string, error) {
	ipamHost := "127.0.0.1"
	ipamPort := uint(9080)

	inuse := make([]string, 0)
	ipam, err := netutils.NewIPAllocator(ipnet.String(), inuse)
	if err != nil {
		return "", err
	}

	// listen and serve does not return the control
	go netutils_server.ListenAndServeNetutilServer(ipam, net.ParseIP(ipamHost), ipamPort, nil)
	return "http://" + ipamHost + ":9080", nil
}

func (self *IpvlanController) StartNode(sync, skipsetup bool) error {
	if sync {
		err := self.syncWithMaster()
		if err != nil {
			log.Errorf("Failed to register with master: %v", err)
			return err
		}
	}
	err := self.initSelfSubnet()
	if err != nil {
		log.Errorf("Failed to get subnet for this host: %v", err)
		return err
	}

	if !skipsetup {
		// Assume we are working with IPv4
		containerNetwork, err := self.subnetRegistry.GetContainerNetwork()
		if err != nil {
			log.Errorf("Failed to obtain ContainerNetwork: %v", err)
			return err
		}

		out, err := exec.Command("openshift-ipvlan-kube-subnet-setup.sh").CombinedOutput()
		log.Infof("Output of setup script:\n%s", out)
		if err != nil {
			log.Errorf("Error executing setup script. \n\tOutput: %s\n\tError: %v\n", out, err)
			return err
		}

		// Container network IPAM
		_, ipnet, err := net.ParseCIDR(self.localSubnet.Sub)
		if err != nil {
			return err
		}
		privateUri, err := self.manageLocalIpam(ipnet, containerNetwork)
		if err != nil {
			return err
		}

		f, err := os.Create("/etc/openshift-sdn/config.env")
		if err != nil {
			return err
		}
		_, err = f.WriteString(fmt.Sprintf("OPENSHIFT_PRIVATE_IPAM_SERVER=%s\n", privateUri))
		if err != nil {
			return err
		}
		_, err = f.WriteString(fmt.Sprintf("OPENSHIFT_IPVLAN_LAYER=%d\n", self.layer))
		if err != nil {
			return err
		}

		if self.pubnetClient != nil {
			// Public network IPAM
			publicUri, publicGateway, err := self.pubnetClient.GetServerUriAndGateway()
			if err != nil {
				return err
			}

			if publicUri != "" && publicGateway != "" {
				_, err = f.WriteString(fmt.Sprintf("OPENSHIFT_PUBLIC_IPAM_SERVER=%s\nOPENSHIFT_PUBLIC_GATEWAY=%s\n", publicUri, publicGateway))
				if err != nil {
					return err
				}
			}
		}

		f.Close()
	}
	return err
}

func (self *IpvlanController) initSelfSubnet() error {
	// get subnet for self
	for {
		sub, err := self.subnetRegistry.GetSubnet(self.hostName)
		if err != nil {
			log.Errorf("Could not find an allocated subnet for minion %s: %s. Waiting...", self.hostName, err)
			time.Sleep(2 * time.Second)
			continue
		}
		self.localSubnet = sub
		return nil
	}
}

func (self *IpvlanController) watchMinions() {
	// watch latest?
	stop := make(chan bool)
	minevent := make(chan *api.MinionEvent)
	go self.subnetRegistry.WatchMinions(minevent, stop)
	for {
		select {
		case ev := <-minevent:
			switch ev.Type {
			case api.Added:
				_, err := self.subnetRegistry.GetSubnet(ev.Minion)
				if err != nil {
					// subnet does not exist already
					self.AddNode(ev.Minion)
				}
			case api.Deleted:
				self.DeleteNode(ev.Minion)
			}
		case <-self.sig:
			log.Error("Signal received. Stopping watching of minions.")
			stop <- true
			return
		}
	}
}

func (self *IpvlanController) Stop() {
	close(self.sig)
	//self.sig <- struct{}{}
}
