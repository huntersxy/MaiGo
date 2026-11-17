package host

// 宿主对外 HTTP 的防爬虫与客户端 IP 解析，对齐 src/host/anti_crawler.py。

import (
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

var crawlerUserAgents = []string{
	"googlebot", "bingbot", "baiduspider", "yandexbot", "slurp", "duckduckbot", "sogou",
	"exabot", "facebot", "ia_archiver", "crawler", "spider", "scraper", "wget", "scrapy",
	"masscan", "nmap", "nikto", "sqlmap",
}

var assetScannerUserAgents = []string{
	"shodan", "censys", "zoomeye", "fofa", "quake", "hunter", "binaryedge", "onyphe",
	"securitytrails", "virustotal", "passivetotal", "acunetix", "appscan", "burpsuite",
	"nessus", "openvas", "qualys", "rapid7", "tenable", "veracode", "zap", "awvs",
	"netsparker", "skipfish", "w3af", "arachni", "masscan", "zmap", "nmap", "whatweb",
	"wpscan", "joomscan", "dnsenum", "subfinder", "amass", "sublist3r", "theharvester",
}

var scannerHeaderTools = map[string][]string{
	"x-scan":           {"shodan", "censys", "zoomeye", "fofa"},
	"x-scanner":        {"nmap", "masscan", "zmap"},
	"x-probe":          {"masscan", "zmap"},
	"x-originating-ip": {},
	"x-remote-ip":      {},
	"x-remote-addr":    {},
}

var staticAntiCrawlerExtensions = []string{
	".css", ".js", ".png", ".jpg", ".jpeg", ".gif", ".svg", ".ico", ".woff", ".woff2", ".ttf", ".eot",
}

const robotsTXT = "User-agent: *\nDisallow: /\n\n# 禁止所有爬虫访问\n"

type antiCrawlerModeConfig struct {
	enabled           bool
	rateLimitWindow   time.Duration
	rateLimitMax      int
	maxTrackedIPs     int
	checkUserAgent    bool
	checkAssetScanner bool
	checkRateLimit    bool
	blockOnDetect     bool
}

func antiCrawlerConfigFor(mode string) antiCrawlerModeConfig {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "false":
		return antiCrawlerModeConfig{enabled: false, rateLimitWindow: time.Minute, rateLimitMax: 1000}
	case "strict":
		return antiCrawlerModeConfig{enabled: true, rateLimitWindow: time.Minute, rateLimitMax: 15, maxTrackedIPs: 20000,
			checkUserAgent: true, checkAssetScanner: true, checkRateLimit: true, blockOnDetect: true}
	case "loose":
		return antiCrawlerModeConfig{enabled: true, rateLimitWindow: time.Minute, rateLimitMax: 60, maxTrackedIPs: 5000,
			checkUserAgent: true, checkAssetScanner: true, checkRateLimit: true, blockOnDetect: true}
	default:
		return antiCrawlerModeConfig{enabled: true, rateLimitWindow: time.Minute, rateLimitMax: 1000,
			checkUserAgent: true, checkAssetScanner: true}
	}
}

// ipRule 精确地址、CIDR 或 IPv4 通配符
type ipRule struct {
	address *net.IP
	network *net.IPNet
	pattern *regexp.Regexp
}

func parseIPRules(raw string) []ipRule {
	rules := make([]ipRule, 0)
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" || strings.HasPrefix(entry, "#") {
			continue
		}
		if strings.Contains(entry, "*") {
			if pattern := wildcardIPPattern(entry); pattern != nil {
				rules = append(rules, ipRule{pattern: pattern})
			}
			continue
		}
		if strings.Contains(entry, "/") {
			if _, network, err := net.ParseCIDR(entry); err == nil {
				rules = append(rules, ipRule{network: network})
			}
			continue
		}
		if address := net.ParseIP(entry); address != nil {
			rules = append(rules, ipRule{address: &address})
		}
	}
	return rules
}

func wildcardIPPattern(raw string) *regexp.Regexp {
	if raw == "*" {
		return regexp.MustCompile(`.*`)
	}
	parts := strings.Split(raw, ".")
	if len(parts) > 4 {
		return nil
	}
	compiled := make([]string, 0, 4)
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "*" {
			compiled = append(compiled, `\d+`)
			continue
		}
		if part == "" || !allDigits(part) {
			return nil
		}
		number := 0
		for _, char := range part {
			number = number*10 + int(char-'0')
		}
		if number > 255 {
			return nil
		}
		compiled = append(compiled, regexp.QuoteMeta(part))
	}
	for len(compiled) < 4 {
		compiled = append(compiled, `\d+`)
	}
	pattern, err := regexp.Compile("^" + strings.Join(compiled, `\.`) + "$")
	if err != nil {
		return nil
	}
	return pattern
}

func allDigits(value string) bool {
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return value != ""
}

func ipMatches(rules []ipRule, raw string) bool {
	address := net.ParseIP(strings.TrimSpace(raw))
	for _, rule := range rules {
		switch {
		case rule.pattern != nil:
			if rule.pattern.MatchString(raw) {
				return true
			}
		case rule.network != nil && address != nil && rule.network.Contains(address):
			return true
		case rule.address != nil && address != nil && rule.address.Equal(address):
			return true
		}
	}
	return false
}

// ClientIPResolver 按可信代理配置解析客户端 IP
type ClientIPResolver struct {
	trusted  []ipRule
	trustXFF bool
}

// NewClientIPResolver 构造解析器
func NewClientIPResolver(trustedProxies string, trustXFF bool) *ClientIPResolver {
	return &ClientIPResolver{trusted: parseIPRules(trustedProxies), trustXFF: trustXFF}
}

// Resolve 返回可信客户端 IP；无法判定时为 unknown
func (r *ClientIPResolver) Resolve(request *http.Request) string {
	direct := ""
	if host, _, err := net.SplitHostPort(request.RemoteAddr); err == nil {
		direct = host
	} else {
		direct = request.RemoteAddr
	}
	useForwarded := r.trustXFF && len(r.trusted) > 0 && net.ParseIP(direct) != nil && ipMatches(r.trusted, direct)
	if useForwarded {
		if forwarded := request.Header.Get("X-Forwarded-For"); forwarded != "" {
			candidate := strings.TrimSpace(strings.Split(forwarded, ",")[0])
			if net.ParseIP(candidate) != nil {
				return candidate
			}
		}
		if realIP := strings.TrimSpace(request.Header.Get("X-Real-IP")); realIP != "" && net.ParseIP(realIP) != nil {
			return realIP
		}
	}
	if net.ParseIP(direct) != nil {
		return direct
	}
	return "unknown"
}

// AntiCrawler 防爬虫中间件
type AntiCrawler struct {
	config   antiCrawlerModeConfig
	allowed  []ipRule
	resolver *ClientIPResolver
	auth     func(*http.Request) bool

	mu           sync.Mutex
	requestTimes map[string][]time.Time
	lastCleanup  time.Time
}

// NewAntiCrawler 按宿主配置创建中间件
func NewAntiCrawler(config HostConfig, resolver *ClientIPResolver, authenticated func(*http.Request) bool) *AntiCrawler {
	return &AntiCrawler{
		config: antiCrawlerConfigFor(config.AntiCrawlerMode), allowed: parseIPRules(config.AllowedIPs),
		resolver: resolver, auth: authenticated, requestTimes: map[string][]time.Time{},
	}
}

func (a *AntiCrawler) middleware(next http.Handler) http.Handler {
	if a == nil || !a.config.enabled {
		return next
	}
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/robots.txt" {
			next.ServeHTTP(rw, r)
			return
		}
		if isAntiCrawlerStatic(path) {
			next.ServeHTTP(rw, r)
			return
		}
		clientIP := a.resolver.Resolve(r)
		if ipMatches(a.allowed, clientIP) || (a.auth != nil && a.auth(r)) {
			next.ServeHTTP(rw, r)
			return
		}
		userAgent := r.Header.Get("User-Agent")
		if a.config.checkAssetScanner && detectAssetScanner(r, userAgent) {
			if a.config.blockOnDetect {
				http.Error(rw, "Access Denied: Asset scanning tools are not allowed", http.StatusForbidden)
				return
			}
		}
		if a.config.checkUserAgent && containsAny(userAgent, crawlerUserAgents) {
			if a.config.blockOnDetect {
				http.Error(rw, "Access Denied: Crawlers are not allowed", http.StatusForbidden)
				return
			}
		}
		if a.config.checkRateLimit && a.rateLimited(clientIP) {
			http.Error(rw, "Too Many Requests: Rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(rw, r)
	})
}

func isAntiCrawlerStatic(path string) bool {
	static := false
	for _, prefix := range []string{"/static/", "/assets/", "/dist/"} {
		if strings.HasPrefix(path, prefix) {
			static = true
			break
		}
	}
	if static && hasAnySuffix(path, staticAntiCrawlerExtensions) {
		return true
	}
	return strings.Count(path, "/") == 1 && hasAnySuffix(path, staticAntiCrawlerExtensions)
}

func hasAnySuffix(value string, suffixes []string) bool {
	lower := strings.ToLower(value)
	for _, suffix := range suffixes {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

func containsAny(value string, keywords []string) bool {
	lower := strings.ToLower(value)
	for _, keyword := range keywords {
		if strings.Contains(lower, keyword) {
			return true
		}
	}
	return false
}

func detectAssetScanner(request *http.Request, userAgent string) bool {
	if containsAny(userAgent, assetScannerUserAgents) {
		return true
	}
	for name, values := range scannerHeaderTools {
		header := strings.ToLower(request.Header.Get(name))
		if header == "" {
			continue
		}
		if len(values) == 0 {
			return true
		}
		for _, value := range values {
			if strings.Contains(header, value) {
				return true
			}
		}
	}
	return false
}

func (a *AntiCrawler) rateLimited(clientIP string) bool {
	if ipMatches(a.allowed, clientIP) {
		return false
	}
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	if now.Sub(a.lastCleanup) > 5*time.Minute {
		a.cleanupLocked(now)
		a.lastCleanup = now
	}
	if a.config.maxTrackedIPs > 0 && len(a.requestTimes) > a.config.maxTrackedIPs {
		a.cleanupOldestLocked()
	}
	history := a.requestTimes[clientIP]
	cutoff := now.Add(-a.config.rateLimitWindow)
	kept := history[:0]
	for _, stamp := range history {
		if stamp.After(cutoff) {
			kept = append(kept, stamp)
		}
	}
	if len(kept) >= a.config.rateLimitMax {
		a.requestTimes[clientIP] = kept
		return true
	}
	a.requestTimes[clientIP] = append(kept, now)
	return false
}

func (a *AntiCrawler) cleanupLocked(now time.Time) {
	cutoff := now.Add(-a.config.rateLimitWindow)
	for key, history := range a.requestTimes {
		if len(history) == 0 || !history[len(history)-1].After(cutoff) {
			delete(a.requestTimes, key)
		}
	}
}

func (a *AntiCrawler) cleanupOldestLocked() {
	empty := make([]string, 0)
	oldestKey := ""
	var oldest time.Time
	for key, history := range a.requestTimes {
		if len(history) == 0 {
			empty = append(empty, key)
			continue
		}
		if oldestKey == "" || history[0].Before(oldest) {
			oldestKey, oldest = key, history[0]
		}
	}
	for _, key := range empty {
		delete(a.requestTimes, key)
	}
	if len(empty) == 0 && oldestKey != "" {
		delete(a.requestTimes, oldestKey)
	}
}
