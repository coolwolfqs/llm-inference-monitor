package shared

import (
	"bufio"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// NewAPIParser parses NewAPI Docker logs for IP-based token statistics
type NewAPIParser struct {
	mu       sync.Mutex
	lines    []string
	lastScan time.Time
	cacheTTL time.Duration
}

// IPTokenStat holds per-IP token usage
type IPTokenStat struct {
	IP           string `json:"ip"`
	PromptTokens int    `json:"prompt_tokens"`
	EvalTokens   int    `json:"eval_tokens"`
	TotalTokens  int    `json:"total_tokens"`
	Count        int    `json:"count"`
}

// NewAPIIPStats holds the complete IP token statistics
type NewAPIIPStats struct {
	Stats []IPTokenStat `json:"stats"`
	Total int           `json:"total_requests"`
}

// RequestSource and IPStat are defined in log_parser.go

func NewNewAPIParser() *NewAPIParser {
	return &NewAPIParser{
		cacheTTL: 10 * time.Second,
	}
}

// GetNewAPILines retrieves the last n lines from NewAPI Docker logs
func (p *NewAPIParser) GetNewAPILines(n int) []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	if !p.lastScan.IsZero() && now.Sub(p.lastScan) < p.cacheTTL && len(p.lines) > 0 {
		if n <= len(p.lines) {
			return p.lines[len(p.lines)-n:]
		}
		return p.lines
	}

	cmd := exec.Command("sh", "-c",
		"docker logs new-api --tail "+strconv.Itoa(n)+" 2>&1")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	var lines []string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	p.lines = lines
	p.lastScan = now

	if n <= len(lines) {
		return lines[len(lines)-n:]
	}
	return lines
}

// GetIPTokenStats computes per-IP token usage from NewAPI logs
func (p *NewAPIParser) GetIPTokenStats() NewAPIIPStats {
	lines := p.GetNewAPILines(500)
	if len(lines) == 0 {
		return NewAPIIPStats{Stats: []IPTokenStat{}, Total: 0}
	}

	type ipData struct {
		promptTokens int
		evalTokens   int
		count        int
	}
	ipMap := make(map[string]*ipData)
	totalReqs := 0

	// Build a map of request_id -> IP from GIN lines
	reqIDToIP := make(map[string]string)
	ginRe := regexp.MustCompile(`\[GIN\].*?\|\s*(\S+)\s*\|`)
	ipRe := regexp.MustCompile(`\d+\.\d+\.\d+\.\d+`)

	for _, line := range lines {
		if strings.Contains(line, "[GIN]") {
			if m := ginRe.FindStringSubmatch(line); len(m) > 1 {
				parts := strings.Split(line, "|")
				if len(parts) >= 6 {
					ip := strings.TrimSpace(parts[5])
					if ipRe.MatchString(ip) {
						reqIDToIP[m[1]] = ip
					}
				}
			}
		}
	}

	// Parse consume log entries
	consumeRe := regexp.MustCompile(`prompt_tokens.:(\d+)`)
	evalRe := regexp.MustCompile(`completion_tokens.:(\d+)`)
	reqIDLineRe := regexp.MustCompile(`\|\s*(\S+)\s*,?\s*\|\s*record consume log:`)

	for _, line := range lines {
		if !strings.Contains(line, "record consume log:") {
			continue
		}

		promptMatch := consumeRe.FindStringSubmatch(line)
		evalMatch := evalRe.FindStringSubmatch(line)
		if promptMatch == nil || evalMatch == nil {
			continue
		}

		promptTokens, _ := strconv.Atoi(promptMatch[1])
		evalTokens, _ := strconv.Atoi(evalMatch[1])

		// Find IP from request ID
		ip := "unknown"
		if reqIDMatch := reqIDLineRe.FindStringSubmatch(line); len(reqIDMatch) > 1 {
			reqID := strings.TrimSuffix(reqIDMatch[1], ",")
			if mappedIP, ok := reqIDToIP[reqID]; ok {
				ip = mappedIP
			}
		}

		if _, ok := ipMap[ip]; !ok {
			ipMap[ip] = &ipData{}
		}
		ipMap[ip].promptTokens += promptTokens
		ipMap[ip].evalTokens += evalTokens
		ipMap[ip].count++
		totalReqs++
	}

	// Convert to sorted slice
	var stats []IPTokenStat
	for ip, data := range ipMap {
		stats = append(stats, IPTokenStat{
			IP:           ip,
			PromptTokens: data.promptTokens,
			EvalTokens:   data.evalTokens,
			TotalTokens:  data.promptTokens + data.evalTokens,
			Count:        data.count,
		})
	}

	// Sort by total tokens descending (simple bubble sort for small N)
	for i := 0; i < len(stats); i++ {
		for j := i + 1; j < len(stats); j++ {
			if stats[j].TotalTokens > stats[i].TotalTokens {
				stats[i], stats[j] = stats[j], stats[i]
			}
		}
	}

	// Limit to top 5
	if len(stats) > 5 {
		stats = stats[:5]
	}

	return NewAPIIPStats{Stats: stats, Total: totalReqs}
}

// GetRequestSources parses NewAPI logs for request source IPs (3-hour window)
func (p *NewAPIParser) GetRequestSources() map[string]int {
	lines := p.GetNewAPILines(2000)
	sources := make(map[string]int)

	ginRe := regexp.MustCompile(`\[GIN\]`)
	completionsRe := regexp.MustCompile(`/v1/chat/completions`)
	okRe := regexp.MustCompile(`\b200\b`)
	ipRe := regexp.MustCompile(`\d+\.\d+\.\d+\.\d+`)

	for _, line := range lines {
		if ginRe.MatchString(line) && completionsRe.MatchString(line) && okRe.MatchString(line) {
			parts := strings.Split(line, "|")
			if len(parts) >= 6 {
				ip := strings.TrimSpace(parts[5])
				if ipRe.MatchString(ip) {
					sources[ip]++
				}
			}
		}
	}
	return sources
}

// Suppress unused import warnings
var _ = bufio.NewScanner
