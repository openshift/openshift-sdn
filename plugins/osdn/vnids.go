package osdn

import (
	"fmt"
	"strings"
	"time"

	log "github.com/golang/glog"

	kapi "k8s.io/kubernetes/pkg/api"
	kubetypes "k8s.io/kubernetes/pkg/kubelet/container"
	"k8s.io/kubernetes/pkg/registry/service/allocator"
	etcdallocator "k8s.io/kubernetes/pkg/registry/service/allocator/etcd"
	"k8s.io/kubernetes/pkg/util"
	kerrors "k8s.io/kubernetes/pkg/util/errors"
	utilruntime "k8s.io/kubernetes/pkg/util/runtime"
	utilwait "k8s.io/kubernetes/pkg/util/wait"
	"k8s.io/kubernetes/pkg/watch"

	"github.com/openshift/openshift-sdn/pkg/netid"
	vnidcontroller "github.com/openshift/openshift-sdn/plugins/osdn/netid/controller"
	"github.com/openshift/openshift-sdn/plugins/osdn/netid/vnid"
	"github.com/openshift/openshift-sdn/plugins/osdn/netid/vnidallocator"
	"github.com/openshift/origin/pkg/sdn/registry/netnamespace"
)

func (oc *OsdnController) VnidStartMaster() error {
	netIDRange, err := vnid.NewVNIDRange(netid.MinVNID, netid.MaxVNID-netid.MinVNID+1)
	if err != nil {
		return fmt.Errorf("Unable to create NetID range: %v", err)
	}

	var etcdAlloc *etcdallocator.Etcd
	netIDAllocator := vnidallocator.New(netIDRange, func(max int, rangeSpec string) allocator.Interface {
		mem := allocator.NewContiguousAllocationMap(max, rangeSpec)
		etcdAlloc = etcdallocator.NewEtcd(mem, "/ranges/namespacevnids", kapi.Resource("namespacevnidallocation"), oc.EtcdHelper)
		return etcdAlloc
	})

	_, kclient := oc.Registry.GetSDNClients()

	// Run vnid migration
	migrate := netnamespace.NewMigrate(oc.EtcdHelper, kclient.Namespaces())
	if err := migrate.Run(); err != nil {
		return fmt.Errorf("Unable to migrate network namespaces: %v, please retry", err)
	}

	// Run vnid repair controller
	repair := vnidcontroller.NewRepair(15*time.Minute, kclient.Namespaces(), netIDRange, etcdAlloc)
	if err := repair.RunOnce(); err != nil {
		return fmt.Errorf("Unable to initialize net ID allocation for all namespaces: %v", err)
	}
	runner := util.NewRunner(repair.RunUntil)
	runner.Start()

	// Run vnid controller
	factory := vnidcontroller.NewVNIDController(netIDAllocator, kclient.Namespaces(), []string{kapi.NamespaceDefault})
	controller := factory.Create()
	controller.Run()

	return nil
}

func (oc *OsdnController) GetVNID(name string) (uint, error) {
	oc.vnidLock.Lock()
	defer oc.vnidLock.Unlock()

	if id, ok := oc.vnidMap[name]; ok {
		return id, nil
	}
	return 0, fmt.Errorf("Failed to find netid for namespace: %s in vnid map", name)
}

// Nodes asynchronously watch for both NetNamespaces and services
// NetNamespaces populates vnid map and services/pod-setup depend on vnid map
// If for some reason, vnid map propagation from master to node is slow
// and if service/pod-setup tries to lookup vnid map then it may fail.
// So, use this method to alleviate this problem. This method will
// retry vnid lookup before giving up.
func (oc *OsdnController) WaitAndGetVNID(name string) (uint, error) {
	// Try few times up to 2 seconds
	retries := 20
	retryInterval := 100 * time.Millisecond
	for i := 0; i < retries; i++ {
		if id, err := oc.GetVNID(name); err == nil {
			return id, nil
		}
		time.Sleep(retryInterval)
	}

	return 0, fmt.Errorf("Failed to find netid for namespace: %s in vnid map", name)
}

func (oc *OsdnController) setVNID(name string, id uint) {
	oc.vnidLock.Lock()
	defer oc.vnidLock.Unlock()

	oc.vnidMap[name] = id
	log.Infof("Associate netid %d to namespace %q", id, name)
}

func (oc *OsdnController) unSetVNID(name string) (id uint, err error) {
	oc.vnidLock.Lock()
	defer oc.vnidLock.Unlock()

	id, found := oc.vnidMap[name]
	if !found {
		return 0, fmt.Errorf("Failed to find netid for namespace: %s in vnid map", name)
	}
	delete(oc.vnidMap, name)
	log.Infof("Dissociate netid %d from namespace %q", id, name)
	return id, nil
}

func populateVNIDMap(oc *OsdnController) error {
	nsList, err := oc.Registry.GetNamespaces()
	if err != nil {
		return err
	}

	for _, ns := range nsList {
		id, err := netid.GetVNID(&ns)
		if err == netid.ErrorVNIDNotFound {
			continue
		} else if err != nil {
			log.Errorf("Invalid netid: %v, ignoring namespace %q", err, ns.ObjectMeta.Name)
		} else {
			oc.setVNID(ns.ObjectMeta.Name, id)
		}
	}
	return nil
}

func (oc *OsdnController) VnidStartNode() error {
	// Populate vnid map synchronously so that existing services can fetch vnid
	err := populateVNIDMap(oc)
	if err != nil {
		return err
	}

	go utilwait.Forever(oc.watchNamespaces, 0)
	go utilwait.Forever(oc.watchServices, 0)
	return nil
}

func (oc *OsdnController) updatePodNetwork(namespace string, netID uint) error {
	// Update OF rules for the existing/old pods in the namespace
	pods, err := oc.GetLocalPods(namespace)
	if err != nil {
		return err
	}
	for _, pod := range pods {
		err := oc.pluginHooks.UpdatePod(pod.Namespace, pod.Name, kubetypes.DockerID(GetPodContainerID(&pod)))
		if err != nil {
			return err
		}
	}

	// Update OF rules for the old services in the namespace
	services, err := oc.Registry.GetServicesForNamespace(namespace)
	if err != nil {
		return err
	}
	errList := []error{}
	for _, svc := range services {
		if err := oc.pluginHooks.DeleteServiceRules(&svc); err != nil {
			log.Error(err)
		}
		if err := oc.pluginHooks.AddServiceRules(&svc, netID); err != nil {
			errList = append(errList, err)
		}
	}
	return kerrors.NewAggregate(errList)
}

func (oc *OsdnController) watchNamespaces() {
	eventQueue := oc.Registry.RunEventQueue(Namespaces)

	for {
		eventType, obj, err := eventQueue.Pop()
		if err != nil {
			utilruntime.HandleError(fmt.Errorf("EventQueue failed for namespaces: %v", err))
			return
		}
		ns := obj.(*kapi.Namespace)
		name := ns.ObjectMeta.Name

		log.V(5).Infof("Watch %s event for Namespace %q", strings.Title(string(eventType)), name)
		switch eventType {
		case watch.Added, watch.Modified:
			netID, err := netid.GetVNID(ns)
			if err != nil {
				// VNID may not be assigned by master yet
				continue
			}

			// Skip this event if the old and new network ids are same
			oldNetID, err := oc.GetVNID(name)
			if (err == nil) && (oldNetID == netID) {
				continue
			}
			oc.setVNID(name, netID)

			err = oc.updatePodNetwork(name, netID)
			if err != nil {
				log.Errorf("Failed to update pod network for namespace '%s', error: %v", name, err)
				oc.setVNID(name, oldNetID)
				continue
			}
		case watch.Deleted:
			// updatePodNetwork needs netid, so unset netid after this call
			err := oc.updatePodNetwork(name, netid.GlobalVNID)
			if err != nil {
				log.Errorf("Failed to update pod network for namespace '%s', error: %v", name, err)
			}
			oc.unSetVNID(name)
		}
	}
}

func isServiceChanged(oldsvc, newsvc *kapi.Service) bool {
	if len(oldsvc.Spec.Ports) == len(newsvc.Spec.Ports) {
		for i := range oldsvc.Spec.Ports {
			if oldsvc.Spec.Ports[i].Protocol != newsvc.Spec.Ports[i].Protocol ||
				oldsvc.Spec.Ports[i].Port != newsvc.Spec.Ports[i].Port {
				return true
			}
		}
		return false
	}
	return true
}

func (oc *OsdnController) watchServices() {
	services := make(map[string]*kapi.Service)
	eventQueue := oc.Registry.RunEventQueue(Services)

	for {
		eventType, obj, err := eventQueue.Pop()
		if err != nil {
			utilruntime.HandleError(fmt.Errorf("EventQueue failed for services: %v", err))
			return
		}
		serv := obj.(*kapi.Service)

		// Ignore headless services
		if !kapi.IsServiceIPSet(serv) {
			continue
		}

		log.V(5).Infof("Watch %s event for Service %q", strings.Title(string(eventType)), serv.ObjectMeta.Name)
		switch eventType {
		case watch.Added, watch.Modified:
			oldsvc, exists := services[string(serv.UID)]
			if exists {
				if !isServiceChanged(oldsvc, serv) {
					continue
				}
				if err := oc.pluginHooks.DeleteServiceRules(oldsvc); err != nil {
					log.Error(err)
				}
			}

			netid, err := oc.WaitAndGetVNID(serv.Namespace)
			if err != nil {
				log.Errorf("Skipped adding service rules for serviceEvent: %v, Error: %v", eventType, err)
				continue
			}

			if err := oc.pluginHooks.AddServiceRules(serv, netid); err != nil {
				log.Error(err)
				continue
			}
			services[string(serv.UID)] = serv
		case watch.Deleted:
			delete(services, string(serv.UID))

			if err := oc.pluginHooks.DeleteServiceRules(serv); err != nil {
				log.Error(err)
			}
		}
	}
}
