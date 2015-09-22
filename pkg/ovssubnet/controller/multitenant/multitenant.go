package multitenant

import (
	"encoding/hex"
	"fmt"
	log "github.com/golang/glog"
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
		if strings.Contains(flow, "NXM_NX_TUN_IPV4") {
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
	otx.AddPort(VOVSBR, 3)

	// Table 0; learn MAC addresses and continue with table 1
	otx.AddFlow("table=0, actions=learn(table=8, priority=200, hard_timeout=900, NXM_OF_ETH_DST[]=NXM_OF_ETH_SRC[], load:NXM_NX_TUN_IPV4_SRC[]->NXM_NX_TUN_IPV4_DST[], output:NXM_OF_IN_PORT[]), goto_table:1")

	// Table 1; initial dispatch
	otx.AddFlow("table=1, arp, actions=goto_table:8")
	otx.AddFlow("table=1, in_port=1, actions=goto_table:2")
	otx.AddFlow("table=1, in_port=2, actions=goto_table:5")
	otx.AddFlow("table=1, in_port=3, actions=goto_table:5")
	otx.AddFlow("table=1, actions=goto_table:3")

	// Table 2; incoming from vxlan
	otx.AddFlow("table=2, arp, actions=goto_table:8")
	otx.AddFlow("table=2, priority=200, ip, nw_dst=%s, actions=output:2", localSubnetGateway)
	otx.AddFlow("table=2, tun_id=0, actions=goto_table:5")
	otx.AddFlow("table=2, priority=100, ip, nw_dst=%s, actions=move:NXM_NX_TUN_ID[0..31]->NXM_NX_REG0[], goto_table:6", localSubnetCIDR)

	// Table 3; incoming from container; filled in by openshift-ovs-multitenant

	// Table 4; services; mostly filled in by multitenant.go
	otx.AddFlow("table=4, priority=100, ip, nw_dst=%s, actions=drop", servicesNetworkCIDR)
	otx.AddFlow("table=4, priority=0, actions=goto_table:5")

	// Table 5; general routing
	otx.AddFlow("table=5, priority=200, ip, nw_dst=%s, actions=output:2", localSubnetGateway)
	otx.AddFlow("table=5, priority=150, ip, nw_dst=%s, actions=goto_table:6", localSubnetCIDR)
	otx.AddFlow("table=5, priority=100, ip, nw_dst=%s, actions=goto_table:7", clusterNetworkCIDR)
	otx.AddFlow("table=5, priority=0, ip, actions=output:2")

	// Table 6; to local container; mostly filled in by openshift-ovs-multitenant
	otx.AddFlow("table=6, priority=200, ip, reg0=0, actions=goto_table:8")

	// Table 7; to remote container; filled in by multitenant.go

	// Table 8; MAC dispatch / ARP, filled in by Table 0's learn() rule
	// and with per-node vxlan ARP rules by multitenant.go
	otx.AddFlow("table=8, priority=0, arp, actions=flood")

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
	otx.AddFlow("table=7,cookie=0x%s,priority=100,ip,nw_dst=%s,actions=move:NXM_NX_REG0[]->NXM_NX_TUN_ID[0..31],set_field:%s->tun_dst,output:1", cookie, nodeSubnetCIDR, nodeIP)
	otx.AddFlow("table=8,cookie=0x%s,priority=100,arp,nw_dst=%s,actions=move:NXM_NX_REG0[]->NXM_NX_TUN_ID[0..31],set_field:%s->tun_dst,output:1", cookie, nodeSubnetCIDR, nodeIP)
	err := otx.EndTransaction()
	if err != nil {
		log.Infof("Error adding OVS flows: %v", err)
	}
	return err
}

func (c *FlowController) DelOFRules(nodeIP, localIP string) error {
	if nodeIP == localIP {
		return nil
	}

	log.Infof("Calling del rules for %s", nodeIP)
	cookie := generateCookie(nodeIP)
	otx := ovs.NewTransaction(BR)
	otx.DeleteFlows("table=7,cookie=0x%s/0xffffffff", cookie)
	otx.DeleteFlows("table=8,cookie=0x%s/0xffffffff", cookie)
	err := otx.EndTransaction()
	if err != nil {
		log.Infof("Error deleting OVS flows: %v", err)
	}
	return err
}

func generateCookie(ip string) string {
	return hex.EncodeToString(net.ParseIP(ip).To4())
}

func (c *FlowController) AddServiceOFRules(netID uint, IP string, protocol api.ServiceProtocol, port uint) error {
	rule := generateAddServiceRule(netID, IP, protocol, port)
	otx := ovs.NewTransaction(BR)
	otx.AddFlow(rule)
	err := otx.EndTransaction()
	if err != nil {
		log.Infof("Error adding OVS flow: %v", err)
	}
	return err
}

func (c *FlowController) DelServiceOFRules(netID uint, IP string, protocol api.ServiceProtocol, port uint) error {
	rule := generateDelServiceRule(IP, protocol, port)
	otx := ovs.NewTransaction(BR)
	otx.DeleteFlows(rule)
	err := otx.EndTransaction()
	if err != nil {
		log.Infof("Error deleting OVS flow: %v", err)
	}
	return err
}

func generateBaseServiceRule(IP string, protocol api.ServiceProtocol, port uint) string {
	return fmt.Sprintf("table=4,%s,nw_dst=%s,tp_dst=%d", strings.ToLower(string(protocol)), IP, port)
}

func generateAddServiceRule(netID uint, IP string, protocol api.ServiceProtocol, port uint) string {
	baseRule := generateBaseServiceRule(IP, protocol, port)
	if netID == 0 {
		return fmt.Sprintf("%s,priority=200,actions=output:2", baseRule)
	} else {
		return fmt.Sprintf("%s,priority=200,reg0=%d,actions=output:2", baseRule, netID)
	}
}

func generateDelServiceRule(IP string, protocol api.ServiceProtocol, port uint) string {
	return generateBaseServiceRule(IP, protocol, port)
}

func (c *FlowController) UpdatePod(namespace, podName, containerID string, netID uint) error {
	out, err := exec.Command("openshift-ovs-multitenant", "update", namespace, podName, containerID, fmt.Sprint(netID)).CombinedOutput()
	log.V(5).Infof("UpdatePod output: %s, error: %v", out, err)
	return err
}
