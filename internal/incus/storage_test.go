package incus

import (
	"strings"
	"testing"
)

func TestGrowOnly(t *testing.T) {
	tests := []struct {
		name             string
		current, request string
		wantErr          bool
	}{
		{"grow GiB to TiB", "30GiB", "1TiB", false},
		{"grow within TiB", "1TiB", "2048GiB", false},
		{"equal is allowed", "1TiB", "1TiB", false},
		{"shrink rejected", "1TiB", "512GiB", true},
		{"bad current", "notasize", "1TiB", true},
		{"bad request", "1TiB", "notasize", true},
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

func TestGrowOnlyShrinkMessage(t *testing.T) {
	err := growOnly("1TiB", "512GiB")
	if err == nil || !strings.Contains(err.Error(), "only grow") {
		t.Fatalf("shrink should report a grow-only error, got %v", err)
	}
}

func TestStoragePoolUsedPct(t *testing.T) {
	tests := []struct {
		name        string
		used, total int64
		want        int
	}{
		{"zero total", 100, 0, 0},
		{"negative total", 10, -5, 0},
		{"half", 50, 100, 50},
		{"three percent rounds down", 30, 1000, 3},
		{"full", 100, 100, 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := StoragePool{UsedBytes: tt.used, TotalBytes: tt.total}
			if got := p.UsedPct(); got != tt.want {
				t.Fatalf("UsedPct(used=%d, total=%d) = %d, want %d", tt.used, tt.total, got, tt.want)
			}
		})
	}
}
