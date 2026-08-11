package shared

import (
	"bufio"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type IPStat struct {
	IP           string `json:"ip"`
	Count        int    `json:"count"`
	PromptTokens int    `json:"prompt_tokens"`
	EvalTokens   int    `json:"eval_tokens"`
	TotalTokens  int    `json:"total_tokens"`
}

type RequestSource struct {
	Sources map[string]int `json:"sources"`
	Total   int            `json:"total"`
}

var logTimingRe = regexp.MustCompile(`total time =\s+([\d.]+)\s+ms\s*/\s*([0-9]+)\s+tokens`)
var logPromptRe = regexp.MustCompile(`prompt eval time =\s+([\d.]+)\s+ms\s*/\s*([0-9]+)\s+tokens`)
var logEvalRe = regexp.MustCompile(`eval time =\s+([\d.]+)\s+ms\s*/\s*([0-9]+)\s+tokens`)
var logTPSRe = regexp.MustCompile(`([\d.]+)\s+tokens per second`)
var logTimeRe = regexp.MustCompile(`^\s*([\d.]+)\s+I\s+slot\s+print_timing:`)
var draftAcceptRe = regexp.MustCompile(`draft acceptance = ([0-9.]+)\s*\(\s*([0-9]+)\s+accepted\s*/\s*([0-9]+)\s+generated\)`)

// DraftAcceptResult holds parsed draft acceptance data from logs
type DraftAcceptResult struct {
	Rate      float64
	Accepted  int
	Generated int
}

// ParseDraftAcceptance reads from the end of the log file and returns the latest draft acceptance.
// Uses reverse file reading to handle large log files with frequent idle messages.
func ParseDraftAcceptance(logPath string) DraftAcceptResult {
	f, err := os.Open(logPath)
	if err != nil {
		return DraftAcceptResult{}
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return DraftAcceptResult{}
	}

	fileSize := stat.Size()
	// Read last 512KB of file (enough to cover thousands of lines)
	readSize := int64(512 * 1024)
	if fileSize < readSize {
		readSize = fileSize
	}

	buf := make([]byte, readSize)
	_, err = f.ReadAt(buf, fileSize-readSize)
	if err != nil && err.Error() != "EOF" {
		return DraftAcceptResult{}
	}

	// Search backwards in the buffer for draft acceptance
	text := string(buf)
	lastIdx := len(text)
	for lastIdx > 0 {
		// Find last occurrence of "draft acceptance" by searching backwards
		searchEnd := lastIdx
		idx := strings.LastIndex(text[:searchEnd], "draft acceptance = ")
		if idx < 0 {
			break
		}
		// Extract line from idx to end of line
		lineEnd := strings.IndexByte(text[idx:], '\n')
		if lineEnd < 0 {
			lineEnd = len(text) - idx
		}
		line := text[idx : idx+lineEnd]

		if m := draftAcceptRe.FindStringSubmatch(line); len(m) >= 4 {
			rate, _ := strconv.ParseFloat(m[1], 64)
			accepted, _ := strconv.Atoi(m[2])
			generated, _ := strconv.Atoi(m[3])
			return DraftAcceptResult{Rate: rate, Accepted: accepted, Generated: generated}
		}
		lastIdx = idx
	}
	return DraftAcceptResult{}
}

func ParseLlamaLogs(logPath string, maxEntries int) ([]LogEntry, []IPStat, RequestSource) {
	return ParseLlamaLogsEx(logPath, maxEntries, 0)
}

func ParseLlamaLogsEx(logPath string, maxEntries int, logFileModTime int64) ([]LogEntry, []IPStat, RequestSource) {
	f, err := os.Open(logPath)
	if err != nil {
		return nil, nil, RequestSource{Sources: map[string]int{}, Total: 0}
	}
	defer f.Close()

	// Only read tail of file (last 500 lines) for performance with large logs
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 256*1024), 256*1024)
	const maxTailLines = 500
	allLines := make([]string, 0, maxTailLines)
	for scanner.Scan() {
		allLines = append(allLines, scanner.Text())
		if len(allLines) > maxTailLines {
			allLines = allLines[1:]
		}
	}

	var entries []LogEntry
	ipMap := make(map[string]*IPStat)
	sourceMap := make(map[string]int)
	totalRequests := 0

	count := 0
	for i := len(allLines) - 1; i >= 0 && count < maxEntries; i-- {
		line := allLines[i]
		if !strings.Contains(line, "total time =") {
			continue
		}

		totalMatch := logTimingRe.FindStringSubmatch(line)
		if len(totalMatch) < 3 {
			continue
		}

		durationMs, _ := strconv.ParseFloat(totalMatch[1], 64)
		totalTokens, _ := strconv.Atoi(totalMatch[2])

		var promptMs float64
		var promptTokens int
		var evalTokens int
		var tps float64

		// LOOK BACKWARDS for prompt/eval lines
		for j := i - 1; j >= 0 && j > i-5; j-- {
			nextLine := allLines[j]
			if pm := logPromptRe.FindStringSubmatch(nextLine); len(pm) >= 3 {
				promptMs, _ = strconv.ParseFloat(pm[1], 64)
				promptTokens, _ = strconv.Atoi(pm[2])
			}
			if !strings.Contains(nextLine, "prompt eval") {
				if em := logEvalRe.FindStringSubmatch(nextLine); len(em) >= 3 {
					evalTokens, _ = strconv.Atoi(em[2])
					// Only eval timing carries generation throughput. Prompt
					// processing progress lines must not overwrite this value.
					if tm := logTPSRe.FindStringSubmatch(nextLine); len(tm) >= 2 {
						tps, _ = strconv.ParseFloat(tm[1], 64)
					}
				}
			}
		}

		tm := logTimeRe.FindStringSubmatch(line)
		logTimeStr := ""
		if len(tm) >= 2 {
			logTimeStr = formatLogTime(tm[1], logFileModTime)
		}

		// 格式化时间毫秒
		timeMsStr := strconv.FormatFloat(durationMs, 'f', 0, 64)

		// 向前搜索 done request 行获取IP和详情
		var detailStr string
		var sourceIP string
		for j := i - 1; j >= 0 && j > i-20; j-- {
			drLine := allLines[j]
			if strings.Contains(drLine, "done request:") {
				detailStr = drLine
				// 提取IP: "done request: POST /v1/chat 10.1.1.5 200"
				drParts := strings.Fields(drLine)
				if len(drParts) >= 6 {
					sourceIP = drParts[len(drParts)-2]
				}
				break
			}
		}

		// 如果没有done request，合成一个
		if detailStr == "" {
			detailStr = "done request: POST /v1/chat/completions local 200"
			sourceIP = "local"
		}

		entry := LogEntry{
			Timestamp:    time.Now().Unix() - int64(len(allLines)-i)*2,
			Time:         logTimeStr,
			Type:         "req",
			Path:         "/v1/chat/completions",
			Status:       "200",
			TimeMs:       timeMsStr,
			TPS:          tps,
			Tokens:       totalTokens,
			SourceIP:     sourceIP,
			Detail:       detailStr,
			PromptMs:     promptMs,
			PromptTokens: promptTokens,
			EvalTokens:   evalTokens,
		}
		entries = append(entries, entry)
		count++

		ipKey := sourceIP
		if ipKey == "" {
			ipKey = "local"
		}
		if stat, ok := ipMap[ipKey]; ok {
			stat.Count++
			stat.PromptTokens += promptTokens
			stat.EvalTokens += evalTokens
			stat.TotalTokens += totalTokens
		} else {
			ipMap[ipKey] = &IPStat{
				IP: ipKey, Count: 1,
				PromptTokens: promptTokens, EvalTokens: evalTokens, TotalTokens: totalTokens,
			}
		}
		sourceMap["direct"]++
		totalRequests++
	}

	var ipStats []IPStat
	for _, stat := range ipMap {
		ipStats = append(ipStats, *stat)
	}

	return entries, ipStats, RequestSource{Sources: sourceMap, Total: totalRequests}
}

func formatLogTime(ts string, logFileModTime int64) string {
	// llama-server时间戳格式: "1014.08.518.689" = 服务器启动秒数
	// 通过日志文件修改时间近似计算墙钟时间
	parts := strings.Split(ts, ".")
	if len(parts) < 1 {
		return ts
	}
	sec, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return ts
	}
	// 计算：日志文件修改时间 - (最后时间戳 - 当前时间戳)
	// 简化：使用logFileModTime作为参考点，sec作为相对偏移
	refTime := logFileModTime
	if refTime == 0 {
		return ts
	}
	// 将时间戳秒数转换为参考点附近的时间
	t := time.Unix(refTime-int64(sec), 0)
	return t.Format("15:04:05")
}
func parseIntHint(s string) int {
	v, _ := strconv.Atoi(s)
	return v
}

func ParseIntFromScript(script, flag string, defaultVal int) int {
	idx := strings.Index(script, flag)
	if idx < 0 {
		return defaultVal
	}
	rest := strings.TrimSpace(script[idx+len(flag):])
	end := strings.IndexAny(rest, " \n\r")
	if end < 0 {
		end = len(rest)
	}
	v, err := strconv.Atoi(rest[:end])
	if err != nil {
		return defaultVal
	}
	return v
}

func ParseStringFromScript(script, flag, defaultVal string) string {
	idx := strings.Index(script, flag)
	if idx < 0 {
		return defaultVal
	}
	rest := strings.TrimSpace(script[idx+len(flag):])
	end := strings.IndexAny(rest, " \n\r")
	if end < 0 {
		end = len(rest)
	}
	if rest[:end] == "" {
		return defaultVal
	}
	return rest[:end]
}

func ParseFloatFromScript(script, flag string, defaultVal float64) float64 {
	idx := strings.Index(script, flag)
	if idx < 0 {
		return defaultVal
	}
	rest := strings.TrimSpace(script[idx+len(flag):])
	end := strings.IndexAny(rest, " \n\r")
	if end < 0 {
		end = len(rest)
	}
	v, err := strconv.ParseFloat(rest[:end], 64)
	if err != nil {
		return defaultVal
	}
	return v
}
