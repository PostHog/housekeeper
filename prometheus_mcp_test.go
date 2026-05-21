package main

import (
	"strings"
	"testing"
	"time"
)

func TestValidateAndParseTimeRange_RejectsFutureStart(t *testing.T) {
	future := time.Now().UTC().Add(1 * time.Hour).Format(time.RFC3339)
	_, _, err := validateAndParseTimeRange(future, "")
	if err == nil {
		t.Fatal("expected error for future start time, got nil")
	}
	if !strings.Contains(err.Error(), "future") {
		t.Errorf("expected error to mention 'future', got: %v", err)
	}
	if !strings.Contains(err.Error(), "current UTC is") {
		t.Errorf("expected error to surface current UTC, got: %v", err)
	}
}

func TestValidateAndParseTimeRange_RejectsFutureEnd(t *testing.T) {
	start := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
	end := time.Now().UTC().Add(1 * time.Hour).Format(time.RFC3339)
	_, _, err := validateAndParseTimeRange(start, end)
	if err == nil {
		t.Fatal("expected error for future end time, got nil")
	}
	if !strings.Contains(err.Error(), "future") {
		t.Errorf("expected error to mention 'future', got: %v", err)
	}
}

func TestValidateAndParseTimeRange_AllowsSmallClockSkew(t *testing.T) {
	// 5 seconds in the future should be tolerated (within the 30s skew window)
	nearFuture := time.Now().UTC().Add(5 * time.Second).Format(time.RFC3339)
	_, _, err := validateAndParseTimeRange("-1h", nearFuture)
	if err != nil {
		t.Errorf("expected small clock-skew to be tolerated, got: %v", err)
	}
}

func TestValidateAndParseTimeRange_AcceptsPastRange(t *testing.T) {
	start := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	end := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
	_, _, err := validateAndParseTimeRange(start, end)
	if err != nil {
		t.Errorf("expected past range to be accepted, got: %v", err)
	}
}

func TestValidateAndParseTimeRange_AcceptsRelativeStart(t *testing.T) {
	_, _, err := validateAndParseTimeRange("-30m", "")
	if err != nil {
		t.Errorf("expected relative start to be accepted, got: %v", err)
	}
}

func TestValidateAndParseTimeRange_RejectsStartAfterEnd(t *testing.T) {
	start := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
	end := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	_, _, err := validateAndParseTimeRange(start, end)
	if err == nil {
		t.Fatal("expected error for start > end, got nil")
	}
	if !strings.Contains(err.Error(), "before") {
		t.Errorf("expected error to mention ordering, got: %v", err)
	}
}
