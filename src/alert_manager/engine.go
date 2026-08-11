package alert_manager

import (
	"fmt"
	"os"
	"strings"
	"time"

	"inference-hub-v3/src/alert_manager/notifiers"
	"inference-hub-v3/src/shared"
)

// AlertState tracks the state of an alert rule
type AlertState struct {
	Firing       bool
	Since        time.Time
	LastNotified time.Time
	CooldownSec  int
}

// AlertEngine evaluates alert rules and sends notifications
type AlertEngine struct {
	rules     map[string]shared.AlertRule
	states    map[string]*AlertState
	notifiers map[string]notifiers.Notifier
}

// NewAlertEngine creates a new alert engine
func NewAlertEngine(cfg *shared.AlertConfig) *AlertEngine {
	ae := &AlertEngine{
		rules:     cfg.Rules,
		states:    make(map[string]*AlertState),
		notifiers: make(map[string]notifiers.Notifier),
	}

	// Initialize notifiers
	for name, notifierCfg := range cfg.Notifiers {
		switch name {
		case "feishu":
			webhook := os.Getenv(notifierCfg.WebhookURLEnv)
			if webhook != "" {
				ae.notifiers[name] = notifiers.NewFeishuNotifier(webhook)
				shared.Infof("[AlertEngine] Feishu notifier initialized")
			}
		case "wechat":
			webhook := os.Getenv(notifierCfg.WebhookURLEnv)
			if webhook != "" {
				ae.notifiers[name] = notifiers.NewWechatNotifier(webhook)
				shared.Infof("[AlertEngine] WeChat notifier initialized")
			}
		case "telegram":
			token := os.Getenv(notifierCfg.BotTokenEnv)
			chatID := os.Getenv(notifierCfg.ChatIDEnv)
			if token != "" && chatID != "" {
				ae.notifiers[name] = notifiers.NewTelegramNotifier(token, chatID)
				shared.Infof("[AlertEngine] Telegram notifier initialized")
			}
		}
	}

	// Initialize states
	for name := range cfg.Rules {
		ae.states[name] = &AlertState{CooldownSec: 300} // 5 min cooldown
	}

	return ae
}

// Evaluate checks all rules against current metrics
func (ae *AlertEngine) Evaluate(metrics map[string]float64) []shared.AlertRule {
	var triggered []shared.AlertRule

	shared.Infof("[AlertEngine] evaluating %d rules against %d metrics", len(ae.rules), len(metrics))

	for name, rule := range ae.rules {
		if !rule.Enabled {
			continue
		}

		value, exists := metrics[rule.Metric]
		if !exists {
			continue
		}

		state := ae.states[name]
		if state == nil {
			state = &AlertState{CooldownSec: 300}
			ae.states[name] = state
		}

		// Evaluate condition
		satisfied := false
		shared.Infof("[AlertEngine] rule %s: metric=%s value=%.2f threshold=%.2f condition=%s",
			name, rule.Metric, value, rule.Threshold, rule.Condition)
		switch rule.Condition {
		case "gt":
			satisfied = value > rule.Threshold
		case "lt":
			satisfied = value < rule.Threshold
		case "eq":
			satisfied = value == rule.Threshold
		case "gte":
			satisfied = value >= rule.Threshold
		case "lte":
			satisfied = value <= rule.Threshold
		}

		now := time.Now()

		if satisfied {
			if !state.Firing {
				state.Firing = true
				state.Since = now
			}

			// Check duration requirement
			if now.Sub(state.Since) >= time.Duration(rule.DurationSec)*time.Second {
				// Check cooldown
				if now.Sub(state.LastNotified) >= time.Duration(state.CooldownSec)*time.Second {
					// Trigger notification
					message := rule.Message
					message = strings.ReplaceAll(message, "{{value}}", fmt.Sprintf("%.2f", value))
					message = strings.ReplaceAll(message, "{{metric}}", rule.Metric)
					message = strings.ReplaceAll(message, "{{severity}}", rule.Severity)

					ae.sendNotification(rule, message, value)
					state.LastNotified = now
				}
				triggered = append(triggered, rule)
			}
		} else {
			if state.Firing {
				state.Firing = false
				state.Since = time.Time{}
			}
		}
	}

	return triggered
}

func (ae *AlertEngine) sendNotification(rule shared.AlertRule, message string, value float64) {
	for _, channel := range rule.Channels {
		notifier, exists := ae.notifiers[channel]
		if !exists {
			shared.Warnf("[AlertEngine] notifier %s not configured", channel)
			continue
		}

		alert := notifiers.Alert{
			Title:     fmt.Sprintf("[%s] %s", strings.ToUpper(rule.Severity), rule.Metric),
			Message:   message,
			Severity:  rule.Severity,
			Value:     value,
			Timestamp: time.Now(),
		}

		if err := notifier.Send(alert); err != nil {
			shared.Errorf("[AlertEngine] failed to send alert via %s: %v", channel, err)
		} else {
			shared.Infof("[AlertEngine] alert sent via %s: %s", channel, message)
		}
	}
}

// GetState returns the current state of all alerts
func (ae *AlertEngine) GetState() map[string]map[string]interface{} {
	result := make(map[string]map[string]interface{})

	for name, state := range ae.states {
		rule := ae.rules[name]
		result[name] = map[string]interface{}{
			"firing":    state.Firing,
			"since":     state.Since,
			"severity":  rule.Severity,
			"enabled":   rule.Enabled,
			"threshold": rule.Threshold,
			"condition": rule.Condition,
			"metric":    rule.Metric,
		}
	}

	return result
}
