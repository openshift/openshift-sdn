package controller

import (
	"fmt"
	"strings"
	"testing"

	kapi "k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/api/errors"
	"k8s.io/kubernetes/pkg/client/unversioned/testclient"
	"k8s.io/kubernetes/pkg/runtime"

	"github.com/openshift/openshift-sdn/pkg/netid"
	"github.com/openshift/openshift-sdn/plugins/osdn/netid/vnid"
	"github.com/openshift/openshift-sdn/plugins/osdn/netid/vnidallocator"
)

func TestController(t *testing.T) {
	var action testclient.Action
	client := &testclient.Fake{}
	client.AddReactor("*", "*", func(a testclient.Action) (handled bool, ret runtime.Object, err error) {
		action = a
		if a.Matches("list", "namespaces") {
			return true, &kapi.NamespaceList{}, nil
		}
		return true, (*kapi.Namespace)(nil), nil
	})

	vnidr, err := vnid.NewVNIDRange(101, 10)
	if err != nil {
		t.Fatal(err)
	}
	vnida := vnidallocator.NewInMemory(vnidr)
	c := VnidController{
		vnid:   vnida,
		client: client.Namespaces(),
	}

	// Add VNID annotation
	err = c.addOrUpdate(&kapi.Namespace{ObjectMeta: kapi.ObjectMeta{Name: "test"}})
	if err != nil {
		t.Fatal(err)
	}

	got := action.(testclient.CreateAction).GetObject().(*kapi.Namespace)
	id, err := netid.GetVNID(got)
	if err != nil {
		t.Fatal("vnid not found in namespace object: %#v", got)
	}
	if id != 101 {
		t.Errorf("unexpected vnid allocation: %#v", got)
	}
	if !vnida.Has(101) {
		t.Errorf("did not allocate vnid: %#v", vnida)
	}

	// Delete VNID annotation
	err = c.delete(got)
	if err != nil {
		t.Fatal(err)
	}
	if vnida.Has(101) {
		t.Errorf("did not release vnid: %#v", vnida)
	}
}

func TestControllerError(t *testing.T) {
	testCases := map[string]struct {
		err     func() error
		errFn   func(err error) bool
		reactFn testclient.ReactionFunc
		actions int
	}{
		"not found": {
			err:     func() error { return errors.NewNotFound(kapi.Resource("Namespace"), "test") },
			errFn:   func(err error) bool { return err == nil },
			actions: 1,
		},
		"unknown": {
			err:     func() error { return fmt.Errorf("unknown") },
			errFn:   func(err error) bool { return err.Error() == "unknown" },
			actions: 1,
		},
		"conflict": {
			actions: 4,
			reactFn: func(a testclient.Action) (bool, runtime.Object, error) {
				if a.Matches("get", "namespaces") {
					return true, &kapi.Namespace{ObjectMeta: kapi.ObjectMeta{Name: "test"}}, nil
				}
				return true, (*kapi.Namespace)(nil), errors.NewConflict(kapi.Resource("namespace"), "test", fmt.Errorf("test conflict"))
			},
			errFn: func(err error) bool {
				return err != nil && strings.Contains(err.Error(), "unable to allocate vnid")
			},
		},
	}

	for s, testCase := range testCases {
		client := &testclient.Fake{}

		if testCase.reactFn == nil {
			testCase.reactFn = func(a testclient.Action) (bool, runtime.Object, error) {
				return true, (*kapi.Namespace)(nil), testCase.err()
			}
		}

		client.AddReactor("*", "*", testCase.reactFn)

		vnidr, err := vnid.NewVNIDRange(101, 10)
		if err != nil {
			t.Fatal(err)
		}
		vnida := vnidallocator.NewInMemory(vnidr)
		c := VnidController{
			vnid:   vnida,
			client: client.Namespaces(),
		}

		// Add VNID annotation
		err = c.addOrUpdate(&kapi.Namespace{ObjectMeta: kapi.ObjectMeta{Name: "test"}})
		if !testCase.errFn(err) {
			t.Errorf("%s: unexpected error: %v", s, err)
		}

		if len(client.Actions()) != testCase.actions {
			t.Errorf("%s: expected %d actions: %v", s, testCase.actions, client.Actions())
		}
		if vnida.Free() != 10 {
			t.Errorf("%s: should not have allocated vnid: %d", s, vnida.Free())
		}
	}
}
