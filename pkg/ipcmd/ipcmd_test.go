package ipcmd

import (
	"fmt"
	"testing"

	"github.com/openshift/openshift-sdn/pkg/exec"
)

// Using a global variable initializer ensures this runs before ipcmd.go's init()
var dummy = setTestMode()

func setTestMode() bool {
	exec.SetTestMode()
	exec.AddTestProgram("/sbin/ip")
	return true
}

func TestGetAddresses(t *testing.T) {
	exec.AddTestResult("/sbin/ip addr show dev lo", `1: lo: <LOOPBACK,UP,LOWER_UP> mtu 65536 qdisc noqueue state UNKNOWN group default 
    link/loopback 00:00:00:00:00:00 brd 00:00:00:00:00:00
    inet 127.0.0.1/8 scope host lo
       valid_lft forever preferred_lft forever
    inet6 ::1/128 scope host 
       valid_lft forever preferred_lft forever
`, nil)
	itx := NewTransaction("lo")
	addrs, err := itx.GetAddresses()
	if err != nil {
		t.Fatalf("Failed to get addresses for 'lo': %v", err)
	}
	if len(addrs) != 1 {
		t.Fatalf("'lo' has unexpected addrs.len %d", len(addrs))
	}
	if addrs[0] != "127.0.0.1/8" {
		t.Fatalf("'lo' has unexpected address %s", addrs[0])
	}
	err = itx.EndTransaction()
	if err != nil {
		t.Fatalf("Transaction unexpectedly returned error: %v", err)
	}

	exec.AddTestResult("/sbin/ip addr show dev eth0", `2: eth0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc pfifo_fast state UP group default qlen 1000
    link/ether aa:bb:cc:dd:ee:ff brd ff:ff:ff:ff:ff:ff
    inet 192.168.1.10/24 brd 192.168.1.255 scope global dynamic eth0
       valid_lft 81296sec preferred_lft 81296sec
    inet 192.168.1.152/24 brd 192.168.1.255 scope global dynamic eth0
       valid_lft 81296sec preferred_lft 81296sec
`, nil)
	itx = NewTransaction("eth0")
	addrs, err = itx.GetAddresses()
	if err != nil {
		t.Fatalf("Failed to get addresses for 'eth0': %v", err)
	}
	if len(addrs) != 2 {
		t.Fatalf("'eth0' has unexpected addrs.len %d", len(addrs))
	}
	if addrs[0] != "192.168.1.10/24" || addrs[1] != "192.168.1.152/24" {
		t.Fatalf("'eth0' has unexpected addresses %v", addrs)
	}
	err = itx.EndTransaction()
	if err != nil {
		t.Fatalf("Transaction unexpectedly returned error: %v", err)
	}

	exec.AddTestResult("/sbin/ip addr show dev wlan0", "", fmt.Errorf("Device \"%s\" does not exist", "wlan0"))
	itx = NewTransaction("wlan0")
	addrs, err = itx.GetAddresses()
	if err == nil {
		t.Fatalf("Allegedly got addresses for non-existent link: %v", addrs)
	}
	err = itx.EndTransaction()
	if err == nil {
		t.Fatalf("Transaction unexpectedly returned no error")
	}
}

func TestErrorHandling(t *testing.T) {
	exec.AddTestResult("/sbin/ip link del dummy0", "", fmt.Errorf("Device \"%s\" does not exist", "dummy0"))
	itx := NewTransaction("dummy0")
	itx.DeleteLink()
	err := itx.EndTransaction()
	if err == nil {
		t.Fatalf("Failed to get expected error")
	}

	exec.AddTestResult("/sbin/ip link del dummy0", "", fmt.Errorf("Device \"%s\" does not exist", "dummy0"))
	exec.AddTestResult("/sbin/ip link add dummy0 type dummy", "", nil)
	itx = NewTransaction("dummy0")
	itx.DeleteLink()
	itx.IgnoreError()
	itx.AddLink("type", "dummy")
	err = itx.EndTransaction()
	if err != nil {
		t.Fatalf("Unexpectedly got error after IgnoreError(): %v", err)
	}

	exec.AddTestResult("/sbin/ip link add dummy0 type dummy", "", fmt.Errorf("RTNETLINK answers: Operation not permitted"))
	// other commands do not get run due to previous error
	itx = NewTransaction("dummy0")
	itx.AddLink("type", "dummy")
	itx.SetLink("up")
	itx.DeleteLink()
	err = itx.EndTransaction()
	if err == nil {
		t.Fatalf("Failed to get expected error")
	}
}
