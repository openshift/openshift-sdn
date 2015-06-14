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

	"github.com/openshift/openshift-sdn/pkg/brctl"
	"github.com/openshift/openshift-sdn/pkg/ipcmd"
	"github.com/openshift/openshift-sdn/pkg/iptables"
	"github.com/openshift/openshift-sdn/pkg/netutils"
	netutils_server "github.com/openshift/openshift-sdn/pkg/netutils/server"
	"github.com/openshift/openshift-sdn/pkg/ovs"
)

const (
	LBR      = "lbr0"
	BR       = "br0"
	VLBR     = "vlinuxbr"
	VOVSBR   = "vovsbr"
	TUN      = "tun0"
	ENV_FILE = `/run/openshift-sdn/docker-network`
	ENV_FMT  = `# This file has been modified by openshift-sdn. Please modify the
# DOCKER_NETWORK_OPTIONS variable in /etc/sysconfig/openshift-node if this
# is an integrated install or /etc/sysconfig/openshift-sdn-node if this is a
# standalone install.

DOCKER_NETWORK_OPTIONS='%s'`
	ETC_FILE = "/etc/openshift-sdn/config.env"
	ETC_FMT  = `export OPENSHIFT_SDN_TAP1_ADDR=%s
export OPENSHIFT_CLUSTER_SUBNET=%s`
)

type FlowController struct {
	oc *ovs.OVS
}

func NewFlowController() *FlowController {
	return &FlowController{}
}

func (c *FlowController) Setup(localSubnet, containerNetwork string) error {
	_, subnet, err := net.ParseCIDR(localSubnet)
	if err != nil {
		return nil
	}
	ms, _ := subnet.Mask.Size()
	maskLength := strconv.Itoa(ms)
	gatewayIP := netutils.GenerateDefaultGateway(subnet)

	envFile, e := os.OpenFile(ENV_FILE, os.O_RDWR|os.O_CREATE, 0640)
	if e != nil {
		return e
	}
	defer envFile.Close()

	ipt, err := iptables.NewIPTables()
	if err != nil {
		return err
	}

	oc, err := ovs.NewOVS()
	if err != nil {
		return err
	}
	c.oc = oc

	ip, err := ipcmd.NewIPCmd()
	if err != nil {
		return err
	}

	brc, err := brctl.NewBrctl()
	if err != nil {
		return err
	}

	if !setup_required(gatewayIP, envFile) {
		return nil
	}

	if _, err := envFile.Seek(0, 0); err != nil {
		return err
	}
	if err := envFile.Truncate(0); err != nil {
		return err
	}

	// openvswitch
	_ = oc.Execute(ovs.DelBridge, BR)
	rule := []string{BR, "--", "set", "Bridge", BR, "fail-mode=secure"}
	if err := oc.Execute(ovs.AddBridge, rule...); err != nil {
		return err
	}
	rule = []string{"bridge", BR, "protocols=OpenFlow13"}
	if err := oc.Execute(ovs.Set, rule...); err != nil {
		return err
	}

	rule = []string{BR, "vxlan0"}
	_ = oc.Execute(ovs.DelPort, rule...)

	rule = []string{BR, "vxlan0", "--", "set", "Interface", "vxlan0", "type=vxlan", `options:remote_ip="flow"`, `options:key="flow"`, "ofport_request=1"}
	if err := oc.Execute(ovs.AddPort, rule...); err != nil {
		return err
	}
	rule = []string{BR, TUN, "--", "set", "Interface", TUN, "type=internal", "ofport_request=2"}
	if err := oc.Execute(ovs.AddPort, rule...); err != nil {
		return err
	}

	rule = []string{VLBR}
	_ = ip.Execute(ipcmd.Link, ipcmd.Del, rule...)

	rule = []string{VLBR, "type", "veth", "peer", "name", VOVSBR}
	if err := ip.Execute(ipcmd.Link, ipcmd.Add, rule...); err != nil {
		return err
	}

	rule = []string{VLBR, "up"}
	if err := ip.Execute(ipcmd.Link, ipcmd.Set, rule...); err != nil {
		return err
	}

	rule = []string{VOVSBR, "up"}
	if err := ip.Execute(ipcmd.Link, ipcmd.Set, rule...); err != nil {
		return err
	}

	rule = []string{VLBR, "txqueuelen", "0"}
	if err := ip.Execute(ipcmd.Link, ipcmd.Set, rule...); err != nil {
		return err
	}

	rule = []string{VOVSBR, "txqueuelen", "0"}
	if err := ip.Execute(ipcmd.Link, ipcmd.Set, rule...); err != nil {
		return err
	}

	rule = []string{BR, VOVSBR}
	_ = oc.Execute(ovs.DelPort, rule...)
	rule = []string{BR, VOVSBR, "--", "set", "Interface", VOVSBR, "ofport_request=9"}
	if err := oc.Execute(ovs.AddPort, rule...); err != nil {
		return err
	}

	// linux bridge
	rule = []string{LBR, "down"}
	_ = ip.Execute(ipcmd.Link, ipcmd.Set, rule...)

	rule = []string{LBR}
	_ = brc.Execute(brctl.DelBr, rule...)

	rule = []string{LBR}
	if err := brc.Execute(brctl.AddBr, rule...); err != nil {
		return err
	}

	rule = []string{gatewayIP.String() + "/" + maskLength, "dev", LBR}
	if err := ip.Execute(ipcmd.Addr, ipcmd.Add, rule...); err != nil {
		return err
	}

	rule = []string{LBR, "up"}
	if err := ip.Execute(ipcmd.Link, ipcmd.Set, rule...); err != nil {
		return err
	}

	rule = []string{LBR, VLBR}
	if err := brc.Execute(brctl.AddIf, rule...); err != nil {
		return err
	}

	// setup tun address
	rule = []string{gatewayIP.String() + "/" + maskLength, "dev", TUN}
	if err := ip.Execute(ipcmd.Addr, ipcmd.Add, rule...); err != nil {
		return err
	}
	rule = []string{TUN, "up"}
	if err := ip.Execute(ipcmd.Link, ipcmd.Set, rule...); err != nil {
		return err
	}
	rule = []string{containerNetwork, "dev", TUN, "proto", "kernel", "scope", "link"}
	if err := ip.Execute(ipcmd.Route, ipcmd.Add, rule...); err != nil {
		return err
	}

	// iptables
	postrouting := ipt.GetChain(iptables.Nat, "POSTROUTING")
	rule = []string{"-s", containerNetwork, "!", "-d", containerNetwork, "-j", "MASQUERADE"}
	_ = postrouting.AddRule(iptables.Delete, rule...)
	if err := postrouting.AddRule(iptables.Append, rule...); err != nil {
		return err
	}

	input := ipt.GetChain("", "INPUT")
	rule = []string{"-p", "udp", "-m", "multiport", "--dports", "4789", "-m", "comment", "--comment", "001 vxlan incoming", "-j", "ACCEPT"}
	_ = input.AddRule(iptables.Delete, rule...)
	if err = input.AddRule(iptables.Insert, rule...); err != nil {
		return err
	}

	rule = []string{"-i", TUN, "-m", "comment", "--comment", "traffic from docker for internet", "-j", "ACCEPT"}
	_ = input.AddRule(iptables.Delete, rule...)
	if err = input.AddRule(iptables.Insert, rule...); err != nil {
		return err
	}

	forward := ipt.GetChain("", "FORWARD")
	// allow everything from containerNetwork
	rule = []string{"-d", containerNetwork, "-j", "ACCEPT"}
	_ = forward.AddRule(iptables.Delete, rule...)
	if err := forward.AddRule(iptables.Append, rule...); err != nil {
		return err
	}
	// allow everything to containerNetwork
	rule[0] = "-s"
	_ = forward.AddRule(iptables.Delete, rule...)
	if err := forward.AddRule(iptables.Append, rule...); err != nil {
		return err
	}

	//go c.manageLocalIpam(ipnet)
	rule = []string{BR}
	if err = oc.Execute(ovs.DelFlows, rule...); err != nil {
		return err
	}
	rule = []string{BR, "cookie=0x0,table=0,priority=50,actions=output:2"}
	if err = oc.Execute(ovs.AddFlow, rule...); err != nil {
		return err
	}
	// arp rule
	rule = []string{BR, fmt.Sprintf("cookie=0x0,table=0,priority=100,arp,nw_dst=%s,actions=output:2", gatewayIP.String())}
	if err = oc.Execute(ovs.AddFlow, rule...); err != nil {
		return err
	}
	// ip rule
	rule = []string{BR, fmt.Sprintf("cookie=0x0,table=0,priority=100,ip,nw_dst=%s,actions=output:2", gatewayIP.String())}
	if err = oc.Execute(ovs.AddFlow, rule...); err != nil {
		return err
	}

	if path, err := exec.LookPath("modprobe"); err == nil {
		_ = exec.Command(path, "br_netfilter").Run()
	}

	path, err := exec.LookPath("sysctl")
	if err != nil {
		return err
	}
	args := []string{"-w", "net.bridge.brdige-nf-call-iptables=0"}
	err = exec.Command(path, args...).Run()
	if err != nil {
		return err
	}

	// THIS IS A BAD IDEA, PROGRAMS DON'T WRITE IN ETC!!!
	etcFile, err := os.Create(ETC_FILE)
	if err != nil {
		return err
	}
	defer etcFile.Close()

	s := fmt.Sprintf(ETC_FMT, gatewayIP.String(), containerNetwork)
	etc_writer := bufio.NewWriter(etcFile)
	if l, err := etc_writer.WriteString(s); l != len(s) || err != nil {
		return err
	}

	opts := os.Getenv("DOCKER_NETWORK_OPTIONS")
	if len(opts) == 0 {
		opts = "-b=" + LBR + " --mtu=1450"
	}
	s = fmt.Sprintf(ENV_FMT, opts)
	env_writer := bufio.NewWriter(envFile)
	if l, err := env_writer.WriteString(s); l != len(s) || err != nil {
		return err
	}
	return err
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
		// ip rule
		rule := []string{BR, fmt.Sprintf("table=0,cookie=0x%s,priority=75,ip,nw_dst=%s,actions=output:9", cookie, subnet)}
		log.Infof("Adding %s", rule)
		if err := c.oc.Execute(ovs.AddFlow, rule...); err != nil {
			return err
		}
		// arp rule
		rule = []string{BR, fmt.Sprintf("table=0,cookie=0x%s,priority=75,arp,nw_dst=%s,actions=output:9", cookie, subnet)}
		log.Info("Adding %s", rule)
		if err := c.oc.Execute(ovs.AddFlow, rule...); err != nil {
			return err
		}
	} else {
		// ip rule
		rule := []string{BR, fmt.Sprintf("table=0,cookie=0x%s,priority=100,ip,nw_dst=%s,actions=set_field:%s->tun_dst,output:1", cookie, subnet, minionIP)}
		log.Info("Adding %s", rule)
		if err := c.oc.Execute(ovs.AddFlow, rule...); err != nil {
			return err
		}
		// arp rule
		rule = []string{BR, fmt.Sprintf("table=0,cookie=0x%s,priority=100,arp,nw_dst=%s,actions=set_field:%s->tun_dst,output:1", cookie, subnet, minionIP)}
		log.Info("Adding %s", rule)
		if err := c.oc.Execute(ovs.AddFlow, rule...); err != nil {
			return err
		}
	}
	return nil
}

func (c *FlowController) DelOFRules(minion, localIP string) error {
	log.Infof("Calling del rules for %s", minion)
	cookie := generateCookie(minion)
	if minion == localIP {
		// ip rule
		rule := []string{BR, fmt.Sprintf("table=0,cookie=0x%s/0xffffffff,ip,in_port=10", cookie)}
		log.Info("Removing %s", rule)
		if err := c.oc.Execute(ovs.DelFlows, rule...); err != nil {
			return err
		}
		// arp rule
		rule = []string{BR, fmt.Sprintf("table=0,cookie=0x%s/0xffffffff,arp,in_port=10", cookie)}
		log.Info("Removing %s", rule)
		if err := c.oc.Execute(ovs.DelFlows, rule...); err != nil {
			return err
		}
	} else {
		// ip rule
		rule := []string{BR, fmt.Sprintf("table=0,cookie=0x%s/0xffffffff,ip", cookie)}
		log.Info("Removing %s", rule)
		if err := c.oc.Execute(ovs.DelFlows, rule...); err != nil {
			return err
		}
		// arp rule
		rule = []string{BR, fmt.Sprintf("table=0,cookie=0x%s/0xffffffff,arp", cookie)}
		log.Info("Removing %s", rule)
		if err := c.oc.Execute(ovs.DelFlows, rule...); err != nil {
			return err
		}
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
