package domain

import (
	"testing"
	"time"
)

func TestHoldTTLAndExpiryGrace(t *testing.T) {
	if HoldTTL != 10*time.Minute {
		t.Errorf("HoldTTL = %v, want 10m", HoldTTL)
	}
	if ExpiryGrace != time.Minute {
		t.Errorf("ExpiryGrace = %v, want 1m", ExpiryGrace)
	}
}
