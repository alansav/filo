package store

import "testing"

func TestLastsAtLeast(t *testing.T) {
	tests := []struct {
		kept, other string
		want        bool
	}{
		{"", "", true},                      // neither has a retention: nothing is lost
		{"2027-01-01", "", true},            // the deleted copy had none
		{Forever, "", true},                 //
		{Forever, Forever, true},            //
		{Forever, "2030-01-01", true},       //
		{"2030-01-01", "2030-01-01", true},  // the same date
		{"2031-01-01", "2030-01-01", true},  // later
		{"2027-01-01", "2030-01-01", false}, // three years would be lost
		{"", "2030-01-01", false},           // the decision would be lost
		{"", Forever, false},                //
		{"2099-12-31", Forever, false},      // a date never outlasts forever
	}
	for _, tt := range tests {
		if got := LastsAtLeast(tt.kept, tt.other); got != tt.want {
			t.Errorf("LastsAtLeast(%q, %q) = %v, want %v", tt.kept, tt.other, got, tt.want)
		}
	}
}
