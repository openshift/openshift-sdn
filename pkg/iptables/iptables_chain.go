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
	"fmt"
	"strings"
)

// Chain defines the iptables chain.
type Chain struct {
	Name     string
	Table    Table
	IPTables *IPTables
}

func (c *Chain) GetRules() (string, error) {
	rule := []string{"-t", string(c.Table), "-S", c.Name}
	b, err := c.IPTables.Raw(rule...)
	return string(b), err
}

func (c *Chain) RuleExists(rule ...string) (bool, error) {
	if c.IPTables.supportsCheck {
		rule = append([]string{"-t", string(c.Table), "-C", c.Name}, rule...)
		if _, err := c.IPTables.Raw(rule...); err != nil {
			return false, err
		}
		return true, nil
	}

	existingRules, _ := c.GetRules()
	ruleString := strings.Join(rule, " ")

	return strings.Contains(string(existingRules), ruleString), nil
}

func (c *Chain) Flush() error {
	// Flush
	if _, err := c.IPTables.Raw("-t", string(c.Table), "-F", c.Name); err != nil {
		return err
	}
	return nil
}

// Will attempt to remove a given chain. Will fail if it has any rules jumping
// to this chain!
func (c *Chain) Remove() error {
	if err := c.Flush(); err != nil {
		return err
	}
	// Delete
	if output, err := c.IPTables.Raw("-t", string(c.Table), "-X", c.Name); err != nil {
		if len(output) > 0 {
			return fmt.Errorf("Could not delete chain %s/%s: %s", c.Table, c.Name, output)
		}
		return err
	}
	return nil
}

func (c *Chain) Exists() (bool, error) {
	b, err := c.IPTables.Raw("-t", string(c.Table), "-n", "-L", c.Name)
	output := string(b)
	if err == nil {
		return true, nil
	} else if len(output) > 0 && strings.Contains(output, "No chain/target/match by that name") {
		return false, nil
	}
	return false, err
}

func (c *Chain) AddRule(action Action, rule ...string) error {
	r := append([]string{"-t", string(c.Table), string(action), c.Name}, rule...)
	_, err := c.IPTables.Raw(r...)
	return err
}

func (ipt *IPTables) GetChain(table Table, name string) *Chain {
	c := &Chain{
		Name:     name,
		Table:    table,
		IPTables: ipt,
	}
	if string(c.Table) == "" {
		c.Table = Filter
	}
	return c
}

func (c *Chain) CreateOrFlush() error {
	// If it does exist, flush it
	if b, err := c.Exists(); b == true {
		return c.Flush()
	} else if err != nil {
		return err
	}

	// Add chain
	if output, err := c.IPTables.Raw("-t", string(c.Table), "-N", c.Name); err != nil {
		return err
	} else if len(output) != 0 {
		return fmt.Errorf("Could not create %s/%s chain: %s", c.Table, c.Name, output)
	}

	return nil
}
