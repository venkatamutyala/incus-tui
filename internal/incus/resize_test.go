package incus

import (
	"strings"
	"testing"
)

// growOnly backs the VM disk-resize grow guard (SetResources). A VM block disk can
// only grow; a smaller target must be rejected before the daemon round-trip.
func TestGrowOnly(t *testing.T) {
	tests := []struct {
		name             string
		current, request string
		wantErr          bool
	}{
		{"grow", "10GiB", "12GiB", false},
		{"equal", "10GiB", "10GiB", false},
		{"grow across units", "10GiB", "1TiB", false},
		{"shrink", "12GiB", "8GiB", true},
		{"shrink across units", "1TiB", "512GiB", true},
		{"bad current", "banana", "10GiB", true},
		{"bad request", "10GiB", "banana", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := growOnly(tt.current, tt.request)
			if (err != nil) != tt.wantErr {
				t.Fatalf("growOnly(%q, %q) error = %v, wantErr = %v", tt.current, tt.request, err, tt.wantErr)
			}
		})
	}
}

// A shrink message should name both sizes so the user sees why it was refused.
func TestGrowOnlyShrinkMessage(t *testing.T) {
	err := growOnly("12GiB", "8GiB")
	if err == nil || !strings.Contains(err.Error(), "can only grow") {
		t.Fatalf("shrink error = %v, want it to mention 'can only grow'", err)
	}
}
