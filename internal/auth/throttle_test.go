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
		failOnce(t, th, "A@example.com", fmt.Sprintf("10.0.0.%d", i), t0.Add(time.Duration(i)*time.Second))
	}
	last := t0.Add(4 * time.Second)
	if !allowed(th, "a@example.com", "10.0.1.1", last) {
		t.Fatal("refused after 4 failures")
	}
	failOnce(t, th, "a@EXAMPLE.com", "10.0.0.9", last)
	if allowed(th, "a@example.com", "10.0.1.1", last) {
		t.Fatal("allowed after the 5th failure")
	}
	if allowed(th, "A@Example.com", "10.0.1.1", last.Add(15*time.Minute-time.Millisecond)) {
		t.Error("allowed before 15 minutes passed since the 5th failure")
	}
	if !allowed(th, "a@example.com", "10.0.1.1", last.Add(15*time.Minute)) {
		t.Error("refused 15 minutes after the 5th failure")
	}
	if !allowed(th, "b@example.com", "10.0.1.1", last) {
		t.Error("another email is refused")
	}
}

func TestThrottleCountsOnlyFailuresWithinFifteenMinutes(t *testing.T) {
	th := NewThrottle()
	for i := range 4 {
		failOnce(t, th, "a@example.com", "10.0.0.1", t0.Add(time.Duration(i)*time.Millisecond))
	}
	// The first failure is exactly 15 minutes old here, so only 4 remain in the window.
	at := t0.Add(15 * time.Minute)
	failOnce(t, th, "a@example.com", "10.0.0.1", at)
	if !allowed(th, "a@example.com", "10.0.0.2", at) {
		t.Error("a failure 15 minutes old still counts")
	}
	failOnce(t, th, "a@example.com", "10.0.0.1", at)
	if allowed(th, "a@example.com", "10.0.0.2", at) {
		t.Error("5 failures within 15 minutes do not lock")
	}
}

func TestThrottleLocksIPAfterTwentyFailures(t *testing.T) {
	th := NewThrottle()
	for i := range 19 {
		failOnce(t, th, fmt.Sprintf("u%d@example.com", i), "10.0.0.1", t0)
	}
	if !allowed(th, "new@example.com", "10.0.0.1", t0) {
		t.Fatal("refused after 19 failures from one IP")
	}
	failOnce(t, th, "u19@example.com", "10.0.0.1", t0)
	if allowed(th, "new@example.com", "10.0.0.1", t0) {
		t.Fatal("allowed after the 20th failure from one IP")
	}
	if allowed(th, "new@example.com", "10.0.0.1", t0.Add(15*time.Minute-time.Millisecond)) {
		t.Error("IP allowed before 15 minutes passed")
	}
	if !allowed(th, "new@example.com", "10.0.0.1", t0.Add(15*time.Minute)) {
		t.Error("IP refused 15 minutes after the 20th failure")
	}
	if !allowed(th, "new@example.com", "10.0.0.2", t0) {
		t.Error("another IP is refused")
	}
}

func TestThrottleSweepDropsRecordsOlderThanFifteenMinutes(t *testing.T) {
	th := NewThrottle()
	failOnce(t, th, "old@example.com", "10.0.0.1", t0)
	failOnce(t, th, "new@example.com", "10.0.0.2", t0.Add(time.Millisecond))
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

func failOnce(t *testing.T, th *Throttle, email, ip string, now time.Time) {
	t.Helper()
	a, ok := th.Begin(email, ip, now)
	if !ok {
		t.Fatalf("Begin(%s, %s) refused before the failure was recorded", email, ip)
	}
	a.Fail(now)
}

func allowed(th *Throttle, email, ip string, now time.Time) bool {
	a, ok := th.Begin(email, ip, now)
	if ok {
		a.Succeed()
	}
	return ok
}

func TestThrottleBeginCountsAttemptsInFlight(t *testing.T) {
	th := NewThrottle()
	var open []Attempt
	for i := range 5 {
		a, ok := th.Begin("a@example.com", fmt.Sprintf("10.0.0.%d", i), t0)
		if !ok {
			t.Fatalf("attempt %d refused", i+1)
		}
		open = append(open, a)
	}
	if _, ok := th.Begin("A@example.com", "10.0.1.1", t0); ok {
		t.Fatal("6th attempt admitted while 5 are in flight")
	}
	open[0].Succeed()
	a, ok := th.Begin("a@example.com", "10.0.1.1", t0)
	if !ok {
		t.Fatal("attempt refused after one in-flight attempt succeeded")
	}
	a.Fail(t0)
	if _, ok := th.Begin("a@example.com", "10.0.1.1", t0); ok {
		t.Fatal("admitted with 1 failure and 4 attempts in flight")
	}
	for _, a := range open[1:] {
		a.Succeed()
	}
	if !allowed(th, "a@example.com", "10.0.1.1", t0) {
		t.Error("refused with 1 failure and nothing in flight")
	}
}

func TestThrottleBeginCountsIPAttemptsInFlight(t *testing.T) {
	th := NewThrottle()
	for i := range 20 {
		if _, ok := th.Begin(fmt.Sprintf("u%d@example.com", i), "10.0.0.1", t0); !ok {
			t.Fatalf("attempt %d refused", i+1)
		}
	}
	if allowed(th, "new@example.com", "10.0.0.1", t0) {
		t.Error("21st attempt from one IP admitted while 20 are in flight")
	}
}

func TestThrottleSweepKeepsKeysWithAttemptsInFlight(t *testing.T) {
	th := NewThrottle()
	a, _ := th.Begin("a@example.com", "10.0.0.1", t0)
	th.Sweep(t0)
	if len(th.email) != 1 || len(th.ip) != 1 {
		t.Fatalf("swept a key with an attempt in flight: %d emails, %d IPs", len(th.email), len(th.ip))
	}
	a.Succeed()
	th.Sweep(t0)
	if len(th.email) != 0 || len(th.ip) != 0 {
		t.Errorf("kept idle keys: %d emails, %d IPs", len(th.email), len(th.ip))
	}
}

func TestThrottleRefusedBeginAddsNoKey(t *testing.T) {
	th := NewThrottle()
	for i := range 20 {
		failOnce(t, th, fmt.Sprintf("u%d@example.com", i), "10.0.0.1", t0)
	}
	if allowed(th, "new@example.com", "10.0.0.1", t0) {
		t.Fatal("locked IP admitted")
	}
	if _, ok := th.email["new@example.com"]; ok || len(th.email) != 20 {
		t.Errorf("a refused Begin added an email key: %d keys", len(th.email))
	}
}
