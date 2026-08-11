package notifiers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type TelegramNotifier struct {
	botToken string
	chatID   string
	client   *http.Client
}

func NewTelegramNotifier(botToken, chatID string) *TelegramNotifier {
	return &TelegramNotifier{
		botToken: botToken,
		chatID:   chatID,
		client:   &http.Client{Timeout: 10 * time.Second},
	}
}

func (n *TelegramNotifier) Send(alert Alert) error {
	// Telegram HTML format
	text := fmt.Sprintf("<b>%s</b>\n\n%s\n\n<b>指标值</b>: %.2f\n<b>触发时间</b>: %s\n\n<i>InferenceHub v3 告警系统</i>",
		alert.Title, alert.Message, alert.Value, alert.Timestamp.Format("2006-01-02 15:04:05"))

	payload := map[string]interface{}{
		"chat_id":    n.chatID,
		"text":       text,
		"parse_mode": "HTML",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", n.botToken)
	resp, err := n.client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("telegram API returned status %d", resp.StatusCode)
	}

	return nil
}
