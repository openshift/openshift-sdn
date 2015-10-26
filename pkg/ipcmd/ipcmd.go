package ipcmd

import (
	"regexp"

	"github.com/openshift/openshift-sdn/pkg/exec"
)

var ipcmdPath string

func init() {
	var err error

	ipcmdPath, err = exec.LookPath("ip")
	if err != nil {
		panic("ip is not installed")
	}
}

type Transaction struct {
	link string
	err  error
}

func NewTransaction(link string) *Transaction {
	return &Transaction{link: link}
}

func (tx *Transaction) exec(args []string) (string, error) {
	if tx.err != nil {
		return "", tx.err
	}

	var output string
	output, tx.err = exec.Exec(ipcmdPath, args...)
	return output, tx.err
}

func (tx *Transaction) AddLink(args ...string) {
	tx.exec(append([]string{"link", "add", tx.link}, args...))
}

func (tx *Transaction) DeleteLink() {
	tx.exec([]string{"link", "del", tx.link})
}

func (tx *Transaction) SetLink(args ...string) {
	tx.exec(append([]string{"link", "set", tx.link}, args...))
}

func (tx *Transaction) AddAddress(cidr string, args ...string) {
	tx.exec(append([]string{"addr", "add", cidr, "dev", tx.link}, args...))
}

func (tx *Transaction) DeleteAddress(cidr string, args ...string) {
	tx.exec(append([]string{"addr", "del", cidr, "dev", tx.link}, args...))
}

func (tx *Transaction) AddRoute(cidr string, args ...string) {
	tx.exec(append([]string{"route", "add", cidr, "dev", tx.link}, args...))
}

func (tx *Transaction) DeleteRoute(cidr string, args ...string) {
	tx.exec(append([]string{"route", "del", cidr, "dev", tx.link}, args...))
}

func (tx *Transaction) AddSlave(slave string) {
	tx.exec([]string{"link", "set", slave, "master", tx.link})
}

func (tx *Transaction) DeleteSlave(slave string) {
	tx.exec([]string{"link", "set", slave, "nomaster"})
}

func (tx *Transaction) IgnoreError() {
	tx.err = nil
}

func (tx *Transaction) GetAddresses() ([]string, error) {
	out, err := tx.exec(append([]string{"addr", "show", "dev", tx.link}))
	if err != nil {
		return nil, err
	}

	re := regexp.MustCompile("inet ([0-9.]*/[0-9]*) ")
	matches := re.FindAllStringSubmatch(out, -1)
	addrs := make([]string, len(matches))
	for i, match := range matches {
		addrs[i] = match[1]
	}
	return addrs, nil
}

func (tx *Transaction) EndTransaction() error {
	err := tx.err
	tx.err = nil
	return err
}
