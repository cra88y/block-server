package items

import "testing"

func TestClampMatchDurationSec(t *testing.T) {
	active := &ActiveMatch{StartTime: 1_000_000}
	now := int64(1_060_000) // 60s elapsed

	if got := clampMatchDurationSec(active, 0, now); got != 60 {
		t.Fatalf("missing client duration should fallback to server 60s, got %d", got)
	}
	if got := clampMatchDurationSec(active, 45, now); got != 45 {
		t.Fatalf("active play with pause (45s <= 60s) should be preserved, got %d", got)
	}
	if got := clampMatchDurationSec(active, 3600, now); got != 60 {
		t.Fatalf("spoofed client duration (3600s > 60s) must clamp to 60s, got %d", got)
	}
	if got := clampMatchDurationSec(nil, -5, now); got != 0 {
		t.Fatalf("negative duration without active match must clamp to 0, got %d", got)
	}
}
