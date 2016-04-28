package controller

import (
	"testing"
	"time"

	kapi "k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/client/unversioned/testclient"
	"k8s.io/kubernetes/pkg/runtime"

	"github.com/openshift/openshift-sdn/pkg/netid"
	"github.com/openshift/openshift-sdn/plugins/osdn/netid/vnid"
)

type fakeRange struct {
	Err       error
	Range     *kapi.RangeAllocation
	Updated   *kapi.RangeAllocation
	UpdateErr error
}

func (r *fakeRange) Get() (*kapi.RangeAllocation, error) {
	return r.Range, r.Err
}

func (r *fakeRange) CreateOrUpdate(update *kapi.RangeAllocation) error {
	r.Updated = update
	return r.UpdateErr
}

func TestRepair(t *testing.T) {
	testCases := map[string]struct {
		list      *kapi.NamespaceList
		allocated bool
	}{
		"empty allocation": {
			list: &kapi.NamespaceList{
				Items: []kapi.Namespace{
					{ObjectMeta: kapi.ObjectMeta{Name: "default"}},
				},
			},
			allocated: false,
		},
		"valid allocation": {
			list: &kapi.NamespaceList{
				Items: []kapi.Namespace{
					{
						ObjectMeta: kapi.ObjectMeta{
							Name:        "default",
							Annotations: map[string]string{netid.VNIDAnnotation: "105"},
						},
					},
				},
			},
			allocated: true,
		},
		"ignore invalid allocation": {
			list: &kapi.NamespaceList{
				Items: []kapi.Namespace{
					{
						ObjectMeta: kapi.ObjectMeta{
							Name:        "default",
							Annotations: map[string]string{netid.VNIDAnnotation: "121"},
						},
					},
				},
			},
			allocated: false,
		},
	}

	for s, testCase := range testCases {
		client := &testclient.Fake{}
		client.AddReactor("*", "*", func(a testclient.Action) (bool, runtime.Object, error) {
			return true, testCase.list, nil
		})

		alloc := &fakeRange{
			Range: &kapi.RangeAllocation{},
		}

		vnidr, _ := vnid.NewVNIDRange(101, 10)
		repair := NewRepair(0*time.Second, client.Namespaces(), vnidr, alloc)

		err := repair.RunOnce()
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		if alloc.Updated == nil {
			t.Fatalf("%s: did not store range: %#v", s, alloc)
		}
		if alloc.Updated.Range != "101-110" {
			t.Errorf("%s: didn't store range properly: %#v", s, alloc.Updated)
		}
		if testCase.allocated && (len(alloc.Updated.Data) == 0) {
			t.Errorf("%s: expected data but was empty: %#v", s, alloc.Updated)
		} else if !testCase.allocated && (len(alloc.Updated.Data) != 0) {
			t.Errorf("%s: expected empty data but found: %#v", s, alloc.Updated)
		}
	}
}
