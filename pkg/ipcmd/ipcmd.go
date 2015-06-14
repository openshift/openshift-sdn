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

package ipcmd

import (
	"fmt"
	"os/exec"
	"strings"
)

type Object string
type Action string

const (
	Link  Object = "link"
	Addr  Object = "addr"
	Route Object = "route"

	Add Action = "add"
	Del Action = "del"
	Set Action = "set"
)

type IPCmd struct {
	path string
}

func NewIPCmd() (*IPCmd, error) {
	ipcmd := new(IPCmd)

	if path, err := exec.LookPath("ip"); err != nil {
		return nil, fmt.Errorf("unable to find %s executable", "ip")
	} else {
		ipcmd.path = path
	}
	return ipcmd, nil
}

func (cmd *IPCmd) Execute(object Object, action Action, args ...string) error {
	args = append([]string{string(object), string(action)}, args...)
	output, err := exec.Command(cmd.path, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s failed: %v: %s (%s)", cmd, strings.Join(args, " "), output, err)
	}
	return nil
}
