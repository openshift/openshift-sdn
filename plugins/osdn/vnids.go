package osdn

import (
	"fmt"

	log "github.com/golang/glog"

	"github.com/openshift/openshift-sdn/plugins/osdn/api"
	"github.com/openshift/origin/pkg/sdn/registry/netnamespace/vnid"
	kubetypes "k8s.io/kubernetes/pkg/kubelet/types"
)

func (oc *OvsController) VnidStartMaster() error {
	getNamespaces := func(registry *Registry) (interface{}, string, error) {
		return registry.GetNamespaces()
	}
	result, err := oc.watchAndGetResource("Namespace", watchNamespaces, getNamespaces)
	if err != nil {
		return err
	}
	namespaces := result.([]string)

	// Handle existing namespaces without corresponding netnamespaces
	netnsList, _, err := oc.Registry.GetNetNamespaces()
	if err != nil {
		return err
	}
	netNamespaceMap := make(map[string]bool, len(netnsList))
	for _, netns := range netnsList {
		netNamespaceMap[netns.Name] = true
	}

	for _, nsName := range namespaces {
		if !netNamespaceMap[nsName] {
			err = oc.Registry.CreateNetNamespace(nsName)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func watchNamespaces(oc *OvsController, ready chan<- bool, start <-chan string) {
	nsevent := make(chan *api.NamespaceEvent)
	stop := make(chan bool)
	go oc.Registry.WatchNamespaces(nsevent, ready, start, stop)
	for {
		select {
		case ev := <-nsevent:
			switch ev.Type {
			case api.Added:
				err := oc.Registry.CreateNetNamespace(ev.Name)
				if err != nil {
					log.Errorf("Error creating NetNamespace: %v", err)
					continue
				}
			case api.Deleted:
				err := oc.Registry.DeleteNetNamespace(ev.Name)
				if err != nil {
					log.Errorf("Error deleting NetNamespace: %v", err)
					continue
				}
			}
		case <-oc.sig:
			log.Error("Signal received. Stopping watching of nodes.")
			stop <- true
			return
		}
	}
}

func (oc *OvsController) VnidStartNode() error {
	getNetNamespaces := func(registry *Registry) (interface{}, string, error) {
		return registry.GetNetNamespaces()
	}
	result, err := oc.watchAndGetResource("NetNamespace", watchNetNamespaces, getNetNamespaces)
	if err != nil {
		return err
	}
	nslist := result.([]api.NetNamespace)
	for _, ns := range nslist {
		oc.VNIDMap[ns.Name] = ns.NetID
	}

	getServices := func(registry *Registry) (interface{}, string, error) {
		return registry.GetServices()
	}
	result, err = oc.watchAndGetResource("Service", watchServices, getServices)
	if err != nil {
		return err
	}

	services := result.([]api.Service)
	for _, svc := range services {
		netid, found := oc.VNIDMap[svc.Namespace]
		if !found {
			return fmt.Errorf("Error fetching Net ID for namespace: %s", svc.Namespace)
		}
		oc.services[svc.UID] = svc
		for _, port := range svc.Ports {
			oc.flowController.AddServiceOFRules(netid, svc.IP, port.Protocol, port.Port)
		}
	}

	getPods := func(registry *Registry) (interface{}, string, error) {
		return registry.GetPods()
	}
	_, err = oc.watchAndGetResource("Pod", watchPods, getPods)
	if err != nil {
		return err
	}

	return nil
}

func (oc *OvsController) updatePodNetwork(namespace string, netID, oldNetID uint) error {
	// Update OF rules for the existing/old pods in the namespace
	pods, err := oc.Registry.GetRunningPods(oc.hostName, namespace)
	if err != nil {
		return err
	}
	for _, pod := range pods {
		err := oc.pluginHooks.UpdatePod(pod.Namespace, pod.Name, kubetypes.DockerID(pod.ContainerID))
		if err != nil {
			return err
		}
	}

	// Update OF rules for the old services in the namespace
	services, err := oc.Registry.GetServicesForNamespace(namespace)
	if err != nil {
		return err
	}
	for _, svc := range services {
		for _, port := range svc.Ports {
			oc.flowController.DelServiceOFRules(oldNetID, svc.IP, port.Protocol, port.Port)
			oc.flowController.AddServiceOFRules(netID, svc.IP, port.Protocol, port.Port)
		}
	}
	return nil
}

func watchNetNamespaces(oc *OvsController, ready chan<- bool, start <-chan string) {
	stop := make(chan bool)
	netNsEvent := make(chan *api.NetNamespaceEvent)
	go oc.Registry.WatchNetNamespaces(netNsEvent, ready, start, stop)
	for {
		select {
		case ev := <-netNsEvent:
			oldNetID, found := oc.VNIDMap[ev.Name]
			if !found {
				log.Errorf("Error fetching Net ID for namespace: %s, skipped netNsEvent: %v", ev.Name, ev)
			}
			switch ev.Type {
			case api.Added:
				// Skip this event if the old and new network ids are same
				if oldNetID == ev.NetID {
					continue
				}
				oc.VNIDMap[ev.Name] = ev.NetID
				err := oc.updatePodNetwork(ev.Name, ev.NetID, oldNetID)
				if err != nil {
					log.Errorf("Failed to update pod network for namespace '%s', error: %s", ev.Name, err)
				}
			case api.Deleted:
				err := oc.updatePodNetwork(ev.Name, vnid.GlobalVNID, oldNetID)
				if err != nil {
					log.Errorf("Failed to update pod network for namespace '%s', error: %s", ev.Name, err)
				}
				delete(oc.VNIDMap, ev.Name)
			}
		case <-oc.sig:
			log.Error("Signal received. Stopping watching of NetNamespaces.")
			stop <- true
			return
		}
	}
}

func watchServices(oc *OvsController, ready chan<- bool, start <-chan string) {
	stop := make(chan bool)
	svcevent := make(chan *api.ServiceEvent)
	go oc.Registry.WatchServices(svcevent, ready, start, stop)
	for {
		select {
		case ev := <-svcevent:
			netid, found := oc.VNIDMap[ev.Service.Namespace]
			if !found {
				log.Errorf("Error fetching Net ID for namespace: %s, skipped serviceEvent: %v", ev.Service.Namespace, ev)
			}
			switch ev.Type {
			case api.Added:
				oc.services[ev.Service.UID] = ev.Service
				for _, port := range ev.Service.Ports {
					oc.flowController.AddServiceOFRules(netid, ev.Service.IP, port.Protocol, port.Port)
				}
			case api.Deleted:
				delete(oc.services, ev.Service.UID)
				for _, port := range ev.Service.Ports {
					oc.flowController.DelServiceOFRules(netid, ev.Service.IP, port.Protocol, port.Port)
				}
			case api.Modified:
				oldsvc, exists := oc.services[ev.Service.UID]
				if exists && len(oldsvc.Ports) == len(ev.Service.Ports) {
					same := true
					for i := range oldsvc.Ports {
						if oldsvc.Ports[i].Protocol != ev.Service.Ports[i].Protocol || oldsvc.Ports[i].Port != ev.Service.Ports[i].Port {
							same = false
							break
						}
					}
					if same {
						continue
					}
				}
				if exists {
					for _, port := range oldsvc.Ports {
						oc.flowController.DelServiceOFRules(netid, oldsvc.IP, port.Protocol, port.Port)
					}
				}
				oc.services[ev.Service.UID] = ev.Service
				for _, port := range ev.Service.Ports {
					oc.flowController.AddServiceOFRules(netid, ev.Service.IP, port.Protocol, port.Port)
				}
			}
		case <-oc.sig:
			log.Error("Signal received. Stopping watching of services.")
			stop <- true
			return
		}
	}
}

func watchPods(oc *OvsController, ready chan<- bool, start <-chan string) {
	stop := make(chan bool)
	go oc.Registry.WatchPods(ready, start, stop)

	<-oc.sig
	log.Error("Signal received. Stopping watching of pods.")
	stop <- true
}
