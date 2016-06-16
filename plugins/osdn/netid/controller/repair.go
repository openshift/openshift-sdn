package controller

import (
	"fmt"
	"time"

	kapi "k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/api/errors"
	kclient "k8s.io/kubernetes/pkg/client/unversioned"
	"k8s.io/kubernetes/pkg/registry/service"
	"k8s.io/kubernetes/pkg/util/runtime"
	"k8s.io/kubernetes/pkg/util/wait"

	"github.com/openshift/openshift-sdn/pkg/netid"
	"github.com/openshift/openshift-sdn/plugins/osdn/netid/vnid"
	"github.com/openshift/openshift-sdn/plugins/osdn/netid/vnidallocator"
)

type Repair struct {
	interval  time.Duration
	client    kclient.NamespaceInterface
	alloc     service.RangeRegistry
	vnidRange *vnid.VNIDRange
}

// NewRepair creates a controller that periodically ensures that VNIDs are allocated for all namespaces
// when using multitenant network plugin and generates informational warnings like VNID leaks, etc.
func NewRepair(interval time.Duration, client kclient.NamespaceInterface, vnidRange *vnid.VNIDRange, alloc service.RangeRegistry) *Repair {
	return &Repair{
		interval:  interval,
		client:    client,
		vnidRange: vnidRange,
		alloc:     alloc,
	}
}

// RunUntil starts the controller until the provided ch is closed.
func (c *Repair) RunUntil(ch chan struct{}) {
	wait.Until(func() {
		if err := c.RunOnce(); err != nil {
			runtime.HandleError(err)
		}
	}, c.interval, ch)
}

// RunOnce verifies the state of the vnid allocations and returns an error if an unrecoverable problem occurs.
func (c *Repair) RunOnce() error {
	return kclient.RetryOnConflict(kclient.DefaultBackoff, c.runOnce)
}

// runOnce verifies the state of the vnid allocations and returns an error if an unrecoverable problem occurs.
func (c *Repair) runOnce() error {
	// TODO: (per smarterclayton) if Get() or List() is a weak consistency read,
	// or if they are executed against different leaders,
	// the ordering guarantee required to ensure no item is allocated twice is violated.
	// List must return a ResourceVersion higher than the etcd index Get,
	// and the release code must not release items that have allocated but not yet been created
	// See #8295

	latest, err := c.alloc.Get()
	if err != nil {
		return fmt.Errorf("unable to refresh the vnid allocation block: %v", err)
	}

	nsList, err := c.client.List(kapi.ListOptions{})
	if err != nil {
		return fmt.Errorf("unable to list namespaces: %v", err)
	}
	netIDCountMap := make(map[uint]int, len(nsList.Items))
	for _, ns := range nsList.Items {
		if id, err := netid.GetVNID(&ns); err == nil {
			netIDCountMap[id] += 1
		}
	}

	r := vnidallocator.NewInMemory(c.vnidRange)
	for _, ns := range nsList.Items {
		id, err := netid.GetVNID(&ns)
		if err != nil {
			continue
		}

		// Skip GlobalVNID as it is not part of the VNID allocation
		if id == netid.GlobalVNID {
			continue
		}

		switch err := r.Allocate(id); err {
		case nil: // Expected value
		case vnidallocator.ErrAllocated:
			if netIDCountMap[id] == 1 {
				// TODO: send event
				runtime.HandleError(fmt.Errorf("unexpected vnid %d allocated error for namespace %s", id, ns.ObjectMeta.Name))
			}
		case vnidallocator.ErrNotInRange:
			// TODO: send event
			// vnid is broken, reallocate
			runtime.HandleError(fmt.Errorf("the vnid %d for namespace %s is not within the vnid range %v; please recreate", id, ns.ObjectMeta.Name, c.vnidRange))
		case vnidallocator.ErrFull:
			// TODO: send event
			return fmt.Errorf("the vnid range %s is full; you must widen the vnid range in order to accomodate new namespaces", c.vnidRange)
		default:
			return fmt.Errorf("unable to allocate vnid %d for namespace %s due to an unknown error: %v", id, ns.ObjectMeta.Name, err)
		}
	}

	err = r.Snapshot(latest)
	if err != nil {
		return fmt.Errorf("unable to take snapshot of vnid allocations: %v", err)
	}

	if err := c.alloc.CreateOrUpdate(latest); err != nil {
		if errors.IsConflict(err) {
			return err
		}
		return fmt.Errorf("unable to persist the updated vnid allocations: %v", err)
	}
	return nil
}
