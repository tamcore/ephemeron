package health

import (
	"log/slog"
	"sync"
	"testing"
)

func TestNew_StartsHealthy(t *testing.T) {
	c := New(3, slog.Default())
	if !c.IsHealthy() {
		t.Error("new checker should be healthy")
	}
	if c.ConsecutiveFailures() != 0 {
		t.Errorf("expected 0 failures, got %d", c.ConsecutiveFailures())
	}
}

func TestReportFailure_TriggersUnhealthy(t *testing.T) {
	c := New(3, slog.Default())

	c.ReportFailure()
	if !c.IsHealthy() {
		t.Error("should still be healthy after 1 failure")
	}

	c.ReportFailure()
	if !c.IsHealthy() {
		t.Error("should still be healthy after 2 failures")
	}

	c.ReportFailure()
	if c.IsHealthy() {
		t.Error("should be unhealthy after 3 failures (threshold)")
	}
	if c.ConsecutiveFailures() != 3 {
		t.Errorf("expected 3 failures, got %d", c.ConsecutiveFailures())
	}
}

func TestReportSuccess_ResetsAfterFailures(t *testing.T) {
	c := New(3, slog.Default())

	c.ReportFailure()
	c.ReportFailure()
	c.ReportSuccess()

	if !c.IsHealthy() {
		t.Error("should be healthy after success resets failures")
	}
	if c.ConsecutiveFailures() != 0 {
		t.Errorf("expected 0 failures after reset, got %d", c.ConsecutiveFailures())
	}
}

func TestReportSuccess_ResetsFromUnhealthy(t *testing.T) {
	c := New(2, slog.Default())

	c.ReportFailure()
	c.ReportFailure()
	if c.IsHealthy() {
		t.Error("should be unhealthy at threshold")
	}

	c.ReportSuccess()
	if !c.IsHealthy() {
		t.Error("should be healthy after success")
	}
}

func TestThresholdOne(t *testing.T) {
	c := New(1, slog.Default())

	if !c.IsHealthy() {
		t.Error("should start healthy")
	}

	c.ReportFailure()
	if c.IsHealthy() {
		t.Error("should be unhealthy after 1 failure with threshold=1")
	}
}

func TestConcurrentAccess(t *testing.T) {
	c := New(100, slog.Default())

	var wg sync.WaitGroup
	for range 50 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			c.ReportFailure()
		}()
		go func() {
			defer wg.Done()
			c.IsHealthy()
		}()
	}
	wg.Wait()

	if c.ConsecutiveFailures() != 50 {
		t.Errorf("expected 50 failures after concurrent writes, got %d", c.ConsecutiveFailures())
	}
}
