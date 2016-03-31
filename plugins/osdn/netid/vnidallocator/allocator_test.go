package vnidallocator

import (
	"strconv"
	"testing"

	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/util/sets"

	"github.com/openshift/openshift-sdn/plugins/osdn/netid/vnid"
)

func TestAllocate(t *testing.T) {
	vr, err := vnid.NewVNIDRange(200, 100)
	if err != nil {
		t.Fatal(err)
	}
	r := NewInMemory(vr)
	if f := r.Free(); f != 100 {
		t.Errorf("unexpected free %d", f)
	}

	// Test AllocateNext()
	found := sets.NewString()
	count := 0
	for r.Free() > 0 {
		vnid, err := r.AllocateNext()
		if err != nil {
			t.Fatalf("error @ %d: %v", count, err)
		}
		count++
		if ok, _ := vr.Contains(vnid); !ok {
			t.Fatalf("allocated %d which is outside of %v", vnid, vr)
		}
		vnidString := strconv.Itoa(int(vnid))
		if found.Has(vnidString) {
			t.Fatalf("allocated %d twice @ %d", vnid, count)
		}
		found.Insert(vnidString)
	}
	if count != 100 {
		t.Fatal("failed to allocate all vnids in the given range")
	}
	if _, err := r.AllocateNext(); err != ErrFull {
		t.Fatal(err)
	}

	// Test Release()
	released := uint(210)
	if err := r.Release(released); err != nil {
		t.Fatal(err)
	}
	if f := r.Free(); f != 1 {
		t.Errorf("unexpected free %d", f)
	}
	vnid, err := r.AllocateNext()
	if err != nil {
		t.Fatal(err)
	}
	if released != vnid {
		t.Errorf("unexpected %d : %d", vnid, released)
	}

	// Test Allocate()
	if err := r.Release(released); err != nil {
		t.Fatal(err)
	}
	if err := r.Allocate(1); err != ErrNotInRange {
		t.Fatal(err)
	}
	if err := r.Allocate(201); err != ErrAllocated {
		t.Fatal(err)
	}
	if err := r.Allocate(300); err != ErrNotInRange {
		t.Fatal(err)
	}
	if err := r.Allocate(500); err != ErrNotInRange {
		t.Fatal(err)
	}
	if f := r.Free(); f != 1 {
		t.Errorf("unexpected free %d", f)
	}
	if err := r.Allocate(released); err != nil {
		t.Fatal(err)
	}
	if f := r.Free(); f != 0 {
		t.Errorf("unexpected free %d", f)
	}
}

func TestSnapshot(t *testing.T) {
	vr, err := vnid.NewVNIDRange(200, 100)
	if err != nil {
		t.Fatal(err)
	}
	r := NewInMemory(vr)
	vnids := []uint{}
	for i := 0; i < 10; i++ {
		vnid, err := r.AllocateNext()
		if err != nil {
			t.Fatal(err)
		}
		vnids = append(vnids, vnid)
	}

	var dst api.RangeAllocation
	err = r.Snapshot(&dst)
	if err != nil {
		t.Fatal(err)
	}

	vr2, err := vnid.ParseVNIDRange(dst.Range)
	if err != nil {
		t.Fatal(err)
	}

	if vr.String() != vr2.String() {
		t.Fatalf("mismatched ranges: %s : %s", vr, vr2)
	}

	otherVr, err := vnid.NewVNIDRange(200, 300)
	if err != nil {
		t.Fatal(err)
	}
	other := NewInMemory(otherVr)
	if err := r.Restore(*otherVr, dst.Data); err != ErrMismatchedRange {
		t.Fatal(err)
	}
	other = NewInMemory(vr2)
	if err := other.Restore(*vr2, dst.Data); err != nil {
		t.Fatal(err)
	}

	for _, vnid := range vnids {
		if !other.Has(vnid) {
			t.Errorf("restored range does not have %d", vnid)
		}
	}
	if other.Free() != r.Free() {
		t.Errorf("counts do not match: %d : %d", other.Free(), r.Free())
	}
}
