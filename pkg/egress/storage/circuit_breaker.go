package storage

import (
	"fmt"
	"sync"
	"time"
)

// CircuitBreakerState represents the state of a circuit breaker
type CircuitBreakerState int

const (
	// CircuitBreakerClosed allows all requests
	CircuitBreakerClosed CircuitBreakerState = iota
	// CircuitBreakerOpen blocks all requests
	CircuitBreakerOpen
	// CircuitBreakerHalfOpen allows limited requests for testing
	CircuitBreakerHalfOpen
)

// CircuitBreaker implements the circuit breaker pattern for handling failures
// Implements circuit breaker requirements from PLAN.md Milestone 3
type CircuitBreaker struct {
	mu sync.RWMutex

	// Configuration
	failureThreshold int           // Failures needed to open circuit
	successThreshold int           // Successes needed to close circuit
	openTimeout      time.Duration // Time to wait before trying half-open
	halfOpenRequests int           // Max requests in half-open state

	// State
	state            CircuitBreakerState
	failures         int
	successes        int
	lastFailureTime  time.Time
	lastSuccessTime  time.Time
	halfOpenAttempts int
	openedAt         time.Time

	// Statistics
	stats CircuitBreakerStats
}

// CircuitBreakerStats holds circuit breaker statistics
type CircuitBreakerStats struct {
	TotalRequests     int64
	TotalSuccesses    int64
	TotalFailures     int64
	ConsecutiveFails  int
	OpenCount         int64
	LastOpenTime      time.Time
	LastCloseTime     time.Time
	CurrentState      string
	TimeInOpenState   time.Duration
	TimeInClosedState time.Duration
	TimeInHalfOpen    time.Duration
}

// NewCircuitBreaker creates a new circuit breaker
func NewCircuitBreaker(failureThreshold, successThreshold int, openTimeout time.Duration, halfOpenRequests int) *CircuitBreaker {
	return &CircuitBreaker{
		failureThreshold: failureThreshold,
		successThreshold: successThreshold,
		openTimeout:      openTimeout,
		halfOpenRequests: halfOpenRequests,
		state:            CircuitBreakerClosed,
	}
}

// Execute runs a function through the circuit breaker
func (cb *CircuitBreaker) Execute(fn func() error) error {
	if !cb.CanExecute() {
		return fmt.Errorf("circuit breaker is open")
	}

	// Record attempt
	cb.mu.Lock()
	cb.stats.TotalRequests++
	cb.mu.Unlock()

	// Execute the function
	err := fn()

	// Record result
	if err != nil {
		cb.RecordFailure()
		return err
	}

	cb.RecordSuccess()
	return nil
}

// CanExecute checks if a request can be executed
func (cb *CircuitBreaker) CanExecute() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case CircuitBreakerClosed:
		return true

	case CircuitBreakerOpen:
		// Check if we should transition to half-open
		if time.Since(cb.openedAt) > cb.openTimeout {
			cb.transitionToHalfOpen()
			return true
		}
		return false

	case CircuitBreakerHalfOpen:
		// Allow limited requests in half-open state
		if cb.halfOpenAttempts < cb.halfOpenRequests {
			cb.halfOpenAttempts++
			return true
		}
		return false

	default:
		return false
	}
}

// RecordSuccess records a successful request
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.stats.TotalSuccesses++
	cb.lastSuccessTime = time.Now()
	cb.failures = 0 // Reset consecutive failures

	switch cb.state {
	case CircuitBreakerHalfOpen:
		cb.successes++
		if cb.successes >= cb.successThreshold {
			cb.transitionToClosed()
		}

	case CircuitBreakerOpen:
		// Should not happen, but handle gracefully
		cb.transitionToHalfOpen()
		cb.successes = 1

	case CircuitBreakerClosed:
		// Normal operation, nothing special to do
		cb.successes = 0 // Reset success counter in closed state
	}
}

// RecordFailure records a failed request
func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.stats.TotalFailures++
	cb.stats.ConsecutiveFails++
	cb.lastFailureTime = time.Now()
	cb.failures++
	cb.successes = 0 // Reset success counter

	switch cb.state {
	case CircuitBreakerClosed:
		if cb.failures >= cb.failureThreshold {
			cb.transitionToOpen()
		}

	case CircuitBreakerHalfOpen:
		// Single failure in half-open immediately opens the circuit
		cb.transitionToOpen()

	case CircuitBreakerOpen:
		// Already open, nothing to do
	}
}

// transitionToOpen transitions the circuit breaker to open state
func (cb *CircuitBreaker) transitionToOpen() {
	cb.state = CircuitBreakerOpen
	cb.openedAt = time.Now()
	cb.stats.OpenCount++
	cb.stats.LastOpenTime = cb.openedAt
	cb.failures = 0
	cb.successes = 0
	cb.halfOpenAttempts = 0
}

// transitionToHalfOpen transitions the circuit breaker to half-open state
func (cb *CircuitBreaker) transitionToHalfOpen() {
	cb.state = CircuitBreakerHalfOpen
	cb.halfOpenAttempts = 0
	cb.successes = 0
	cb.failures = 0
}

// transitionToClosed transitions the circuit breaker to closed state
func (cb *CircuitBreaker) transitionToClosed() {
	cb.state = CircuitBreakerClosed
	cb.stats.LastCloseTime = time.Now()
	cb.stats.ConsecutiveFails = 0
	cb.failures = 0
	cb.successes = 0
	cb.halfOpenAttempts = 0
}

// GetState returns the current state
func (cb *CircuitBreaker) GetState() CircuitBreakerState {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.state
}

// GetStateString returns the current state as a string
func (cb *CircuitBreaker) GetStateString() string {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	switch cb.state {
	case CircuitBreakerClosed:
		return "closed"
	case CircuitBreakerOpen:
		return "open"
	case CircuitBreakerHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// IsOpen returns true if the circuit breaker is open
func (cb *CircuitBreaker) IsOpen() bool {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.state == CircuitBreakerOpen
}

// IsClosed returns true if the circuit breaker is closed
func (cb *CircuitBreaker) IsClosed() bool {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.state == CircuitBreakerClosed
}

// IsHalfOpen returns true if the circuit breaker is half-open
func (cb *CircuitBreaker) IsHalfOpen() bool {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.state == CircuitBreakerHalfOpen
}

// GetStats returns circuit breaker statistics
func (cb *CircuitBreaker) GetStats() CircuitBreakerStats {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	stats := cb.stats
	stats.CurrentState = cb.GetStateString()

	// Calculate time in each state
	now := time.Now()
	switch cb.state {
	case CircuitBreakerOpen:
		stats.TimeInOpenState = now.Sub(cb.openedAt)
	case CircuitBreakerClosed:
		if !cb.stats.LastCloseTime.IsZero() {
			stats.TimeInClosedState = now.Sub(cb.stats.LastCloseTime)
		}
	}

	return stats
}

// Reset resets the circuit breaker to closed state
func (cb *CircuitBreaker) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.state = CircuitBreakerClosed
	cb.failures = 0
	cb.successes = 0
	cb.halfOpenAttempts = 0
	cb.stats.ConsecutiveFails = 0
}

// ForceOpen forces the circuit breaker to open state
func (cb *CircuitBreaker) ForceOpen() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.transitionToOpen()
}

// ForceClosed forces the circuit breaker to closed state
func (cb *CircuitBreaker) ForceClosed() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.transitionToClosed()
}

// IsAvailable returns true if circuit breaker allows requests
func (cb *CircuitBreaker) IsAvailable() bool {
	return cb.CanExecute()
}

// WithCircuitBreaker wraps a storage operation with circuit breaker protection
func WithCircuitBreaker(cb *CircuitBreaker, operation func() error) error {
	return cb.Execute(operation)
}

// CircuitBreakerManager manages multiple circuit breakers for different operations
type CircuitBreakerManager struct {
	mu       sync.RWMutex
	breakers map[string]*CircuitBreaker
	config   CircuitBreakerConfig
}

// NewCircuitBreakerManager creates a new circuit breaker manager
func NewCircuitBreakerManager(config CircuitBreakerConfig) *CircuitBreakerManager {
	return &CircuitBreakerManager{
		breakers: make(map[string]*CircuitBreaker),
		config:   config,
	}
}

// GetBreaker gets or creates a circuit breaker for an operation
func (m *CircuitBreakerManager) GetBreaker(operation string) *CircuitBreaker {
	m.mu.RLock()
	breaker, exists := m.breakers[operation]
	m.mu.RUnlock()

	if exists {
		return breaker
	}

	// Create new breaker
	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check after acquiring write lock
	if breaker, exists = m.breakers[operation]; exists {
		return breaker
	}

	breaker = NewCircuitBreaker(
		m.config.FailureThreshold,
		m.config.SuccessThreshold,
		m.config.OpenTimeout,
		m.config.HalfOpenRequests,
	)
	m.breakers[operation] = breaker

	return breaker
}

// ExecuteWithBreaker executes an operation with circuit breaker protection
func (m *CircuitBreakerManager) ExecuteWithBreaker(operation string, fn func() error) error {
	breaker := m.GetBreaker(operation)
	return breaker.Execute(fn)
}

// GetAllStats returns statistics for all circuit breakers
func (m *CircuitBreakerManager) GetAllStats() map[string]CircuitBreakerStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	stats := make(map[string]CircuitBreakerStats)
	for name, breaker := range m.breakers {
		stats[name] = breaker.GetStats()
	}
	return stats
}

// ResetAll resets all circuit breakers
func (m *CircuitBreakerManager) ResetAll() {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, breaker := range m.breakers {
		breaker.Reset()
	}
}

// IsHealthy checks if all circuit breakers are healthy (not open)
func (m *CircuitBreakerManager) IsHealthy() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, breaker := range m.breakers {
		if breaker.IsOpen() {
			return false
		}
	}
	return true
}