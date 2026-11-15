package app

import (
	"io"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"MaiGo/internal/webui/middleware"
)

const (
	antiCrawlerRobotsBody = "User-agent: *\nDisallow: /\n\n# 禁止所有爬虫访问\n"

	antiCrawlerScannerBody = "Access Denied: Asset scanning tools are not allowed"
	antiCrawlerCrawlerBody = "Access Denied: Crawlers are not allowed"
	antiCrawlerRateBody    = "Too Many Requests: Rate limit exceeded"
)

// crawlerUserAgents 对齐 MaiBot src/webui/middleware/anti_crawler.py:22-61
var crawlerUserAgents = []string{
	"googlebot", "bingbot", "baiduspider", "yandexbot", "slurp",
	"duckduckbot", "sogou", "exabot", "facebot", "ia_archiver",
	"crawler", "spider", "scraper", "wget", "scrapy", "masscan", "nmap", "nikto", "sqlmap",
}

// assetScannerUserAgents 对齐 anti_crawler.py:63-106
var assetScannerUserAgents = []string{
	"shodan", "censys", "zoomeye", "fofa", "quake", "hunter", "binaryedge", "onyphe",
	"securitytrails", "virustotal", "passivetotal", "acunetix", "appscan", "burpsuite",
	"nessus", "openvas", "qualys", "rapid7", "tenable", "veracode", "zap", "awvs",
	"netsparker", "skipfish", "w3af", "arachni", "masscan", "zmap", "nmap", "whatweb",
	"wpscan", "joomscan", "dnsenum", "subfinder", "amass", "sublist3r", "theharvester",
}

var antiCrawlerStaticExtensions = []string{
	".css", ".js", ".png", ".jpg", ".jpeg", ".gif", ".svg", ".ico",
	".woff", ".woff2", ".ttf", ".eot",
}

var antiCrawlerStaticPrefixes = []string{"/static/", "/assets/", "/dist/"}

// antiCrawlerModeSettings 对齐 anti_crawler.py:266-329
func antiCrawlerModeSettingsFor(mode string) antiCrawlerModeSettings {
	switch normalizeAntiCrawlerMode(mode) {
	case "false":
		return antiCrawlerModeSettings{}
	case "strict":
		return antiCrawlerModeSettings{
			enabled: true, window: 60 * time.Second, maxRequests: 15, maxTrackedIPs: 20000,
			checkUserAgent: true, checkAssetScanner: true, checkRateLimit: true, blockOnDetect: true,
		}
	case "loose":
		return antiCrawlerModeSettings{
			enabled: true, window: 60 * time.Second, maxRequests: 60, maxTrackedIPs: 5000,
			checkUserAgent: true, checkAssetScanner: true, checkRateLimit: true, blockOnDetect: true,
		}
	default:
		return antiCrawlerModeSettings{
			enabled: true, window: 60 * time.Second, maxRequests: 1000, maxTrackedIPs: 0,
			checkUserAgent: true, checkAssetScanner: true, checkRateLimit: false, blockOnDetect: false,
		}
	}
}

type antiCrawlerModeSettings struct {
	enabled           bool
	window            time.Duration
	maxRequests       int
	maxTrackedIPs     int
	checkUserAgent    bool
	checkAssetScanner bool
	checkRateLimit    bool
	blockOnDetect     bool
}

type antiCrawlerMiddleware struct {
	settings antiCrawlerModeSettings
	allowed  []ipRule
	trusted  []ipRule
	trustXFF bool
	auth     *auth.Manager

	mu           sync.Mutex
	requestTimes map[string][]time.Time
	lastCleanup  time.Time
	now          func() time.Time
}

func newAntiCrawlerMiddleware(cfg antiCrawlerConfig, authManager *auth.Manager) *antiCrawlerMiddleware {
	m := &antiCrawlerMiddleware{
		settings:     antiCrawlerModeSettingsFor(cfg.mode),
		allowed:      parseIPRules(cfg.allowedIPs),
		trusted:      parseIPRules(cfg.trustedProxies),
		trustXFF:     cfg.trustXFF,
		auth:         authManager,
		requestTimes: make(map[string][]time.Time),
		now:          time.Now,
	}
	m.lastCleanup = m.now()
	return m
}

func (m *antiCrawlerMiddleware) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m == nil || !m.settings.enabled {
			next.ServeHTTP(w, r)
			return
		}

		path := r.URL.Path
		if path == "/robots.txt" || isAntiCrawlerStaticPath(path) {
			next.ServeHTTP(w, r)
			return
		}

		clientIP := m.clientIP(r)
		if m.ipAllowed(clientIP) {
			next.ServeHTTP(w, r)
			return
		}
		if m.hasValidSession(r) {
			next.ServeHTTP(w, r)
			return
		}

		userAgent := r.Header.Get("User-Agent")
		if m.settings.checkAssetScanner {
			if ok, tool := detectAssetScanner(r); ok {
				zap.L().Warn("检测到资产测绘工具请求", zap.String("ip", clientIP), zap.String("tool", tool), zap.String("user_agent", userAgent), zap.String("path", path))
				if m.settings.blockOnDetect {
					writeAntiCrawlerText(w, http.StatusForbidden, antiCrawlerScannerBody)
					return
				}
			}
		}

		if m.settings.checkUserAgent && isCrawlerUserAgent(userAgent) {
			zap.L().Warn("检测到爬虫请求", zap.String("ip", clientIP), zap.String("user_agent", userAgent), zap.String("path", path))
			if m.settings.blockOnDetect {
				writeAntiCrawlerText(w, http.StatusForbidden, antiCrawlerCrawlerBody)
				return
			}
		}

		if m.settings.checkRateLimit && m.checkRateLimit(clientIP) {
			zap.L().Warn("请求频率过高", zap.String("ip", clientIP), zap.String("user_agent", userAgent), zap.String("path", path))
			writeAntiCrawlerText(w, http.StatusTooManyRequests, antiCrawlerRateBody)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (a *App) handleRobotsTxt(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = io.WriteString(w, antiCrawlerRobotsBody)
}

func writeAntiCrawlerText(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func isAntiCrawlerStaticPath(path string) bool {
	if !hasAntiCrawlerStaticExtension(path) {
		return false
	}
	for _, prefix := range antiCrawlerStaticPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return strings.Count(path, "/") == 1
}

func hasAntiCrawlerStaticExtension(path string) bool {
	for _, extension := range antiCrawlerStaticExtensions {
		if strings.HasSuffix(path, extension) {
			return true
		}
	}
	return false
}

func isCrawlerUserAgent(userAgent string) bool {
	if strings.TrimSpace(userAgent) == "" {
		return false
	}
	lower := strings.ToLower(userAgent)
	for _, keyword := range crawlerUserAgents {
		if strings.Contains(lower, keyword) {
			return true
		}
	}
	return false
}

// detectAssetScanner 对齐 anti_crawler.py:374-456：先 UA，再特定头
func detectAssetScanner(r *http.Request) (bool, string) {
	userAgent := strings.ToLower(r.Header.Get("User-Agent"))
	if userAgent != "" {
		for _, keyword := range assetScannerUserAgents {
			if strings.Contains(userAgent, keyword) {
				return true, keyword
			}
		}
	}

	headers := make([]string, 0, len(r.Header))
	for name := range r.Header {
		headers = append(headers, name)
	}
	sort.Strings(headers)
	for _, name := range headers {
		lowerName := strings.ToLower(name)
		values := r.Header.Values(name)
		for _, value := range values {
			if !isAntiCrawlerScannerHeader(lowerName, value) {
				continue
			}
			if tool := scannerToolFromHeader(lowerName, value); tool != "" {
				return true, tool
			}
			return true, "unknown_scanner"
		}
	}
	return false, ""
}

func isAntiCrawlerScannerHeader(name, value string) bool {
	lowerValue := strings.ToLower(value)
	var expected []string
	switch name {
	case "x-scan":
		expected = []string{"shodan", "censys", "zoomeye", "fofa"}
	case "x-scanner":
		expected = []string{"nmap", "masscan", "zmap"}
	case "x-probe":
		expected = []string{"masscan", "zmap"}
	case "x-originating-ip", "x-remote-ip", "x-remote-addr":
		return strings.TrimSpace(lowerValue) != ""
	default:
		return false
	}
	if containsAny(lowerValue, expected) || containsAny(lowerValue, assetScannerUserAgents) {
		return true
	}
	return false
}

func scannerToolFromHeader(name, value string) string {
	lowerValue := strings.ToLower(value)
	for _, tool := range assetScannerUserAgents {
		if strings.Contains(lowerValue, tool) {
			return tool
		}
	}
	return ""
}

func containsAny(value string, candidates []string) bool {
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}

func (m *antiCrawlerMiddleware) hasValidSession(r *http.Request) bool {
	if m.auth == nil {
		return false
	}
	cookie, err := r.Cookie(auth.WebUISessionCookieName)
	if err != nil {
		return false
	}
	return m.auth.ValidateToken(cookie.Value)
}

func (m *antiCrawlerMiddleware) clientIP(r *http.Request) string {
	direct := antiCrawlerRemoteHost(r.RemoteAddr)
	useXFF := false
	if m.trustXFF && len(m.trusted) > 0 && direct != "" {
		useXFF = ipRulesMatch(m.trusted, direct)
	}
	if useXFF {
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			first := strings.TrimSpace(strings.Split(forwarded, ",")[0])
			if _, err := netip.ParseAddr(first); err == nil {
				return first
			}
		}
		if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
			if _, err := netip.ParseAddr(realIP); err == nil {
				return realIP
			}
		}
	}
	if _, err := netip.ParseAddr(direct); err == nil {
		return direct
	}
	return "unknown"
}

func antiCrawlerRemoteHost(remoteAddr string) string {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return strings.Trim(remoteAddr, "[]")
}

func (m *antiCrawlerMiddleware) ipAllowed(ip string) bool {
	return ip != "unknown" && ipRulesMatch(m.allowed, ip)
}

func (m *antiCrawlerMiddleware) checkRateLimit(ip string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now()
	if now.Sub(m.lastCleanup) > 300*time.Second {
		m.cleanupOldRequestsLocked(now)
		m.lastCleanup = now
	}
	if m.settings.maxTrackedIPs > 0 && len(m.requestTimes) > m.settings.maxTrackedIPs {
		m.cleanupOldestIPsLocked()
	}

	times := m.requestTimes[ip]
	cutoff := now.Add(-m.settings.window)
	kept := times[:0]
	for _, timestamp := range times {
		if timestamp.After(cutoff) {
			kept = append(kept, timestamp)
		}
	}
	m.requestTimes[ip] = kept
	if len(kept) >= m.settings.maxRequests {
		return true
	}
	m.requestTimes[ip] = append(kept, now)
	return false
}

func (m *antiCrawlerMiddleware) cleanupOldRequestsLocked(now time.Time) {
	if len(m.requestTimes) > m.settings.maxTrackedIPs*8/10 {
		m.cleanupOldestIPsLocked()
	}
}

func (m *antiCrawlerMiddleware) cleanupOldestIPsLocked() {
	if len(m.requestTimes) == 0 {
		return
	}
	var empty []string
	oldestIP := ""
	var oldest time.Time
	for ip, times := range m.requestTimes {
		if len(times) == 0 {
			empty = append(empty, ip)
			continue
		}
		if oldestIP == "" || times[0].Before(oldest) {
			oldestIP = ip
			oldest = times[0]
		}
	}
	for _, ip := range empty {
		delete(m.requestTimes, ip)
	}
	if len(empty) == 0 && oldestIP != "" {
		delete(m.requestTimes, oldestIP)
	}
}

type ipRule struct {
	addr     netip.Addr
	prefix   netip.Prefix
	wildcard *regexp.Regexp
}

func parseIPRules(raw string) []ipRule {
	var rules []ipRule
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" || strings.HasPrefix(entry, "#") {
			continue
		}
		if strings.Contains(entry, "*") {
			pattern, ok := wildcardIPPattern(entry)
			if !ok {
				zap.L().Warn("忽略无效的防爬虫通配符 IP", zap.String("entry", entry))
				continue
			}
			rules = append(rules, ipRule{wildcard: pattern})
			continue
		}
		if strings.Contains(entry, "/") {
			prefix, err := netip.ParsePrefix(entry)
			if err != nil {
				zap.L().Warn("忽略无效的防爬虫 CIDR", zap.String("entry", entry), zap.Error(err))
				continue
			}
			// Python ip_network(strict=False) 会掩掉主机位，这里保持一致
			rules = append(rules, ipRule{prefix: prefix.Masked()})
			continue
		}
		addr, err := netip.ParseAddr(entry)
		if err != nil {
			zap.L().Warn("忽略无效的防爬虫 IP", zap.String("entry", entry), zap.Error(err))
			continue
		}
		rules = append(rules, ipRule{addr: addr})
	}
	return rules
}

func wildcardIPPattern(entry string) (*regexp.Regexp, bool) {
	if entry == "*" {
		return regexp.MustCompile(`.*`), true
	}
	parts := strings.Split(entry, ".")
	if len(parts) > 4 {
		return nil, false
	}
	for len(parts) < 4 {
		parts = append(parts, "*")
	}
	segments := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "*" {
			segments = append(segments, `\d+`)
			continue
		}
		if !isASCIIDigits(part) {
			return nil, false
		}
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 || value > 255 {
			return nil, false
		}
		segments = append(segments, regexp.QuoteMeta(part))
	}
	return regexp.MustCompile("^" + strings.Join(segments, `\.`) + "$"), true
}

func isASCIIDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func ipRulesMatch(rules []ipRule, ip string) bool {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	for _, rule := range rules {
		switch {
		case rule.wildcard != nil:
			if rule.wildcard.MatchString(ip) {
				return true
			}
		case rule.prefix.IsValid():
			if rule.prefix.Contains(addr) {
				return true
			}
		case rule.addr.IsValid():
			if rule.addr == addr {
				return true
			}
		}
	}
	return false
}
