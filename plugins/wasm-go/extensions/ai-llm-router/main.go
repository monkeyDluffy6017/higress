package main

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	logs "github.com/higress-group/wasm-go/pkg/log"
	"github.com/higress-group/wasm-go/pkg/wrapper"

	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm"
	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm/types"
	"github.com/tidwall/gjson"
)

func main() {}

func init() {
	wrapper.SetCtx(
		// 插件名称
		"ai-llm-router",
		// 为解析插件配置，设置自定义函数
		wrapper.ParseConfigBy(parseConfig),
		// 为处理请求头，设置自定义函数
		wrapper.ProcessRequestHeadersBy(onHttpRequestHeaders),
		wrapper.ProcessRequestBody(onHttpRequestBody),
		wrapper.ProcessResponseHeaders(onHttpResponseHeaders),
		// wrapper.ProcessStreamingResponseBody(onHttpStreamingResponseBody),
		// wrapper.ProcessResponseBody(onHttpResponseBody),
		// wrapper.ProcessStreamDone(onHttpStreamDone),
	)

}

const defaultPromptTemplate = "You are a highly-specialized classification expert. Your ONLY purpose is to classify a user's development request into one of five labels based on the detailed definitions below.\n\n" +
	"Here are the definitions for each label:\n\n" +
	"1.  build_new_project: creating a brand-new, standalone application/service/module/system from scratch.\n" +
	"2.  add_new_feature: adding a new capability to an existing application/service/module.\n" +
	"3.  fix_bug: fixing errors/defects or unexpected behavior in existing functionality.\n" +
	"4.  other: anything else (refactoring, docs, performance, analysis, etc.).\n\n" +
	"Instructions: respond with ONE label only: build_new_project, add_new_feature, fix_bug, use_tool, or other. No explanations.\n\n" +
	"User Request: {USER_INPUT}"

// 自定义插件配置
type AnalyzerConfig struct {
	enabled        bool
	apiToken       string
	model          string
	timeoutMs      uint32
	totalTimeoutMs uint32
	maxInputBytes  int
	promptTemplate string
	protocol       string
	labels         []string
	labelRegex     *regexp.Regexp
	// service-source based fields (optional). If serviceName is set, prefer DNS service source.
	serviceName   string
	servicePort   int64
	serviceDomain string
	// parsed for client
	domain string
	port   int64
	path   string
	client wrapper.HttpClient
}

type InputExtractionConfig struct {
	protocol        string
	userJoinSep     string
	stripCodeFences bool
	codeFenceRegex  string
	contentJsonPath string
}

type Candidate struct {
	id      string
	enabled bool
	scores  map[string]int
}

type RoutingConfig struct {
	providerIdHeader   string
	candidates         []Candidate
	fallbackProviderId string
	minScore           int
	tieBreakOrder      []string
}

type Config struct {
	analyzer        AnalyzerConfig
	inputExtraction InputExtractionConfig
	routing         RoutingConfig
}

// 在控制台插件配置中填写的yaml配置会自动转换为json，此处直接从json这个参数里解析配置即可
func parseConfig(j gjson.Result, config *Config, log logs.Log) error {
	// analyzer
	config.analyzer.enabled = j.Get("analyzer.enabled").Bool()
	if !j.Get("analyzer.enabled").Exists() {
		config.analyzer.enabled = true
	}
	config.analyzer.apiToken = j.Get("analyzer.apiToken").String()
	config.analyzer.model = j.Get("analyzer.model").String()
	config.analyzer.timeoutMs = uint32(j.Get("analyzer.timeoutMs").Uint())
	if config.analyzer.timeoutMs == 0 {
		config.analyzer.timeoutMs = 3000
	}
	config.analyzer.maxInputBytes = int(j.Get("analyzer.maxInputBytes").Int())
	if config.analyzer.maxInputBytes <= 0 {
		config.analyzer.maxInputBytes = 10 * 1024
	}
	config.analyzer.promptTemplate = j.Get("analyzer.promptTemplate").String()
	config.analyzer.protocol = j.Get("analyzer.protocol").String()
	if config.analyzer.protocol == "" {
		config.analyzer.protocol = "openai"
	}
	// configurable labels
	config.analyzer.labels = make([]string, 0)
	for _, v := range j.Get("analyzer.labels").Array() {
		s := strings.TrimSpace(v.String())
		if s != "" {
			config.analyzer.labels = append(config.analyzer.labels, s)
		}
	}
	if len(config.analyzer.labels) == 0 {
		config.analyzer.labels = []string{"build_new_project", "add_new_feature", "fix_bug", "use_tool", "other"}
	}
	// compile label regex: \b(l1|l2|...)\b
	{
		var b strings.Builder
		b.WriteString("\\b(")
		for i, s := range config.analyzer.labels {
			if i > 0 {
				b.WriteString("|")
			}
			// labels are literal words
			b.WriteString(regexp.QuoteMeta(s))
		}
		b.WriteString(")\\b")
		if re, err := regexp.Compile(b.String()); err == nil {
			config.analyzer.labelRegex = re
		}
	}

	if config.analyzer.enabled {
		// read service-source fields
		config.analyzer.serviceName = j.Get("analyzer.serviceName").String()
		if j.Get("analyzer.servicePort").Exists() {
			config.analyzer.servicePort = int64(j.Get("analyzer.servicePort").Int())
		}
		config.analyzer.serviceDomain = j.Get("analyzer.serviceDomain").String()

		// derive path (and defaults) from baseUrl if provided
		// no baseUrl anymore; require explicit path/domain/port in config
		config.analyzer.path = j.Get("analyzer.path").String()
		if config.analyzer.servicePort == 0 {
			// default https 443 if not specified
			config.analyzer.servicePort = 443
		}

		// require serviceName and serviceDomain to build client
		if config.analyzer.serviceName != "" && config.analyzer.serviceDomain != "" {
			port := config.analyzer.servicePort
			if port == 0 {
				// default to 443 if not specified and cannot be derived
				port = 443
			}
			config.analyzer.domain = config.analyzer.serviceDomain
			config.analyzer.port = port
			// 如果是IP，serviceName 指向你的静态服务条目（后台解析到 IP），serviceDomain 填 IP
			config.analyzer.client = wrapper.NewClusterClient(wrapper.DnsCluster{
				ServiceName: config.analyzer.serviceName,
				Port:        port,
				Domain:      config.analyzer.serviceDomain,
			})
		}
	}

	// summary logs for debugging
	// totalTimeoutMs 默认 10000 ms
	config.analyzer.totalTimeoutMs = uint32(j.Get("analyzer.totalTimeoutMs").Uint())
	if config.analyzer.totalTimeoutMs == 0 {
		config.analyzer.totalTimeoutMs = 10000
	}

	log.Infof("[ai-llm-router] analyzer.enabled=%v model=%s timeoutMs=%d totalTimeoutMs=%d maxInputBytes=%d protocol=%s domain=%s port=%d path=%s",
		config.analyzer.enabled, config.analyzer.model, config.analyzer.timeoutMs, config.analyzer.totalTimeoutMs, config.analyzer.maxInputBytes, config.analyzer.protocol, config.analyzer.domain, config.analyzer.port, config.analyzer.path)

	// input extraction
	config.inputExtraction.protocol = j.Get("inputExtraction.protocol").String()
	if config.inputExtraction.protocol == "" {
		config.inputExtraction.protocol = "openai"
	}
	config.inputExtraction.userJoinSep = j.Get("inputExtraction.userJoinSep").String()
	if config.inputExtraction.userJoinSep == "" {
		config.inputExtraction.userJoinSep = "\n\n"
	}
	if j.Get("inputExtraction.stripCodeFences").Exists() {
		config.inputExtraction.stripCodeFences = j.Get("inputExtraction.stripCodeFences").Bool()
	} else {
		config.inputExtraction.stripCodeFences = true
	}
	config.inputExtraction.codeFenceRegex = j.Get("inputExtraction.codeFenceRegex").String()
	config.inputExtraction.contentJsonPath = j.Get("inputExtraction.contentJsonPath").String()

	// routing
	config.routing.providerIdHeader = j.Get("routing.providerIdHeader").String()
	if config.routing.providerIdHeader == "" {
		config.routing.providerIdHeader = "X-HI-Provider-Id"
	}
	config.routing.fallbackProviderId = j.Get("routing.fallbackProviderId").String()
	config.routing.minScore = int(j.Get("routing.minScore").Int())
	config.routing.tieBreakOrder = make([]string, 0)
	for _, v := range j.Get("routing.tieBreakOrder").Array() {
		s := v.String()
		if s != "" {
			config.routing.tieBreakOrder = append(config.routing.tieBreakOrder, s)
		}
	}
	config.routing.candidates = make([]Candidate, 0)
	for _, v := range j.Get("routing.candidates").Array() {
		c := Candidate{id: v.Get("id").String(), enabled: true, scores: map[string]int{}}
		if v.Get("enabled").Exists() {
			c.enabled = v.Get("enabled").Bool()
		}
		for k, val := range v.Get("scores").Map() {
			c.scores[k] = int(val.Int())
		}
		if c.id != "" {
			config.routing.candidates = append(config.routing.candidates, c)
		}
	}
	log.Infof("[ai-llm-router] routing candidates=%d providerIdHeader=%s fallback=%s minScore=%d",
		len(config.routing.candidates), config.routing.providerIdHeader, config.routing.fallbackProviderId, config.routing.minScore)
	return nil
}

func onHttpRequestHeaders(ctx wrapper.HttpContext, config Config, log logs.Log) types.Action {
	// 我们需要读取请求体进行语义分析
	ctx.DisableReroute()
	ctx.SetRequestBodyBufferLimit(1024 * 1024)
	return types.HeaderStopIteration
}

func onHttpRequestBody(ctx wrapper.HttpContext, config Config, body []byte) types.Action {
	// 若未启用 analyzer 或缺少必要配置，直接继续
	if !config.analyzer.enabled || config.analyzer.client == nil || config.analyzer.path == "" || config.analyzer.apiToken == "" || config.analyzer.model == "" {
		logs.Debugf("[ai-llm-router] analyzer disabled or not configured, skip routing")
		return types.ActionContinue
	}

	// 提取用户输入
	userText := extractUserInput(body, config.inputExtraction, config.analyzer.maxInputBytes)
	if strings.TrimSpace(userText) == "" {
		logs.Debugf("[ai-llm-router] empty user text after extraction, skip routing")
		return types.ActionContinue
	}
	logs.Debugf("[ai-llm-router] extracted user text bytes=%d protocol=%s", len([]byte(userText)), config.inputExtraction.protocol)
	logs.Debugf("[ai-llm-router] extracted user text content: %s", userText)

	// 组织请求体
	prompt := buildPrompt(config.analyzer.promptTemplate, userText, config.analyzer.labels)
	reqBody, _ := json.Marshal(map[string]interface{}{
		"model":    config.analyzer.model,
		"messages": []map[string]string{{"role": "user", "content": prompt}},
	})

	headers := [][2]string{{"Content-Type", "application/json"}, {"Authorization", "Bearer " + config.analyzer.apiToken}}
	// 调用前打印关键信息
	logs.Debugf("[ai-llm-router] analyzer request: host=%s port=%d path=%s body=%s", config.analyzer.domain, config.analyzer.port, config.analyzer.path, string(reqBody))

	// 异步调用 + 重试，最多重试3次，总时间不超过 totalTimeoutMs
	maxRetries := 3
	deadline := time.Now().Add(time.Duration(config.analyzer.totalTimeoutMs) * time.Millisecond)
	attempt := 0

	var send func()
	send = func() {
		logs.Debugf("[ai-llm-router] analyzer httpcall: timeoutMs=%d", config.analyzer.timeoutMs)
		remaining := time.Until(deadline) / time.Millisecond
		if remaining <= 0 {
			logs.Warnf("[ai-llm-router] analyzer deadline reached, stop retrying")
			_ = proxywasm.ResumeHttpRequest()
			return
		}
		callTimeout := config.analyzer.timeoutMs
		if callTimeout == 0 || int64(callTimeout) > int64(remaining) {
			callTimeout = uint32(remaining)
		}
		logs.Debugf("[ai-llm-router] analyzer attempt=%d timeoutMs=%d remainingMs=%d path=%s", attempt+1, callTimeout, remaining, config.analyzer.path)
		// Always use path when calling cluster client so that service-source based routing works
		err := config.analyzer.client.Post(
			config.analyzer.path,
			headers,
			reqBody,
			func(statusCode int, responseHeaders http.Header, responseBody []byte) {
				logs.Debugf("[ai-llm-router] analyzer response: status=%d body=%s", statusCode, string(responseBody))
				label := classifyLabel(statusCode, responseBody, config.analyzer.labelRegex, config.analyzer.labels)
				if label != "" {
					logs.Infof("[ai-llm-router] analyzer classified label=%s", label)
					selected := selectProvider(label, config.routing)
					if selected != "" {
						_ = proxywasm.ReplaceHttpRequestHeader(config.routing.providerIdHeader, selected)
						// 将请求体中的 model 改为选中的 providerId
						if len(body) > 0 {
							// 简单替换：若存在 "model":"..."，用 providerId 覆盖；否则不处理
							bs := body
							// 寻找 "model":"
							// 朴素扫描，避免引入额外依赖
							needle := []byte("\"model\":\"")
							idx := indexOf(bs, needle)
							if idx >= 0 {
								start := idx + len(needle)
								end := start
								for end < len(bs) && bs[end] != '"' {
									end++
								}
								var b []byte
								b = append(b, bs[:start]...)
								b = append(b, []byte(selected)...)
								b = append(b, bs[end:]...)
								_ = proxywasm.ReplaceHttpRequestBody(b)
								logs.Debugf("[ai-llm-router] override request model to provider id: %s", selected)
							}
						}
						ctx.SetContext("selectedProviderId", selected)
						logs.Infof("[ai-llm-router] selected provider=%s", selected)
					}
					_ = proxywasm.ResumeHttpRequest()
					return
				}
				// 非成功或无法解析到标签 -> 重试
				if attempt < maxRetries && time.Until(deadline) > 0 {
					logs.Warnf("[ai-llm-router] analyzer classify failed (status=%d), retrying... attempt=%d", statusCode, attempt+2)
					attempt++
					send()
					return
				}
				logs.Warnf("[ai-llm-router] analyzer classify failed and no more retries, status=%d", statusCode)
				_ = proxywasm.ResumeHttpRequest()
			},
			callTimeout,
		)
		if err != nil {
			// Host 层错误（如集群不存在、参数非法）不会进入回调，这里尽量打印上下文，并不做重试
			logs.Warnf("[ai-llm-router] analyzer http error: %v, path=%s host=%s port=%d", err, config.analyzer.path, config.analyzer.domain, config.analyzer.port)
			_ = proxywasm.ResumeHttpRequest()
			return
		}
	}

	send()
	return types.ActionPause
}

func onHttpResponseHeaders(ctx wrapper.HttpContext, config Config) types.Action {
	if v := ctx.GetContext("selectedProviderId"); v != nil {
		if id, _ := v.(string); id != "" {
			_ = proxywasm.ReplaceHttpResponseHeader("x-select-llm", id)
			logs.Debugf("[ai-llm-router] response header x-select-llm=%s", id)
		}
	}
	return types.ActionContinue
}

func onHttpResponseBody(ctx wrapper.HttpContext, config Config, body []byte) types.Action {
	return types.ActionContinue
}

// ===== Helpers =====

func extractUserInput(body []byte, ie InputExtractionConfig, maxBytes int) string {
	var textParts []string
	switch ie.protocol {
	case "openai":
		msgs := gjson.GetBytes(body, "messages").Array()
		for _, m := range msgs {
			if m.Get("role").String() == "user" {
				c := m.Get("content")
				if c.Exists() {
					if c.IsArray() {
						// simple concat of parts if array (not fully handling structured content)
						var b strings.Builder
						for _, p := range c.Array() {
							s := p.Get("text").String()
							if s == "" {
								s = p.String()
							}
							if s != "" {
								b.WriteString(s)
								b.WriteString("\n")
							}
						}
						textParts = append(textParts, strings.TrimSpace(b.String()))
					} else {
						textParts = append(textParts, c.String())
					}
				}
			}
		}
	default:
		if path := ie.contentJsonPath; path != "" {
			s := gjson.GetBytes(body, path).String()
			if s != "" {
				textParts = append(textParts, s)
			}
		}
	}
	text := strings.Join(textParts, ie.userJoinSep)
	if ie.stripCodeFences {
		pattern := ie.codeFenceRegex
		if pattern == "" {
			pattern = "(?s)```{3,4}[\\s\\S]*?```{3,4}"
		}
		if re, err := regexp.Compile(pattern); err == nil {
			text = re.ReplaceAllString(text, "")
		}
	}
	// truncate to maxBytes
	if maxBytes > 0 && len([]byte(text)) > maxBytes {
		bs := []byte(text)
		if maxBytes < len(bs) {
			bs = bs[:maxBytes]
		}
		text = string(bs)
	}
	return text
}

func buildPrompt(tpl string, user string, labels []string) string {
	if strings.TrimSpace(tpl) == "" {
		// Build default template with dynamic labels if provided
		if len(labels) == 0 {
			tpl = defaultPromptTemplate
		} else {
			var b strings.Builder
			b.WriteString("You are a highly-specialized classification expert. Your ONLY purpose is to classify a user's development request into one of labels based on the definitions below.\n\n")
			b.WriteString("Here are the definitions for each label:\n\n")
			// No per-label definition text here; users can still override via promptTemplate
			// Just list labels and instruct to reply one only
			b.WriteString("Labels: ")
			for i, s := range labels {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(s)
			}
			b.WriteString(".\n\nInstructions: respond with ONE label only. No explanations.\n\nUser Request: {USER_INPUT}")
			tpl = b.String()
		}
	}
	return strings.ReplaceAll(tpl, "{USER_INPUT}", user)
}

// indexOf returns the first index of needle in haystack, or -1 if not found.
func indexOf(haystack []byte, needle []byte) int {
	if len(needle) == 0 || len(haystack) < len(needle) {
		return -1
	}
	first := needle[0]
	for i := 0; i <= len(haystack)-len(needle); i++ {
		if haystack[i] != first {
			continue
		}
		match := true
		for j := 1; j < len(needle); j++ {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func classifyLabel(statusCode int, resp []byte, re *regexp.Regexp, fallbackLabels []string) string {
	if statusCode != 200 || len(resp) == 0 {
		return ""
	}
	content := gjson.GetBytes(resp, "choices.0.message.content").String()
	if content == "" {
		return ""
	}
	// prefer configured regex
	if re != nil {
		m := re.FindString(content)
		return m
	}
	// fallback: simple scan for any configured label substring
	for _, s := range fallbackLabels {
		if s != "" && strings.Contains(content, s) {
			return s
		}
	}
	return ""
}

func selectProvider(label string, r RoutingConfig) string {
	// build tie map
	order := map[string]int{}
	for i, id := range r.tieBreakOrder {
		order[id] = i
	}
	bestId := ""
	bestScore := -1 << 30
	bestRank := 1 << 30
	foundAny := false
	for _, c := range r.candidates {
		if !c.enabled || c.id == "" {
			continue
		}
		score, ok := c.scores[label]
		if !ok {
			// 该 provider 未为该标签配置打分，跳过
			continue
		}
		foundAny = true
		rank := 1 << 29
		if v, ok := order[c.id]; ok {
			rank = v
		}
		if score > bestScore || (score == bestScore && rank < bestRank) {
			bestScore, bestRank, bestId = score, rank, c.id
		}
	}
	if !foundAny || bestId == "" || bestScore < r.minScore {
		return r.fallbackProviderId
	}
	return bestId
}
