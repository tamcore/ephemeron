package health

import (
	"log/slog"
	"sync"
)

// Checker tracks consecutive failures and determines system health.
// It is safe for concurrent use.
type Checker struct {
	mu                  sync.RWMutex
	consecutiveFailures int
	threshold           int
	logger              *slog.Logger
}

// New creates a Checker that reports unhealthy after threshold consecutive failures.
func New(threshold int, logger *slog.Logger) *Checker {
	return &Checker{
		threshold: threshold,
		logger:    logger,
	}
}

// ReportSuccess resets the consecutive failure counter.
func (c *Checker) ReportSuccess() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.consecutiveFailures > 0 {
		c.logger.Info("registry health recovered", "previous_failures", c.consecutiveFailures)
	}
	c.consecutiveFailures = 0
}

// ReportFailure increments the consecutive failure counter.
func (c *Checker) ReportFailure() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.consecutiveFailures++
	c.logger.Warn("registry health degraded",
		"consecutive_failures", c.consecutiveFailures,
		"threshold", c.threshold,
	)
}

// IsHealthy returns false when consecutive failures have reached the threshold.
func (c *Checker) IsHealthy() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.consecutiveFailures < c.threshold
}
