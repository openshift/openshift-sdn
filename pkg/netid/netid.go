// Accessor methods to annotate namespace for multitenant support
package netid

import (
	"fmt"
	"strconv"

	kapi "k8s.io/kubernetes/pkg/api"
)

const (
	// Maximum VXLAN Virtual Network Identifier(VNID) as per RFC#7348
	MaxVNID = uint((1 << 24) - 1)
	// VNID: 1 to 9 are internally reserved for any special cases in the future
	MinVNID = 10
	// VNID: 0 reserved for default namespace and can reach any network in the cluster
	GlobalVNID = uint(0)

	// Current assigned VNID for the namespace
	VNIDAnnotation string = "pod.network.openshift.io/multitenant.vnid"
	// Desired VNID for the namespace
	RequestedVNIDAnnotation string = "pod.network.openshift.io/multitenant.requested-vnid"
)

var (
	ErrorVNIDNotFound = fmt.Errorf("VNID or RequestedVNID annotation not found")
)

// Check if the given vnid is valid or not
func ValidVNID(vnid uint) error {
	if vnid == GlobalVNID {
		return nil
	}
	if vnid < MinVNID {
		return fmt.Errorf("VNID must be greater than or equal to %d", MinVNID)
	}
	if vnid > MaxVNID {
		return fmt.Errorf("VNID must be less than or equal to %d", MaxVNID)
	}
	return nil
}

// GetVNID returns vnid associated with the namespace
func GetVNID(ns *kapi.Namespace) (uint, error) {
	return getVNIDAnnotation(ns, VNIDAnnotation)
}

// SetVNID assigns vnid to the namespace
func SetVNID(ns *kapi.Namespace, id uint) error {
	return setVNIDAnnotation(ns, VNIDAnnotation, id)
}

// DeleteVNID deletes vnid from the namespace
func DeleteVNID(ns *kapi.Namespace) {
	delete(ns.Annotations, VNIDAnnotation)
}

// GetRequestedVNID fetches requested vnid for the namespace
func GetRequestedVNID(ns *kapi.Namespace) (uint, error) {
	return getVNIDAnnotation(ns, RequestedVNIDAnnotation)
}

// SetRequestedVNID requests vnid for the namespace
// This will be processed by the VNID controller and will be assigned if it meets required conditions.
func SetRequestedVNID(ns *kapi.Namespace, id uint) error {
	return setVNIDAnnotation(ns, RequestedVNIDAnnotation, id)
}

// DeleteRequestedVNID removes requested vnid intent from the namespace
func DeleteRequestedVNID(ns *kapi.Namespace) {
	delete(ns.Annotations, RequestedVNIDAnnotation)
}

func getVNIDAnnotation(ns *kapi.Namespace, annotationKey string) (uint, error) {
	if ns.Annotations == nil {
		return 0, ErrorVNIDNotFound
	}
	value, ok := ns.Annotations[annotationKey]
	if !ok {
		return 0, ErrorVNIDNotFound
	}
	id, err := strconv.ParseUint(value, 10, 32)
	vnid := uint(id)

	if err := ValidVNID(vnid); err != nil {
		return 0, err
	}
	return vnid, err
}

func setVNIDAnnotation(ns *kapi.Namespace, annotationKey string, id uint) error {
	if err := ValidVNID(id); err != nil {
		return err
	}

	if ns.Annotations == nil {
		ns.Annotations = make(map[string]string)
	}
	ns.Annotations[annotationKey] = strconv.Itoa(int(id))
	return nil
}
