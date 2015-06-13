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

package iptables

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"

	"github.com/golang/glog"
)

// Action signifies the iptable action.
type Action string

// Table refers to Nat, Filter or Mangle.
type Table string

const (
	// Append appends the rule at the end of the chain.
	Append Action = "-A"
	// Delete deletes the rule from the chain.
	Delete Action = "-D"
	// Insert inserts the rule at the top of the chain.
	Insert Action = "-I"

	// Nat table is used for nat translation rules.
	Nat Table = "nat"
	// Filter table is used for filter rules.
	Filter Table = "filter"
	// Mangle table is used for mangling the packet.
	Mangle Table = "mangle"
)

// ErrIptablesNotFound is returned when the rule is not found.
var ErrIptablesNotFound = errors.New("Iptables not found")

type IPTables struct {
	supportsXlock bool
	supportsCheck bool
	// used to lock iptables commands if xtables lock is not supported
	bestEffortLock sync.Mutex
	path           string
}

func NewIPTables() (*IPTables, error) {
	iptables := new(IPTables)

	if path, err := exec.LookPath("iptables"); err != nil {
		return nil, ErrIptablesNotFound
	} else {
		iptables.path = path
	}

	iptables.supportsXlock = (exec.Command(iptables.path, "--wait", "-L", "-n").Run() == nil)

	if supportsCheck, err := getIptablesHasCheckCommand(iptables.path); err != nil {
		glog.Warningf("Error checking iptables version, assuming version less than 1.4.11: %v\n", err)
		iptables.supportsCheck = false
	} else {
		iptables.supportsCheck = supportsCheck
	}
	return iptables, nil
}

func (iptables *IPTables) GetRules(table Table, chain string) (string, error) {
	c := iptables.GetChain(table, chain)
	return c.GetRules()
}

func (iptables *IPTables) RuleExists(table Table, chain string, rule ...string) (bool, error) {
	c := iptables.GetChain(table, chain)
	return c.RuleExists(rule...)
}

func (iptables *IPTables) AddRule(table Table, chain string, action Action, rule ...string) error {
	c := iptables.GetChain(table, chain)
	return c.AddRule(action, rule...)
}

// Raw calls 'iptables' system command, passing supplied arguments.
func (iptables *IPTables) Raw(args ...string) ([]byte, error) {
	/*
		if firewalldRunning {
			output, err := Passthrough(Iptables, args...)
			if err == nil || !strings.Contains(err.Error(), "was not provided by any .service files") {
				return output, err
			}

		}
	*/
	if iptables.supportsXlock {
		args = append([]string{"--wait"}, args...)
	} else {
		iptables.bestEffortLock.Lock()
		defer iptables.bestEffortLock.Unlock()
	}

	output, err := exec.Command(iptables.path, args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("iptables failed: iptables %v: %s (%s)", strings.Join(args, " "), output, err)
	}

	// ignore iptables' message about xtables lock
	if strings.Contains(string(output), "waiting for it to exit") {
		output = []byte("")
	}

	return output, err
}
