// Copyright 2015 Eric Paris
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ovs

import (
	"fmt"
	"os/exec"
	"strings"
)

// Action signifies the iptable action.
type Action string

const (
	// ovs-vsctl
	AddBridge Action = "add-bridge"
	DelBridge Action = "del-bridge"
	Set       Action = "set"
	AddPort   Action = "add-port"
	DelPort   Action = "del-port"

	// ovs-ofctl
	AddFlow  Action = "add-flow"
	DelFlows Action = "del-flows"
)

type OVS struct {
	vsctl string
	ofctl string
}

func NewOVS() (*OVS, error) {
	ovs := new(OVS)

	if path, err := exec.LookPath("ovs-vsctl"); err != nil {
		return nil, fmt.Errorf("unable to find %s executable", "ovs-vsctl")
	} else {
		ovs.vsctl = path
	}

	if path, err := exec.LookPath("ovs-ofctl"); err != nil {
		return nil, fmt.Errorf("unable to find %s executable", "ovs-ofctl")
	} else {
		ovs.ofctl = path
	}
	return ovs, nil
}

func (ovs *OVS) execute(cmd string, args ...string) error {
	output, err := exec.Command(cmd, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s failed: %v: %s (%s)", cmd, strings.Join(args, " "), output, err)
	}
	return nil
}

func (ovs *OVS) vsctlExecute(action Action, args ...string) error {
	args = append([]string{"-O", "OpenFlow13", string(action)}, args...)
	return ovs.execute(ovs.vsctl, args...)
}

func (ovs *OVS) ofctlExecute(action Action, args ...string) error {
	args = append([]string{string(action)}, args...)
	return ovs.execute(ovs.ofctl, args...)
}

func (ovs *OVS) Execute(action Action, args ...string) error {
	switch action {
	case AddBridge:
		fallthrough
	case DelBridge:
		fallthrough
	case Set:
		fallthrough
	case AddPort:
		fallthrough
	case DelPort:
		return ovs.vsctlExecute(action, args...)
	case AddFlow:
		fallthrough
	case DelFlows:
		return ovs.ofctlExecute(action, args...)
	default:
		return fmt.Errorf("Unknown action %s\n", string(action))
	}
}
