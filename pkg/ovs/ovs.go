package ovs

import (
	"fmt"
	"strings"

	"github.com/openshift/openshift-sdn/pkg/exec"
)

var vsctlPath, ofctlPath string

func init() {
	var err error

	vsctlPath, err = exec.LookPath("ovs-vsctl")
	if err != nil {
		panic("ovs is not installed")
	}
	ofctlPath, err = exec.LookPath("ovs-ofctl")
	if err != nil {
		panic("ovs is not installed")
	}
}

type Transaction struct {
	bridge string
	err    error
}

func NewTransaction(bridge string) *Transaction {
	return &Transaction{bridge: bridge}
}

func (tx *Transaction) exec(cmd string, args ...string) (string, error) {
	if tx.err != nil {
		return "", tx.err
	}

	var output string
	output, tx.err = exec.Exec(cmd, args...)
	return output, tx.err
}

func (tx *Transaction) vsctlExec(args ...string) (string, error) {
	args = append([]string{"-O", "OpenFlow13"}, args...)
	return tx.exec(vsctlPath, args...)
}

func (tx *Transaction) ofctlExec(args ...string) (string, error) {
	return tx.exec(ofctlPath, args...)
}

func (tx *Transaction) AddBridge(properties ...string) {
	args := []string{"--if-exists", "del-br", tx.bridge, "--", "add-br", tx.bridge}
	if len(properties) > 0 {
		args = append(args, "--", "set", "Bridge", tx.bridge)
		args = append(args, properties...)
	}
	tx.vsctlExec(args...)
}

func (tx *Transaction) DeleteBridge() {
	tx.vsctlExec("del-br", tx.bridge)
}

func (tx *Transaction) AddPort(port string, ofport uint, properties ...string) {
	args := []string{"--if-exists", "del-port", port, "--", "add-port", tx.bridge, port, "--", "set", "Interface", port, fmt.Sprintf("ofport_request=%d", ofport)}
	if len(properties) > 0 {
		args = append(args, properties...)
	}
	tx.vsctlExec(args...)
}

func (tx *Transaction) DeletePort(port string) {
	tx.vsctlExec("del-port", port)
}

func (tx *Transaction) AddFlow(flow string, args ...interface{}) {
	if len(args) > 0 {
		flow = fmt.Sprintf(flow, args...)
	}
	tx.ofctlExec("add-flow", tx.bridge, flow)
}

func (tx *Transaction) DeleteFlows(flow string, args ...interface{}) {
	if len(args) > 0 {
		flow = fmt.Sprintf(flow, args...)
	}
	tx.ofctlExec("del-flows", tx.bridge, flow)
}

func (tx *Transaction) DumpFlows() ([]string, error) {
	out, err := tx.ofctlExec("dump-flows", tx.bridge)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(out, "\n")
	flows := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.Contains(line, "cookie=") {
			flows = append(flows, line)
		}
	}
	return flows, nil
}

func (tx *Transaction) EndTransaction() error {
	err := tx.err
	tx.err = nil
	return err
}
