package notifiers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type WechatNotifier struct {
	webhookURL string
	client     *http.Client
}

func NewWechatNotifier(webhookURL string) *WechatNotifier {
	return &WechatNotifier{
		webhookURL: webhookURL,
		client:     &http.Client{Timeout: 10 * time.Second},
	}
}

func (n *WechatNotifier) Send(alert Alert) error {
	// WeChat markdown format
	content := fmt.Sprintf("## %s\n\n%s\n\n**指标值**: %.2f\n**触发时间**: %s",
		alert.Title, alert.Message, alert.Value, alert.Timestamp.Format("2006-01-02 15:04:05"))

	card := map[string]interface{}{
		"msgtype": "markdown",
		"markdown": map[string]interface{}{
			"content": content,
		},
	}

	body, err := json.Marshal(card)
	if err != nil {
		return err
	}

	resp, err := n.client.Post(n.webhookURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("wechat webhook returned status %d", resp.StatusCode)
	}

	return nil
}
