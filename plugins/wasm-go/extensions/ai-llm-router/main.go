package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"

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

// 自定义插件配置
type AnalyzerConfig struct {
	enabled        bool
	baseUrl        string
	apiToken       string
	model          string
	timeoutMs      uint32
	maxInputBytes  int
	promptTemplate string
	protocol       string
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
	config.analyzer.baseUrl = j.Get("analyzer.baseUrl").String()
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
	// build analyzer client if enabled and baseUrl present
	if config.analyzer.enabled && config.analyzer.baseUrl != "" {
		u, err := url.Parse(config.analyzer.baseUrl)
		if err == nil {
			config.analyzer.path = u.Path
			host := u.Hostname()
			port := int64(80)
			if u.Scheme == "https" {
				port = 443
			}
			if u.Port() != "" {
				// best-effort parse
				// ignore error -> keep default
				if p := u.Port(); p != "" {
					// convert
					// simple manual parse to avoid extra import
					var acc int64
					for i := 0; i < len(p); i++ {
						ch := p[i]
						if ch < '0' || ch > '9' {
							acc = 0
							break
						}
						acc = acc*10 + int64(ch-'0')
					}
					if acc > 0 {
						port = acc
					}
				}
			}
			config.analyzer.domain = host
			config.analyzer.port = port
			config.analyzer.client = wrapper.NewClusterClient(wrapper.FQDNCluster{FQDN: host, Host: host, Port: port})
		}
	}

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
		return types.ActionContinue
	}

	// 提取用户输入
	userText := extractUserInput(body, config.inputExtraction, config.analyzer.maxInputBytes)
	if strings.TrimSpace(userText) == "" {
		return types.ActionContinue
	}

	// 组织请求体
	prompt := buildPrompt(config.analyzer.promptTemplate, userText)
	reqBody, _ := json.Marshal(map[string]interface{}{
		"model":    config.analyzer.model,
		"messages": []map[string]string{{"role": "user", "content": prompt}},
	})

	headers := [][2]string{{"Content-Type", "application/json"}, {"Authorization", "Bearer " + config.analyzer.apiToken}}

	// 异步调用，暂停请求，回调中 Resume
	err := config.analyzer.client.Post(
		config.analyzer.path,
		headers,
		reqBody,
		func(statusCode int, responseHeaders http.Header, responseBody []byte) {
			label := classifyLabel(statusCode, responseBody)
			selected := selectProvider(label, config.routing)
			if selected != "" {
				_ = proxywasm.ReplaceHttpRequestHeader(config.routing.providerIdHeader, selected)
				ctx.SetContext("selectedProviderId", selected)
			}
			_ = proxywasm.ResumeHttpRequest()
		},
		config.analyzer.timeoutMs,
	)
	if err != nil {
		_ = proxywasm.ResumeHttpRequest()
		return types.ActionPause
	}
	return types.ActionPause
}

func onHttpResponseHeaders(ctx wrapper.HttpContext, config Config) types.Action {
	if v := ctx.GetContext("selectedProviderId"); v != nil {
		if id, _ := v.(string); id != "" {
			_ = proxywasm.ReplaceHttpResponseHeader("x-select-llm", id)
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

func buildPrompt(tpl string, user string) string {
	if strings.TrimSpace(tpl) == "" {
		tpl = defaultPromptTemplate
	}
	return strings.ReplaceAll(tpl, "{USER_INPUT}", user)
}

var labelRegex = regexp.MustCompile(`\b(build_new_project|add_new_feature|fix_bug|use_tool|other)\b`)

func classifyLabel(statusCode int, resp []byte) string {
	if statusCode != 200 || len(resp) == 0 {
		return "other"
	}
	content := gjson.GetBytes(resp, "choices.0.message.content").String()
	if content == "" {
		return "other"
	}
	m := labelRegex.FindString(content)
	if m == "" {
		return "other"
	}
	return m
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
	for _, c := range r.candidates {
		if !c.enabled || c.id == "" {
			continue
		}
		score := c.scores[label]
		rank := 1 << 29
		if v, ok := order[c.id]; ok {
			rank = v
		}
		if score > bestScore || (score == bestScore && rank < bestRank) {
			bestScore, bestRank, bestId = score, rank, c.id
		}
	}
	if bestId == "" || bestScore < r.minScore {
		return r.fallbackProviderId
	}
	return bestId
}

const defaultPromptTemplate = "You are a highly-specialized classification expert. Your ONLY purpose is to classify a user's development request into one of five labels based on the detailed definitions below.\n\n" +
	"Here are the definitions for each label:\n\n" +
	"1.  build_new_project: creating a brand-new, standalone application/service/module/system from scratch.\n" +
	"2.  add_new_feature: adding a new capability to an existing application/service/module.\n" +
	"3.  fix_bug: fixing errors/defects or unexpected behavior in existing functionality.\n" +
	"4.  other: anything else (refactoring, docs, performance, analysis, etc.).\n\n" +
	"Instructions: respond with ONE label only: build_new_project, add_new_feature, fix_bug, use_tool, or other. No explanations.\n\n" +
	"User Request: {USER_INPUT}"
