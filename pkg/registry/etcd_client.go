package registry

import (
	"fmt"
	"path"
	"sync"
	"time"

	"github.com/coreos/go-etcd/etcd"
	log "github.com/golang/glog"
	"github.com/openshift/openshift-sdn/pkg/api"
)

type EtcdClient struct {
	mux sync.Mutex
	cli *etcd.Client
	cfg *api.EtcdConfig
}

func newEtcdClient(cfg *api.EtcdConfig) (*etcd.Client, error) {
	if cfg.Keyfile != "" || cfg.Certfile != "" || cfg.CAFile != "" {
		return etcd.NewTLSClient(cfg.Endpoints, cfg.Certfile, cfg.Keyfile, cfg.CAFile)
	} else {
		return etcd.NewClient(cfg.Endpoints), nil
	}
}

func NewEtcdClient(cfg *api.EtcdConfig) (*EtcdClient, error) {
	cli, err := newEtcdClient(cfg)
	if err != nil {
		return nil, err
	}

	return &EtcdClient{
		cli: cli,
		cfg: cfg,
	}, nil
}

func (self *EtcdClient) CheckEtcdIsAlive(seconds uint64) bool {
	for {
		status := self.client().SyncCluster()
		log.Infof("Etcd cluster status: %v", status)
		if status {
			return status
		}
		if seconds <= 0 {
			break
		}
		time.Sleep(5 * time.Second)
		seconds -= 5
	}
	return false
}

func (self *EtcdClient) watch(key string, rev uint64, stop chan bool) (*etcd.Response, error) {
	rawResp, err := self.client().RawWatch(key, rev, true, nil, stop)

	if err != nil {
		if err == etcd.ErrWatchStoppedByUser {
			return nil, err
		} else {
			log.Warningf("Temporary error while watching %s: %v\n", key, err)
			time.Sleep(time.Second)
			self.resetClient()
			return nil, nil
		}
	}

	if len(rawResp.Body) == 0 {
		// etcd timed out, go back but recreate the client as the underlying
		// http transport gets hosed (http://code.google.com/p/go/issues/detail?id=8648)
		self.resetClient()
		return nil, nil
	}

	return rawResp.Unmarshal()
}

func (self *EtcdClient) client() *etcd.Client {
	self.mux.Lock()
	defer self.mux.Unlock()
	return self.cli
}

func (self *EtcdClient) resetClient() {
	self.mux.Lock()
	defer self.mux.Unlock()

	var err error
	self.cli, err = newEtcdClient(self.cfg)
	if err != nil {
		panic(fmt.Errorf("resetClient: error recreating etcd client: %v", err))
	}
}

func (self *EtcdClient) getKeyPath(key string) string {
	return path.Join(self.cfg.Path, key)
}
