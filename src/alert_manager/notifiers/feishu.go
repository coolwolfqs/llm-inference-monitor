package notifiers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type FeishuNotifier struct {
	webhookURL string
	client     *http.Client
}

func NewFeishuNotifier(webhookURL string) *FeishuNotifier {
	return &FeishuNotifier{
		webhookURL: webhookURL,
		client:     &http.Client{Timeout: 10 * time.Second},
	}
}

func (n *FeishuNotifier) Send(alert Alert) error {
	// Feishu card format
	card := map[string]interface{}{
		"msg_type": "interactive",
		"card": map[string]interface{}{
			"header": map[string]interface{}{
				"title": map[string]interface{}{
					"tag":     "plain_text",
					"content": alert.Title,
				},
				"template": n.getTemplate(alert.Severity),
			},
			"elements": []map[string]interface{}{
				{
					"tag": "div",
					"text": map[string]interface{}{
						"tag": "lark_md",
						"content": fmt.Sprintf("**告警内容**: %s\\n\\n**指标值**: %.2f\\n\\n**触发时间**: %s",
							alert.Message, alert.Value, alert.Timestamp.Format("2006-01-02 15:04:05")),
					},
				},
				{
					"tag": "hr",
				},
				{
					"tag": "note",
					"elements": []map[string]interface{}{
						{
							"tag":     "plain_text",
							"content": "InferenceHub v3 告警系统",
						},
					},
				},
			},
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
		return fmt.Errorf("feishu webhook returned status %d", resp.StatusCode)
	}

	return nil
}

func (n *FeishuNotifier) getTemplate(severity string) string {
	switch severity {
	case "critical":
		return "red"
	case "warning":
		return "orange"
	default:
		return "blue"
	}
}
