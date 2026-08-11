package shared

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// NginxLogEntry 单条 nginx JSON access log
type NginxLogEntry struct {
	Time      string  `json:"time"`
	IP        string  `json:"ip"`
	Method    string  `json:"method"`
	Path      string  `json:"path"`
	Status    int     `json:"status"`
	BodyBytes int     `json:"body_bytes"`
	ReqTime   float64 `json:"request_time"`
	Upstream  string  `json:"upstream_response_time"`
}

// ParseNginxAccessLog 解析 nginx JSON access log
// 返回: IP统计, 请求来源统计, 最近的 LogEntry 列表
func ParseNginxAccessLog(logPath string, maxLines int) ([]IPStat, RequestSource, []LogEntry) {
	f, err := os.Open(logPath)
	if err != nil {
		return nil, RequestSource{Sources: map[string]int{}, Total: 0}, nil
	}
	defer f.Close()

	var allLines []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	for scanner.Scan() {
		allLines = append(allLines, scanner.Text())
	}
	total := len(allLines)
	start := 0
	if total > maxLines {
		start = total - maxLines
	}
	lines := allLines[start:]

	ipMap := make(map[string]*IPStat)
	sourceMap := make(map[string]int)
	totalRequests := 0
	var entries []LogEntry

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || line[0] != '{' {
			continue
		}

		var e NginxLogEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}

		ip := e.IP
		if ip == "" {
			ip = "unknown"
		}
		totalRequests++

		// IP 统计
		if stat, ok := ipMap[ip]; ok {
			stat.Count++
		} else {
			ipMap[ip] = &IPStat{IP: ip, Count: 1}
		}

		// 来源分类
		src := classifySource(e.Path)
		sourceMap[src]++

		// 构建 LogEntry
		le := LogEntry{
			Time:     e.Time,
			Type:     "req",
			Path:     e.Path,
			Status:   fmt.Sprintf("%d", e.Status),
			SourceIP: ip,
			Detail:   e.Method + " " + e.Path,
		}
		entries = append(entries, le)
	}

	var ipStats []IPStat
	for _, stat := range ipMap {
		ipStats = append(ipStats, *stat)
	}

	return ipStats, RequestSource{Sources: sourceMap, Total: totalRequests}, entries
}

// classifySource 根据请求路径分类来源
func classifySource(path string) string {
	switch {
	case strings.HasPrefix(path, "/v1/"):
		return "api"
	case path == "/slots" || path == "/stats" || path == "/metrics":
		return "internal"
	case path == "/health" || path == "/props":
		return "health"
	default:
		return "other"
	}
}
