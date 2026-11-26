package auth

import (
	"fmt"
	"testing"
	"time"
)

var t0 = time.UnixMilli(1_800_000_000_000).UTC()

func TestThrottleLocksEmailAfterFiveFailuresForFifteenMinutes(t *testing.T) {
	th := NewThrottle()
	for i := range 4 {
		th.Fail("A@example.com", fmt.Sprintf("10.0.0.%d", i), t0.Add(time.Duration(i)*time.Second))
	}
	last := t0.Add(4 * time.Second)
	if !th.Allowed("a@example.com", "10.0.1.1", last) {
		t.Fatal("refused after 4 failures")
	}
	th.Fail("a@EXAMPLE.com", "10.0.0.9", last)
	if th.Allowed("a@example.com", "10.0.1.1", last) {
		t.Fatal("allowed after the 5th failure")
	}
	if th.Allowed("A@Example.com", "10.0.1.1", last.Add(15*time.Minute-time.Millisecond)) {
		t.Error("allowed before 15 minutes passed since the 5th failure")
	}
	if !th.Allowed("a@example.com", "10.0.1.1", last.Add(15*time.Minute)) {
		t.Error("refused 15 minutes after the 5th failure")
	}
	if !th.Allowed("b@example.com", "10.0.1.1", last) {
		t.Error("another email is refused")
	}
}

func TestThrottleCountsOnlyFailuresWithinFifteenMinutes(t *testing.T) {
	th := NewThrottle()
	for i := range 4 {
		th.Fail("a@example.com", "10.0.0.1", t0.Add(time.Duration(i)*time.Millisecond))
	}
	// The first failure is exactly 15 minutes old here, so only 4 remain in the window.
	at := t0.Add(15 * time.Minute)
	th.Fail("a@example.com", "10.0.0.1", at)
	if !th.Allowed("a@example.com", "10.0.0.2", at) {
		t.Error("a failure 15 minutes old still counts")
	}
	th.Fail("a@example.com", "10.0.0.1", at)
	if th.Allowed("a@example.com", "10.0.0.2", at) {
		t.Error("5 failures within 15 minutes do not lock")
	}
}

func TestThrottleLocksIPAfterTwentyFailures(t *testing.T) {
	th := NewThrottle()
	for i := range 19 {
		th.Fail(fmt.Sprintf("u%d@example.com", i), "10.0.0.1", t0)
	}
	if !th.Allowed("new@example.com", "10.0.0.1", t0) {
		t.Fatal("refused after 19 failures from one IP")
	}
	th.Fail("u19@example.com", "10.0.0.1", t0)
	if th.Allowed("new@example.com", "10.0.0.1", t0) {
		t.Fatal("allowed after the 20th failure from one IP")
	}
	if th.Allowed("new@example.com", "10.0.0.1", t0.Add(15*time.Minute-time.Millisecond)) {
		t.Error("IP allowed before 15 minutes passed")
	}
	if !th.Allowed("new@example.com", "10.0.0.1", t0.Add(15*time.Minute)) {
		t.Error("IP refused 15 minutes after the 20th failure")
	}
	if !th.Allowed("new@example.com", "10.0.0.2", t0) {
		t.Error("another IP is refused")
	}
}

func TestThrottleSweepDropsRecordsOlderThanFifteenMinutes(t *testing.T) {
	th := NewThrottle()
	th.Fail("old@example.com", "10.0.0.1", t0)
	th.Fail("new@example.com", "10.0.0.2", t0.Add(time.Millisecond))
	th.Sweep(t0.Add(15*time.Minute - time.Millisecond))
	if len(th.email) != 2 || len(th.ip) != 2 {
		t.Fatalf("swept records younger than 15 minutes: %d emails, %d IPs", len(th.email), len(th.ip))
	}
	th.Sweep(t0.Add(15 * time.Minute))
	if _, ok := th.email["old@example.com"]; ok || len(th.email) != 1 {
		t.Errorf("email records after sweep = %v; want only new@example.com", th.email)
	}
	if _, ok := th.ip["10.0.0.1"]; ok || len(th.ip) != 1 {
		t.Errorf("IP records after sweep = %v; want only 10.0.0.2", th.ip)
	}
	if n := len(th.email["new@example.com"].fails); n != 1 {
		t.Errorf("kept record has %d failures; want 1", n)
	}
}
