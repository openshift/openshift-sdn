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

package brctl

import (
	"fmt"
	"os/exec"
	"strings"
)

type Action string

const (
	DelBr Action = "delbr"
	AddBr Action = "addbr"
	AddIf Action = "addif"
)

type Brctl struct {
	path string
}

func NewBrctl() (*Brctl, error) {
	brctl := new(Brctl)

	if path, err := exec.LookPath("brctl"); err != nil {
		return nil, fmt.Errorf("unable to find %s executable", "ip")
	} else {
		brctl.path = path
	}
	return brctl, nil
}

func (cmd *Brctl) Execute(action Action, args ...string) error {
	args = append([]string{string(action)}, args...)
	output, err := exec.Command(cmd.path, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s failed: %v: %s (%s)", cmd, strings.Join(args, " "), output, err)
	}
	return nil
}
