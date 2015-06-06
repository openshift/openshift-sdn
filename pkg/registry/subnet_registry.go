package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strconv"

	"github.com/coreos/go-etcd/etcd"
	log "github.com/golang/glog"
	"github.com/openshift/openshift-sdn/pkg/api"
)

type EtcdSubnetRegistry struct {
	cli              *EtcdClient
	subnetPath       string
	subnetConfigPath string
	minionPath       string
}

func newMinionEvent(action, key, value string) *api.MinionEvent {
	min := &api.MinionEvent{}
	switch action {
	case "delete", "deleted", "expired":
		min.Type = api.Deleted
	default:
		min.Type = api.Added
	}

	if key != "" {
		_, min.Minion = path.Split(key)
		return min
	}

	fmt.Printf("Error decoding minion event: nil key (%s,%s,%s).\n", action, key, value)
	return nil
}

func newSubnetEvent(resp *etcd.Response) *api.SubnetEvent {
	var value string
	_, minkey := path.Split(resp.Node.Key)
	var t api.EventType
	switch resp.Action {
	case "deleted", "delete", "expired":
		t = api.Deleted
		value = resp.PrevNode.Value
	default:
		t = api.Added
		value = resp.Node.Value
	}
	var sub api.Subnet
	if err := json.Unmarshal([]byte(value), &sub); err == nil {
		return &api.SubnetEvent{
			Type:   t,
			Minion: minkey,
			Sub:    sub,
		}
	}
	log.Errorf("Failed to unmarshal response: %v", resp)
	return nil
}

func NewEtcdSubnetRegistry(config *api.EtcdConfig, subnetPath string, subnetConfigPath string, minionPath string) (api.SubnetRegistry, error) {
	r := &EtcdSubnetRegistry{
		subnetPath:       subnetPath,
		subnetConfigPath: subnetConfigPath,
		minionPath:       minionPath,
	}

	var err error
	r.cli, err = NewEtcdClient(config)
	if err != nil {
		return nil, err
	}

	return r, nil
}

func (sub *EtcdSubnetRegistry) InitSubnets() error {
	key := sub.subnetPath
	_, err := sub.cli.client().SetDir(key, 0)
	if err != nil {
		return err
	}
	key = sub.subnetConfigPath
	_, err = sub.cli.client().SetDir(key, 0)
	return err
}

func (sub *EtcdSubnetRegistry) InitMinions() error {
	key := sub.minionPath
	_, err := sub.cli.client().SetDir(key, 0)
	return err
}

func (sub *EtcdSubnetRegistry) GetMinions() (*[]string, error) {
	key := sub.minionPath
	resp, err := sub.cli.client().Get(key, false, true)
	if err != nil {
		return nil, err
	}

	if resp.Node.Dir == false {
		return nil, errors.New("Minion path is not a directory")
	}

	minions := make([]string, 0)

	for _, node := range resp.Node.Nodes {
		if node.Key == "" {
			log.Errorf("Error unmarshalling GetMinions response node %s", node.Key)
			continue
		}
		_, minion := path.Split(node.Key)
		minions = append(minions, minion)
	}
	return &minions, nil
}

func (sub *EtcdSubnetRegistry) GetSubnets() (*[]api.Subnet, error) {
	key := sub.subnetPath
	resp, err := sub.cli.client().Get(key, false, true)
	if err != nil {
		return nil, err
	}

	if resp.Node.Dir == false {
		return nil, errors.New("Subnet path is not a directory")
	}

	subnets := make([]api.Subnet, 0)

	for _, node := range resp.Node.Nodes {
		var s api.Subnet
		err := json.Unmarshal([]byte(node.Value), &s)
		if err != nil {
			log.Errorf("Error unmarshalling GetSubnets response for node %s: %s", node.Value, err.Error())
			continue
		}
		subnets = append(subnets, s)
	}
	return &subnets, err
}

func (sub *EtcdSubnetRegistry) GetSubnet(minionip string) (*api.Subnet, error) {
	key := path.Join(sub.subnetPath, minionip)
	resp, err := sub.cli.client().Get(key, false, false)
	if err == nil {
		log.Infof("Unmarshalling response: %s", resp.Node.Value)
		var sub api.Subnet
		if err = json.Unmarshal([]byte(resp.Node.Value), &sub); err == nil {
			return &sub, nil
		}
		return nil, err
	}
	return nil, err
}

func (sub *EtcdSubnetRegistry) DeleteSubnet(minion string) error {
	key := path.Join(sub.subnetPath, minion)
	_, err := sub.cli.client().Delete(key, false)
	return err
}

func (sub *EtcdSubnetRegistry) WriteNetworkConfig(network string, subnetLength uint) error {
	key := path.Join(sub.subnetConfigPath, "ContainerNetwork")
	_, err := sub.cli.client().Create(key, network, 0)
	if err != nil {
		log.Warningf("Found existing network configuration, overwriting it.")
		_, err = sub.cli.client().Update(key, network, 0)
		if err != nil {
			log.Errorf("Failed to write Network configuration to etcd: %v", err)
			return err
		}
	}

	key = path.Join(sub.subnetConfigPath, "SubnetLength")
	data := strconv.FormatUint(uint64(subnetLength), 10)
	_, err = sub.cli.client().Create(key, data, 0)
	if err != nil {
		_, err = sub.cli.client().Update(key, data, 0)
		if err != nil {
			log.Errorf("Failed to write Network configuration to etcd: %v", err)
			return err
		}
	}
	return nil
}

func (sub *EtcdSubnetRegistry) GetContainerNetwork() (string, error) {
	key := path.Join(sub.subnetConfigPath, "ContainerNetwork")
	resp, err := sub.cli.client().Get(key, false, false)
	if err != nil {
		return "", err
	}
	return resp.Node.Value, err
}

func (sub *EtcdSubnetRegistry) GetSubnetLength() (uint64, error) {
	key := path.Join(sub.subnetConfigPath, "SubnetLength")
	resp, err := sub.cli.client().Get(key, false, false)
	if err == nil {
		return strconv.ParseUint(resp.Node.Value, 10, 0)
	}
	return 0, err
}

func (sub *EtcdSubnetRegistry) CreateMinion(minion string, data string) error {
	key := path.Join(sub.minionPath, minion)
	_, err := sub.cli.client().Get(key, false, false)
	if err != nil {
		// good, it does not exist, write it
		_, err = sub.cli.client().Create(key, data, 0)
		if err != nil {
			log.Errorf("Failed to write new subnet to etcd: %v", err)
			return err
		}
	}

	return nil
}

func (sub *EtcdSubnetRegistry) CreateSubnet(minion string, subnet *api.Subnet) error {
	subbytes, _ := json.Marshal(subnet)
	data := string(subbytes)
	log.Infof("Minion subnet structure: %s", data)
	key := path.Join(sub.subnetPath, minion)
	_, err := sub.cli.client().Create(key, data, 0)
	if err != nil {
		_, err = sub.cli.client().Update(key, data, 0)
		if err != nil {
			log.Errorf("Failed to write new subnet to etcd: %v", err)
			return err
		}
	}

	return nil
}

func (sub *EtcdSubnetRegistry) WatchMinions(receiver chan *api.MinionEvent, stop chan bool) error {
	var rev uint64
	rev = 0
	key := sub.minionPath
	log.Infof("Watching %s for new minions.", key)
	for {
		resp, err := sub.cli.watch(key, rev, stop)
		if err != nil && err == etcd.ErrWatchStoppedByUser {
			log.Infof("New subnet event error: %v", err)
			return err
		}
		if resp == nil || err != nil {
			continue
		}
		rev = resp.Node.ModifiedIndex + 1
		log.Infof("Issuing a minion event: %v", resp)
		minevent := newMinionEvent(resp.Action, resp.Node.Key, resp.Node.Value)
		receiver <- minevent
	}
}

func (sub *EtcdSubnetRegistry) WatchSubnets(receiver chan *api.SubnetEvent, stop chan bool) error {
	for {
		var rev uint64
		rev = 0
		key := sub.subnetPath
		resp, err := sub.cli.watch(key, rev, stop)
		if resp == nil && err == nil {
			continue
		}
		rev = resp.Node.ModifiedIndex + 1
		if err != nil && err == etcd.ErrWatchStoppedByUser {
			log.Infof("New subnet event error: %v", err)
			return err
		}
		subevent := newSubnetEvent(resp)
		log.Infof("New subnet event: %v, %v", subevent, resp)
		receiver <- subevent
	}
}

func (sub *EtcdSubnetRegistry) CheckEtcdIsAlive(seconds uint64) bool {
	return sub.cli.CheckEtcdIsAlive(seconds)
}
