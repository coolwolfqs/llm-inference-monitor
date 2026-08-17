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
	anchorLogTime := ""
	for i := len(allLines) - 1; i >= 0; i-- {
		if match := logTimeRe.FindStringSubmatch(allLines[i]); len(match) >= 2 {
			anchorLogTime = match[1]
			break
		}
	}

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
		var evalMs float64
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
					evalMs, _ = strconv.ParseFloat(em[1], 64)
					evalTokens, _ = strconv.Atoi(em[2])
					// Only eval timing carries generation throughput. Prompt
					// processing progress lines must not overwrite this value.
					if tm := logTPSRe.FindStringSubmatch(nextLine); len(tm) >= 2 {
						parsedTPS, _ := strconv.ParseFloat(tm[1], 64)
						if IsReliableEvalSample(evalMs, evalTokens, parsedTPS) {
							tps = parsedTPS
						}
					}
				}
			}
		}

		tm := logTimeRe.FindStringSubmatch(line)
		logTimeStr := ""
		eventTimestamp := time.Now().Unix() - int64(len(allLines)-i)*2
		if len(tm) >= 2 {
			if wallTime, ok := logWallTime(tm[1], anchorLogTime, logFileModTime); ok {
				eventTimestamp = wallTime.Unix()
				logTimeStr = wallTime.Local().Format("15:04:05")
			}
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
			Timestamp:    eventTimestamp,
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

func parseLogMonotonic(ts string) (float64, bool) {
	// llama-server timestamps use minutes.seconds.milliseconds.microseconds,
	// for example 1083.11.912.794 means 1083m 11.912794s since startup.
	parts := strings.Split(ts, ".")
	if len(parts) < 2 {
		return 0, false
	}
	minutes, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return 0, false
	}
	seconds, err := strconv.ParseFloat(parts[1], 64)
	if err != nil {
		return 0, false
	}
	fractionScale := 1.0
	for _, fraction := range parts[2:] {
		fractionScale *= 1000
		value, parseErr := strconv.ParseFloat(fraction, 64)
		if parseErr != nil {
			return 0, false
		}
		seconds += value / fractionScale
	}
	return minutes*60 + seconds, true
}

func logWallTime(ts string, anchorTs string, logFileModTime int64) (time.Time, bool) {
	if logFileModTime == 0 || anchorTs == "" {
		return time.Time{}, false
	}
	current, ok := parseLogMonotonic(ts)
	if !ok {
		return time.Time{}, false
	}
	anchor, ok := parseLogMonotonic(anchorTs)
	if !ok || current > anchor {
		return time.Time{}, false
	}
	delta := time.Duration((anchor - current) * float64(time.Second))
	return time.Unix(logFileModTime, 0).Add(-delta), true
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
