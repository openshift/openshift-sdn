package netutils

import (
	"encoding/binary"
	"fmt"
	"net"
	"os/exec"
	"strings"
)

func IPToUint32(ip net.IP) uint32 {
	return binary.BigEndian.Uint32(ip.To4())
}

func Uint32ToIP(u uint32) net.IP {
	ip := make([]byte, 4)
	binary.BigEndian.PutUint32(ip, u)
	return net.IPv4(ip[0], ip[1], ip[2], ip[3])
}

// Generate the default gateway IP Address for a subnet
func GenerateDefaultGateway(sna *net.IPNet) net.IP {
	ip := sna.IP.To4()
	return net.IPv4(ip[0], ip[1], ip[2], ip[3]|0x1)
}

func GetNodeIP(nodeName string) (string, error) {
	ip := net.ParseIP(nodeName)
	if ip == nil {
		addrs, err := net.LookupIP(nodeName)
		if err != nil {
			return "", fmt.Errorf("Failed to lookup IP address for node %s: %v", nodeName, err)
		}
		for _, addr := range addrs {
			if addr.String() != "127.0.0.1" {
				ip = addr
				break
			}
		}
	}
	if ip == nil || len(ip.String()) == 0 {
		return "", fmt.Errorf("Failed to obtain IP address from node name: %s", nodeName)
	}
	return ip.String(), nil
}

func GetNodeSubnet(nodeIP string) (*net.IPNet, error) {
	ip := net.ParseIP(nodeIP)
	if ip == nil {
		return nil, fmt.Errorf("Invalid nodeIP: %s", nodeIP)
	}

	routes, err := exec.Command("ip", "route").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("Could not execute 'ip route': %s", err)
	}
	for _, route := range strings.Split(string(routes), "\n") {
		words := strings.Split(route, " ")
		if len(words) == 0 || words[0] == "default" {
			continue
		}
		_, network, err := net.ParseCIDR(words[0])
		if err != nil {
			continue
		}
		if network.Contains(ip) {
			return network, nil
		}
	}
	return nil, fmt.Errorf("Could not find subnet for node %s", nodeIP)
}
