package kube

import (
	"bufio"
	"crypto/md5"
	"fmt"
	log "github.com/golang/glog"
	"io/ioutil"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/openshift/openshift-sdn/pkg/netutils"
	netutils_server "github.com/openshift/openshift-sdn/pkg/netutils/server"
)

const (
	ENVFILE = `/run/openshift-sdn/docker-network`
	LBR     = "lbr0"
	TUN     = "tun0"
)

type FlowController struct {
}

func NewFlowController() *FlowController {
	return &FlowController{}
}

func (c *FlowController) Setup(localSubnet, containerNetwork string) error {
	_, subnet, err := net.ParseCIDR(localSubnet)
	if err != nil {
		return err
	}
	//s, _ := subnet.Mask.Size()
	//maskLength := strconv.Itoa(s)
	gatewayIP := netutils.GenerateDefaultGateway(subnet)

	envFile, e := os.OpenFile(ENVFILE, os.O_RDWR|os.O_CREATE, 0640)
	if e != nil {
		return e
	}
	defer envFile.Close()

	if !setup_required(gatewayIP, envFile) {
		return nil
	}

	if _, err := envFile.Seek(0, 0); err != nil {
		return err
	}
	if err := envFile.Truncate(0); err != nil {
		return err
	}

	return nil
}

func (c *FlowController) manageLocalIpam(ipnet *net.IPNet) error {
	ipamHost := "127.0.0.1"
	ipamPort := uint(9080)
	inuse := make([]string, 0)
	ipam, _ := netutils.NewIPAllocator(ipnet.String(), inuse)
	f, err := os.Create("/etc/openshift-sdn/config.env")
	if err != nil {
		return err
	}
	_, err = f.WriteString(fmt.Sprintf("OPENSHIFT_SDN_TAP1_ADDR=%s\nOPENSHIFT_SDN_IPAM_SERVER=http://%s:%s", netutils.GenerateDefaultGateway(ipnet), ipamHost, ipamPort))
	if err != nil {
		return err
	}
	f.Close()
	// listen and serve does not return the control
	netutils_server.ListenAndServeNetutilServer(ipam, net.ParseIP(ipamHost), ipamPort, nil)
	return nil
}

func (c *FlowController) AddOFRules(minionIP, subnet, localIP string) error {
	cookie := generateCookie(minionIP)
	if minionIP == localIP {
		// self, so add the input rules for containers that are not processed through kube-hooks
		// for the input rules to pods, see the kube-hook
		iprule := fmt.Sprintf("table=0,cookie=0x%s,priority=75,ip,nw_dst=%s,actions=output:9", cookie, subnet)
		arprule := fmt.Sprintf("table=0,cookie=0x%s,priority=75,arp,nw_dst=%s,actions=output:9", cookie, subnet)
		o, e := exec.Command("ovs-ofctl", "-O", "OpenFlow13", "add-flow", "br0", iprule).CombinedOutput()
		log.Infof("Output of adding %s: %s (%v)", iprule, o, e)
		o, e = exec.Command("ovs-ofctl", "-O", "OpenFlow13", "add-flow", "br0", arprule).CombinedOutput()
		log.Infof("Output of adding %s: %s (%v)", arprule, o, e)
		return e
	} else {
		iprule := fmt.Sprintf("table=0,cookie=0x%s,priority=100,ip,nw_dst=%s,actions=set_field:%s->tun_dst,output:1", cookie, subnet, minionIP)
		arprule := fmt.Sprintf("table=0,cookie=0x%s,priority=100,arp,nw_dst=%s,actions=set_field:%s->tun_dst,output:1", cookie, subnet, minionIP)
		o, e := exec.Command("ovs-ofctl", "-O", "OpenFlow13", "add-flow", "br0", iprule).CombinedOutput()
		log.Infof("Output of adding %s: %s (%v)", iprule, o, e)
		o, e = exec.Command("ovs-ofctl", "-O", "OpenFlow13", "add-flow", "br0", arprule).CombinedOutput()
		log.Infof("Output of adding %s: %s (%v)", arprule, o, e)
		return e
	}
	return nil
}

func (c *FlowController) DelOFRules(minion, localIP string) error {
	log.Infof("Calling del rules for %s", minion)
	cookie := generateCookie(minion)
	if minion == localIP {
		iprule := fmt.Sprintf("table=0,cookie=0x%s/0xffffffff,ip,in_port=10", cookie)
		arprule := fmt.Sprintf("table=0,cookie=0x%s/0xffffffff,arp,in_port=10", cookie)
		o, e := exec.Command("ovs-ofctl", "-O", "OpenFlow13", "del-flows", "br0", iprule).CombinedOutput()
		log.Infof("Output of deleting local ip rules %s (%v)", o, e)
		o, e = exec.Command("ovs-ofctl", "-O", "OpenFlow13", "del-flows", "br0", arprule).CombinedOutput()
		log.Infof("Output of deleting local arp rules %s (%v)", o, e)
		return e
	} else {
		iprule := fmt.Sprintf("table=0,cookie=0x%s/0xffffffff,ip", cookie)
		arprule := fmt.Sprintf("table=0,cookie=0x%s/0xffffffff,arp", cookie)
		o, e := exec.Command("ovs-ofctl", "-O", "OpenFlow13", "del-flows", "br0", iprule).CombinedOutput()
		log.Infof("Output of deleting %s: %s (%v)", iprule, o, e)
		o, e = exec.Command("ovs-ofctl", "-O", "OpenFlow13", "del-flows", "br0", arprule).CombinedOutput()
		log.Infof("Output of deleting %s: %s (%v)", arprule, o, e)
		return e
	}
	return nil
}

func interfaceHasIP(name string, address net.IP) bool {
	interfaces, e := net.Interfaces()
	if e != nil {
		return false
	}
	for _, inter := range interfaces {
		if inter.Name != name {
			continue
		}
		addrs, e := inter.Addrs()
		if e != nil {
			return false
		}
		for _, addr := range addrs {
			switch ip := addr.(type) {
			case *net.IPAddr:
				// annoyed that fallthrough doesn't work
				if ip.IP.Equal(address) {
					return true
				}
			case *net.IPNet:
				if ip.IP.Equal(address) {
					return true
				}
			}
		}
	}
	return false
}

func setup_required(ip net.IP, envFile *os.File) bool {
	if !interfaceHasIP(LBR, ip) {
		return true
	}

	reader := bufio.NewReader(envFile)
	contents, err := ioutil.ReadAll(reader)
	if err != nil {
		return true
	}
	if !strings.Contains(string(contents), LBR) {
		return true
	}
	return false
}

func generateCookie(ip string) string {
	return strconv.FormatInt(int64(md5.Sum([]byte(ip))[0]), 16)
}
