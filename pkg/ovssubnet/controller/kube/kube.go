package kube

import (
	"encoding/hex"
	"fmt"
	"io/ioutil"
	"net"
	"os/exec"
	"strings"

	"github.com/openshift/openshift-sdn/pkg/ipcmd"
	"github.com/openshift/openshift-sdn/pkg/netutils"
	"github.com/openshift/openshift-sdn/pkg/ovs"
	"github.com/openshift/openshift-sdn/pkg/ovssubnet/api"
)

const (
	BR       = "br0"
	LBR      = "lbr0"
	TUN      = "tun0"
	VLINUXBR = "vlinuxbr"
	VOVSBR   = "vovsbr"
	VXLAN    = "vxlan0"
)

type FlowController struct {
}

func NewFlowController() *FlowController {
	return &FlowController{}
}

func alreadySetUp(localSubnetGatewayCIDR string) bool {
	var found bool

	itx := ipcmd.NewTransaction(LBR)
	addrs, err := itx.GetAddresses()
	itx.EndTransaction()
	if err != nil {
		return false
	}
	found = false
	for _, addr := range addrs {
		if addr == localSubnetGatewayCIDR {
			found = true
			break
		}
	}
	if !found {
		return false
	}

	otx := ovs.NewTransaction(BR)
	flows, err := otx.DumpFlows()
	otx.EndTransaction()
	if err != nil {
		return false
	}
	found = false
	for _, flow := range flows {
		if strings.Contains(flow, "table=0") && strings.Contains(flow, "arp") {
			found = true
			break
		}
	}
	if !found {
		return false
	}

	return true
}

func (c *FlowController) Setup(localSubnetCIDR, clusterNetworkCIDR, servicesNetworkCIDR string, mtu uint) error {
	_, ipnet, err := net.ParseCIDR(localSubnetCIDR)
	localSubnetMaskLength, _ := ipnet.Mask.Size()
	localSubnetGateway := netutils.GenerateDefaultGateway(ipnet).String()
	if alreadySetUp(fmt.Sprintf("%s/%s", localSubnetGateway, localSubnetMaskLength)) {
		return nil
	}

	config := fmt.Sprintf("export OPENSHIFT_CLUSTER_SUBNET=%s", clusterNetworkCIDR)
	err = ioutil.WriteFile("/run/openshift/config.env", []byte(config), 0644)
	if err != nil {
		return err
	}

	itx := ipcmd.NewTransaction(VLINUXBR)
	itx.DeleteLink()
	itx.IgnoreError()
	itx.AddLink("type", "veth", "peer", "name", VOVSBR)
	itx.SetLink("up")
	itx.SetLink("txqueuelen", "0")
	err = itx.EndTransaction()
	if err != nil {
		return err
	}

	itx = ipcmd.NewTransaction(VOVSBR)
	itx.SetLink("up")
	itx.SetLink("txqueuelen", "0")
	err = itx.EndTransaction()
	if err != nil {
		return err
	}

	itx = ipcmd.NewTransaction(LBR)
	itx.SetLink("down")
	itx.IgnoreError()
	itx.DeleteLink()
	itx.IgnoreError()
	itx.AddLink("type", "bridge")
	itx.AddAddress(localSubnetGateway)
	itx.DeleteRoute(localSubnetCIDR, "proto", "kernel", "scope", "link", "src", localSubnetGateway)
	itx.SetLink("up")
	itx.AddSlave(VLINUXBR)
	err = itx.EndTransaction()
	if err != nil {
		return err
	}

	otx := ovs.NewTransaction(BR)
	otx.AddBridge("fail-mode=secure", "protocols=OpenFlow13")
	otx.AddPort(VXLAN, 1, "type=vxlan", `options:remote_ip="flow"`, `options:key="flow"`)
	otx.AddPort(TUN, 2, "type=internal")
	otx.AddPort(VOVSBR, 9)
	otx.AddFlow("table=0,priority=100,arp,nw_dst=%s,actions=output:2", localSubnetGateway)
	otx.AddFlow("table=0,priority=100,ip,nw_dst=%s,actions=output:2", localSubnetGateway)
	otx.AddFlow("table=0,priority=75,ip,nw_dst=%s,actions=output:9", localSubnetCIDR)
	otx.AddFlow("table=0,priority=75,arp,nw_dst=%s,actions=output:9", localSubnetCIDR)
	otx.AddFlow("table=0,priority=50,actions=output:2")
	err = otx.EndTransaction()
	if err != nil {
		return err
	}

	itx = ipcmd.NewTransaction(TUN)
	itx.AddAddress(localSubnetGateway)
	itx.SetLink("up")
	itx.AddRoute(clusterNetworkCIDR, "proto", "kernel", "scope", "link")
	err = itx.EndTransaction()
	if err != nil {
		return err
	}

	// Cleanup docker0 since docker won't
	itx = ipcmd.NewTransaction("docker0")
	itx.SetLink("down")
	itx.IgnoreError()
	itx.DeleteLink()
	itx.IgnoreError()
	_ = itx.EndTransaction()

	// Enable IP forwarding for ipv4 packets
	_, err = exec.Command("sysctl", "-w", "net.ipv4.ip_forward=1").CombinedOutput()
	if err != nil {
		return fmt.Errorf("Could not enable IPv4 forwarding: %s", err)
	}
	_, err = exec.Command("sysctl", "-w", fmt.Sprintf("net.ipv4.conf.%s.forwarding=1", TUN)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("Could not enable IPv4 forwarding on %s: %s", TUN, err)
	}

	return nil
}

func (c *FlowController) AddOFRules(nodeIP, nodeSubnetCIDR, localIP string) error {
	if nodeIP == localIP {
		return nil
	}

	cookie := generateCookie(nodeIP)
	otx := ovs.NewTransaction(BR)
	otx.AddFlow("table=0,cookie=0x%s,priority=100,ip,nw_dst=%s,actions=set_field:%s->tun_dst,output:1", cookie, nodeSubnetCIDR, nodeIP)
	otx.AddFlow("table=0,cookie=0x%s,priority=100,arp,nw_dst=%s,actions=set_field:%s->tun_dst,output:1", cookie, nodeSubnetCIDR, nodeIP)
	return otx.EndTransaction()
}

func (c *FlowController) DelOFRules(nodeIP, localIP string) error {
	if nodeIP == localIP {
		return nil
	}

	cookie := generateCookie(nodeIP)
	otx := ovs.NewTransaction(BR)
	otx.DeleteFlows("table=0,cookie=0x%s/0xffffffff,ip", cookie)
	otx.DeleteFlows("table=0,cookie=0x%s/0xffffffff,arp", cookie)
	return otx.EndTransaction()
}

func generateCookie(ip string) string {
	return hex.EncodeToString(net.ParseIP(ip).To4())
}

func (c *FlowController) AddServiceOFRules(netID uint, IP string, protocol api.ServiceProtocol, port uint) error {
	return nil
}

func (c *FlowController) DelServiceOFRules(netID uint, IP string, protocol api.ServiceProtocol, port uint) error {
	return nil
}

func (c *FlowController) UpdatePod(namespace, podName, containerID string, netID uint) error {
	return nil
}
