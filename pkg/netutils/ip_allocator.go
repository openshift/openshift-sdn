package netutils

import (
	"fmt"
	"net"
	"strconv"
)

type IPAllocator struct {
	network  *net.IPNet
	allocMap map[string]bool
}

func NewIPAllocator(network string, inUse []string) (*IPAllocator, error) {
	_, netIP, err := net.ParseCIDR(network)
	if err != nil {
		return nil, fmt.Errorf("Failed to parse network address: %q", network)
	}

	amap := make(map[string]bool)
	for _, netStr := range inUse {
		_, nIp, err := net.ParseCIDR(netStr)
		if err != nil {
			fmt.Println("Failed to parse network address: ", netStr)
			continue
		}
		if !netIP.Contains(nIp.IP) {
			fmt.Println("Provided subnet doesn't belong to network: ", nIp)
			continue
		}
		amap[netStr] = true
	}

	// Add the network address to the map
	amap[netIP.String()] = true
	return &IPAllocator{network: netIP, allocMap: amap}, nil
}

func nextIP(ip net.IP) {
	for j := len(ip) - 1; j >= 0; j-- {
		ip[j]++
		if ip[j] > 0 {
			break
		}
	}
}

func NewIPAllocatorBounded(network string, begin string, end string) (*IPAllocator, error) {
	_, ipnet, err := net.ParseCIDR(network)
	if err != nil {
		return nil, fmt.Errorf("Failed to parse network address: ", network)
	}

	beginIP := net.ParseIP(begin)
	if beginIP == nil {
		return nil, fmt.Errorf("Failed to parse beginning network address: ", begin)
	}
	if !ipnet.Contains(beginIP) {
		return nil, fmt.Errorf("Begining IP doesn't belong to network: ", begin)
	}

	endIP := net.ParseIP(end)
	if endIP == nil {
		return nil, fmt.Errorf("Failed to parse ending network address: ", end)
	}
	if !ipnet.Contains(endIP) {
		return nil, fmt.Errorf("Ending IP doesn't belong to network: ", end)
	}

	prefixSize, _ := ipnet.Mask.Size()
	prefix := strconv.FormatUint(uint64(prefixSize), 10)

	amap := make(map[string]bool)
	for ip := beginIP.Mask(ipnet.Mask); ipnet.Contains(ip); nextIP(ip) {
		if ip.String() == begin {
			break
		}
		amap[ip.String() + "/" + prefix] = true
	}
	// Advance one past end IP
	nextIP(endIP)
	for ip := endIP; ipnet.Contains(ip); nextIP(ip) {
		amap[ip.String() + "/" + prefix] = true
	}

	return &IPAllocator{network: ipnet, allocMap: amap}, nil
}

func (ipa *IPAllocator) GetIP() (*net.IPNet, error) {
	var (
		numIPs    uint32
		numIPBits uint
	)
	baseipu := IPToUint32(ipa.network.IP)
	netMaskSize, _ := ipa.network.Mask.Size()
	numIPBits = 32 - uint(netMaskSize)
	numIPs = 1 << numIPBits

	var i uint32
	// We exclude the last address as it is reserved for broadcast
	for i = 0; i < numIPs-1; i++ {
		ipu := baseipu | i
		genIP := &net.IPNet{IP: Uint32ToIP(ipu), Mask: net.CIDRMask(netMaskSize, 32)}
		if !ipa.allocMap[genIP.String()] {
			ipa.allocMap[genIP.String()] = true
			return genIP, nil
		}
	}

	return nil, fmt.Errorf("No IPs available.")
}

func (ipa *IPAllocator) ReleaseIP(ip *net.IPNet) error {
	if !ipa.network.Contains(ip.IP) {
		return fmt.Errorf("Provided IP %v doesn't belong to the network %v.", ip, ipa.network)
	}

	ipStr := ip.String()
	if !ipa.allocMap[ipStr] {
		return fmt.Errorf("Provided IP %v is already available.", ip)
	}

	ipa.allocMap[ipStr] = false

	return nil
}
