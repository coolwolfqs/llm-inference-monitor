package notifiers

import "time"

// Alert represents an alert notification
type Alert struct {
	Title     string    `json:"title"`
	Message   string    `json:"message"`
	Severity  string    `json:"severity"`
	Value     float64   `json:"value"`
	Timestamp time.Time `json:"timestamp"`
}

// Notifier interface for sending alerts
type Notifier interface {
	Send(alert Alert) error
}
