package osdn

import (
	"fmt"
	"strings"
	"time"

	log "github.com/golang/glog"

	kapi "k8s.io/kubernetes/pkg/api"
	kubetypes "k8s.io/kubernetes/pkg/kubelet/container"
	kerrors "k8s.io/kubernetes/pkg/util/errors"
	utilruntime "k8s.io/kubernetes/pkg/util/runtime"
	"k8s.io/kubernetes/pkg/util/sets"
	utilwait "k8s.io/kubernetes/pkg/util/wait"
	"k8s.io/kubernetes/pkg/watch"

	"github.com/openshift/openshift-sdn/pkg/netid"
)

func (oc *OsdnController) GetVNID(name string) (uint, error) {
	oc.vnidLock.Lock()
	defer oc.vnidLock.Unlock()

	if id, ok := oc.vnidMap[name]; ok {
		return id, nil
	}
	// In case of error, return some value which is not a valid VNID
	return netid.MaxVNID + 1, fmt.Errorf("Failed to find netid for namespace: %s in vnid map", name)
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

	// In case of error, return some value which is not a valid VNID
	return netid.MaxVNID + 1, fmt.Errorf("Failed to find netid for namespace: %s in vnid map", name)
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
		// In case of error, return some value which is not a valid VNID
		return netid.MaxVNID + 1, fmt.Errorf("Failed to find netid for namespace: %s in vnid map", name)
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
