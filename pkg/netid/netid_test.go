package netid

import (
	"testing"
)

func TestValidVNID(t *testing.T) {
	testCases := []struct {
		input   uint
		success bool
		usecase string
	}{
		{MinVNID, true, "equal to min vnid"},
		{101, true, "greater than min vnid"},
		{0, true, "global vnid"},
		{MaxVNID - 4, true, "less than max vnid"},
		{MaxVNID, true, "equal to max vnid"},
		{4, false, "less than min vnid"},
		{MaxVNID + 4, false, "greater than max vnid"},
	}

	for i := range testCases {
		tc := &testCases[i]
		err := ValidVNID(tc.input)
		if err != nil && tc.success == true {
			t.Errorf("expected success for %s, got %q", tc.usecase, err)
			continue
		} else if err == nil && tc.success == false {
			t.Errorf("expected failure for %s", tc.usecase)
			continue
		}
	}
}
