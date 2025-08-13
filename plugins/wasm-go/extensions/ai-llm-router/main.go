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

	ruleengine "github.com/monkeyDluffy6017/ai-llm-rule-engine/pkg/ruleengine"
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

// ===== 策略模式：通用接口与语义策略实现 =====

// Strategy 定义了路由策略的统一接口
type Strategy interface {
	Name() string
	Parse(j gjson.Result, log logs.Log) error
	OnRequestHeaders(ctx wrapper.HttpContext, log logs.Log) types.Action
	OnRequestBody(ctx wrapper.HttpContext, body []byte) types.Action
	OnResponseHeaders(ctx wrapper.HttpContext) types.Action
	OnResponseBody(ctx wrapper.HttpContext, body []byte) types.Action
}

// 自定义插件配置（顶层）
type Config struct {
	strategyType string
	strategy     Strategy
}

// 语义选择策略的内部配置与实现
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

// RuleEngineConfig 控制规则引擎（声明式筛选）
type RuleEngineConfig struct {
	enabled bool
	// inlineRules 与 rulesFile 二选一；若两者都有，优先 inlineRules
	inlineRules []ruleengine.Rule
	rulesFile   string
}

type SemanticStrategy struct {
	analyzer        AnalyzerConfig
	inputExtraction InputExtractionConfig
	routing         RoutingConfig
	ruleEngine      RuleEngineConfig
}

func (s *SemanticStrategy) Name() string { return "semantic" }

func (s *SemanticStrategy) Parse(j gjson.Result, log logs.Log) error {
	// analyzer
	s.analyzer.enabled = j.Get("analyzer.enabled").Bool()
	if !j.Get("analyzer.enabled").Exists() {
		s.analyzer.enabled = true
	}
	s.analyzer.apiToken = j.Get("analyzer.apiToken").String()
	s.analyzer.model = j.Get("analyzer.model").String()
	s.analyzer.timeoutMs = uint32(j.Get("analyzer.timeoutMs").Uint())
	if s.analyzer.timeoutMs == 0 {
		s.analyzer.timeoutMs = 3000
	}
	s.analyzer.maxInputBytes = int(j.Get("analyzer.maxInputBytes").Int())
	if s.analyzer.maxInputBytes <= 0 {
		s.analyzer.maxInputBytes = 10 * 1024
	}
	s.analyzer.promptTemplate = j.Get("analyzer.promptTemplate").String()
	s.analyzer.protocol = j.Get("analyzer.protocol").String()
	if s.analyzer.protocol == "" {
		s.analyzer.protocol = "openai"
	}
	// labels
	s.analyzer.labels = make([]string, 0)
	for _, v := range j.Get("analyzer.labels").Array() {
		sv := strings.TrimSpace(v.String())
		if sv != "" {
			s.analyzer.labels = append(s.analyzer.labels, sv)
		}
	}
	if len(s.analyzer.labels) == 0 {
		s.analyzer.labels = []string{"build_new_project", "add_new_feature", "fix_bug", "use_tool", "other"}
	}
	// compile regex
	{
		var b strings.Builder
		b.WriteString("\\b(")
		for i, sv := range s.analyzer.labels {
			if i > 0 {
				b.WriteString("|")
			}
			b.WriteString(regexp.QuoteMeta(sv))
		}
		b.WriteString(")\\b")
		if re, err := regexp.Compile(b.String()); err == nil {
			s.analyzer.labelRegex = re
		}
	}

	if s.analyzer.enabled {
		// service-source
		s.analyzer.serviceName = j.Get("analyzer.serviceName").String()
		if j.Get("analyzer.servicePort").Exists() {
			s.analyzer.servicePort = int64(j.Get("analyzer.servicePort").Int())
		}
		s.analyzer.serviceDomain = j.Get("analyzer.serviceDomain").String()
		s.analyzer.path = j.Get("analyzer.path").String()
		if s.analyzer.servicePort == 0 {
			s.analyzer.servicePort = 443
		}
		if s.analyzer.serviceName != "" && s.analyzer.serviceDomain != "" {
			port := s.analyzer.servicePort
			if port == 0 {
				port = 443
			}
			s.analyzer.domain = s.analyzer.serviceDomain
			s.analyzer.port = port
			s.analyzer.client = wrapper.NewClusterClient(wrapper.DnsCluster{
				ServiceName: s.analyzer.serviceName,
				Port:        port,
				Domain:      s.analyzer.serviceDomain,
			})
		}
	}

	s.analyzer.totalTimeoutMs = uint32(j.Get("analyzer.totalTimeoutMs").Uint())
	if s.analyzer.totalTimeoutMs == 0 {
		s.analyzer.totalTimeoutMs = 10000
	}
	log.Infof("[ai-llm-router] analyzer.enabled=%v model=%s timeoutMs=%d totalTimeoutMs=%d maxInputBytes=%d protocol=%s domain=%s port=%d path=%s",
		s.analyzer.enabled, s.analyzer.model, s.analyzer.timeoutMs, s.analyzer.totalTimeoutMs, s.analyzer.maxInputBytes, s.analyzer.protocol, s.analyzer.domain, s.analyzer.port, s.analyzer.path)

	// input extraction
	s.inputExtraction.protocol = j.Get("inputExtraction.protocol").String()
	if s.inputExtraction.protocol == "" {
		s.inputExtraction.protocol = "openai"
	}
	s.inputExtraction.userJoinSep = j.Get("inputExtraction.userJoinSep").String()
	if s.inputExtraction.userJoinSep == "" {
		s.inputExtraction.userJoinSep = "\n\n"
	}
	if j.Get("inputExtraction.stripCodeFences").Exists() {
		s.inputExtraction.stripCodeFences = j.Get("inputExtraction.stripCodeFences").Bool()
	} else {
		s.inputExtraction.stripCodeFences = true
	}
	s.inputExtraction.codeFenceRegex = j.Get("inputExtraction.codeFenceRegex").String()
	s.inputExtraction.contentJsonPath = j.Get("inputExtraction.contentJsonPath").String()

	// routing
	s.routing.providerIdHeader = j.Get("routing.providerIdHeader").String()
	if s.routing.providerIdHeader == "" {
		s.routing.providerIdHeader = "X-HI-Provider-Id"
	}
	s.routing.fallbackProviderId = j.Get("routing.fallbackProviderId").String()
	s.routing.minScore = int(j.Get("routing.minScore").Int())
	s.routing.tieBreakOrder = make([]string, 0)
	for _, v := range j.Get("routing.tieBreakOrder").Array() {
		sv := v.String()
		if sv != "" {
			s.routing.tieBreakOrder = append(s.routing.tieBreakOrder, sv)
		}
	}
	s.routing.candidates = make([]Candidate, 0)
	for _, v := range j.Get("routing.candidates").Array() {
		c := Candidate{id: v.Get("id").String(), enabled: true, scores: map[string]int{}}
		if v.Get("enabled").Exists() {
			c.enabled = v.Get("enabled").Bool()
		}
		for k, val := range v.Get("scores").Map() {
			c.scores[k] = int(val.Int())
		}
		if c.id != "" {
			s.routing.candidates = append(s.routing.candidates, c)
		}
	}
	logs.Infof("[ai-llm-router] routing candidates=%d providerIdHeader=%s fallback=%s minScore=%d",
		len(s.routing.candidates), s.routing.providerIdHeader, s.routing.fallbackProviderId, s.routing.minScore)

	// rule engine
	s.ruleEngine.enabled = j.Get("ruleEngine.enabled").Bool()
	// inline rules (JSON/YAML 已转换为 JSON 数组)
	if arr := j.Get("ruleEngine.inlineRules"); arr.Exists() && arr.IsArray() {
		s.ruleEngine.inlineRules = make([]ruleengine.Rule, 0, len(arr.Array()))
		for _, it := range arr.Array() {
			var r ruleengine.Rule
			if err := json.Unmarshal([]byte(it.Raw), &r); err == nil {
				s.ruleEngine.inlineRules = append(s.ruleEngine.inlineRules, r)
			} else {
				log.Warnf("[ai-llm-router] invalid inline rule: %v", err)
			}
		}
	}
	s.ruleEngine.rulesFile = j.Get("ruleEngine.rulesFile").String()
	log.Infof("[ai-llm-router] ruleEngine.enabled=%v inlineRules=%d rulesFile=%s",
		s.ruleEngine.enabled, len(s.ruleEngine.inlineRules), s.ruleEngine.rulesFile)
	return nil
}

func (s *SemanticStrategy) OnRequestHeaders(ctx wrapper.HttpContext, log logs.Log) types.Action {
	// 读取请求体进行语义分析
	ctx.DisableReroute()
	ctx.SetRequestBodyBufferLimit(1024 * 1024)
	return types.HeaderStopIteration
}

func (s *SemanticStrategy) OnRequestBody(ctx wrapper.HttpContext, body []byte) types.Action {
	// 规则引擎第一阶段：若启用，则优先使用规则引擎对 available models 进行资格筛选
	var prefiltered []map[string]any
	if s.ruleEngine.enabled {
		if qms, err := runRuleEngine(s.ruleEngine, body, nil, nil); err != nil {
			logs.Warnf("[ai-llm-router] rule engine evaluate error: %v", err)
		} else if len(qms) > 0 {
			prefiltered = qms
			bs, _ := json.Marshal(map[string]any{"qualified_models": prefiltered})
			_ = proxywasm.ReplaceHttpRequestHeader("x-qualified-models", string(bs))
		}
	}

	if !s.analyzer.enabled || s.analyzer.client == nil || s.analyzer.path == "" || s.analyzer.apiToken == "" || s.analyzer.model == "" {
		logs.Debugf("[ai-llm-router] analyzer disabled or not configured, skip routing")
		return types.ActionContinue
	}
	userText := extractUserInput(body, s.inputExtraction, s.analyzer.maxInputBytes)
	if strings.TrimSpace(userText) == "" {
		logs.Debugf("[ai-llm-router] empty user text after extraction, skip routing")
		return types.ActionContinue
	}
	logs.Debugf("[ai-llm-router] extracted user text bytes=%d protocol=%s", len([]byte(userText)), s.inputExtraction.protocol)
	logs.Debugf("[ai-llm-router] extracted user text content: %s", userText)

	prompt := buildPrompt(s.analyzer.promptTemplate, userText, s.analyzer.labels)
	reqBody, _ := json.Marshal(map[string]interface{}{
		"model":    s.analyzer.model,
		"messages": []map[string]string{{"role": "user", "content": prompt}},
	})
	headers := [][2]string{{"Content-Type", "application/json"}, {"Authorization", "Bearer " + s.analyzer.apiToken}}
	logs.Debugf("[ai-llm-router] analyzer request: host=%s port=%d path=%s body=%s", s.analyzer.domain, s.analyzer.port, s.analyzer.path, string(reqBody))

	maxRetries := 3
	deadline := time.Now().Add(time.Duration(s.analyzer.totalTimeoutMs) * time.Millisecond)
	attempt := 0

	var send func()
	send = func() {
		logs.Debugf("[ai-llm-router] analyzer httpcall: timeoutMs=%d", s.analyzer.timeoutMs)
		remaining := time.Until(deadline) / time.Millisecond
		if remaining <= 0 {
			logs.Warnf("[ai-llm-router] analyzer deadline reached, stop retrying")
			_ = proxywasm.ResumeHttpRequest()
			return
		}
		callTimeout := s.analyzer.timeoutMs
		if callTimeout == 0 || int64(callTimeout) > int64(remaining) {
			callTimeout = uint32(remaining)
		}
		logs.Debugf("[ai-llm-router] analyzer attempt=%d timeoutMs=%d remainingMs=%d path=%s", attempt+1, callTimeout, remaining, s.analyzer.path)
		err := s.analyzer.client.Post(
			s.analyzer.path,
			headers,
			reqBody,
			func(statusCode int, responseHeaders http.Header, responseBody []byte) {
				logs.Debugf("[ai-llm-router] analyzer response: status=%d body=%s", statusCode, string(responseBody))
				label := classifyLabel(statusCode, responseBody, s.analyzer.labelRegex, s.analyzer.labels)
				if label != "" {
					logs.Infof("[ai-llm-router] analyzer classified label=%s", label)
					// 第二阶段策略：基于候选模型（若有规则引擎预筛选）选择 provider
					selected := selectProvider(label, s.routing)
					if selected != "" {
						_ = proxywasm.ReplaceHttpRequestHeader(s.routing.providerIdHeader, selected)
						if len(body) > 0 {
							bs := body
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
			logs.Warnf("[ai-llm-router] analyzer http error: %v, path=%s host=%s port=%d", err, s.analyzer.path, s.analyzer.domain, s.analyzer.port)
			_ = proxywasm.ResumeHttpRequest()
			return
		}
	}

	send()
	return types.ActionPause
}

func (s *SemanticStrategy) OnResponseHeaders(ctx wrapper.HttpContext) types.Action {
	if v := ctx.GetContext("selectedProviderId"); v != nil {
		if id, _ := v.(string); id != "" {
			_ = proxywasm.ReplaceHttpResponseHeader("x-select-llm", id)
			logs.Debugf("[ai-llm-router] response header x-select-llm=%s", id)
		}
	}
	return types.ActionContinue
}

func (s *SemanticStrategy) OnResponseBody(ctx wrapper.HttpContext, body []byte) types.Action {
	return types.ActionContinue
}

// 在控制台插件配置中填写的yaml配置会自动转换为json
// 新配置结构：
// strategy:
//
//	type: semantic
//	semantic:
//	  analyzer: {...}
//	  inputExtraction: {...}
//	  routing: {...}
func parseConfig(j gjson.Result, config *Config, log logs.Log) error {
	st := strings.TrimSpace(j.Get("strategy.type").String())
	if st == "" {
		st = "semantic"
	}
	config.strategyType = st

	switch st {
	case "semantic":
		s := &SemanticStrategy{}
		if err := s.Parse(j.Get("strategy.semantic"), log); err != nil {
			return err
		}
		config.strategy = s
		log.Infof("[ai-llm-router] strategy=%s ready", s.Name())
	default:
		log.Warnf("[ai-llm-router] unknown strategy type: %s", st)
		config.strategy = nil
	}
	return nil
}

func onHttpRequestHeaders(ctx wrapper.HttpContext, config Config, log logs.Log) types.Action {
	if config.strategy == nil {
		return types.ActionContinue
	}
	return config.strategy.OnRequestHeaders(ctx, log)
}

func onHttpRequestBody(ctx wrapper.HttpContext, config Config, body []byte) types.Action {
	if config.strategy == nil {
		return types.ActionContinue
	}
	return config.strategy.OnRequestBody(ctx, body)
}

func onHttpResponseHeaders(ctx wrapper.HttpContext, config Config) types.Action {
	if config.strategy == nil {
		return types.ActionContinue
	}
	return config.strategy.OnResponseHeaders(ctx)
}

func onHttpResponseBody(ctx wrapper.HttpContext, config Config, body []byte) types.Action {
	if config.strategy == nil {
		return types.ActionContinue
	}
	return config.strategy.OnResponseBody(ctx, body)
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

// mapFromGJSON 将 gjson.Result 转 map[string]any（只做浅层转换，嵌套 map/array 递归）
func mapFromGJSON(r gjson.Result) map[string]any {
	if !r.Exists() {
		return map[string]any{}
	}
	switch {
	case r.IsArray():
		return map[string]any{"_": arrayFromGJSON(r)}
	case r.IsObject():
		m := make(map[string]any)
		r.ForEach(func(k, v gjson.Result) bool {
			m[k.String()] = valueFromGJSON(v)
			return true
		})
		return m
	default:
		return map[string]any{"_": valueFromGJSON(r)}
	}
}

func arrayFromGJSON(r gjson.Result) []any {
	arr := make([]any, 0, len(r.Array()))
	for _, v := range r.Array() {
		arr = append(arr, valueFromGJSON(v))
	}
	return arr
}

func valueFromGJSON(v gjson.Result) any {
	switch {
	case v.IsObject():
		m := make(map[string]any)
		v.ForEach(func(k, vv gjson.Result) bool {
			m[k.String()] = valueFromGJSON(vv)
			return true
		})
		return m
	case v.IsArray():
		return arrayFromGJSON(v)
	default:
		if v.Type == gjson.Number {
			return v.Num
		}
		if v.Type == gjson.True || v.Type == gjson.False {
			return v.Bool()
		}
		return v.String()
	}
}

// runRuleEngine 将规则引擎执行过程封装，便于不同策略复用
// - cfg: 规则引擎配置（开关、内联规则、文件路径）
// - body: 原始请求体（用于尝试提取 request_context 与 available_models）
// - extraRequestContext: 额外补充/覆盖到 request_context 的键值
// - defaultAvailableModels: 当请求体未提供 routing.available_models 时，使用该缺省集合
// 返回：合格模型的对象数组（已按规则 sortBy 排序），或 nil
func runRuleEngine(cfg RuleEngineConfig, body []byte, extraRequestContext map[string]any, defaultAvailableModels []map[string]any) ([]map[string]any, error) {
	if !cfg.enabled {
		return nil, nil
	}

	// 1) 构建 request_context
	reqCtx := ruleengine.RequestContext{
		"task_type": gjson.GetBytes(body, "task_type").String(),
		"mode":      gjson.GetBytes(body, "mode").String(),
		"language":  gjson.GetBytes(body, "language").String(),
	}
	for k, v := range extraRequestContext {
		reqCtx[k] = v
	}

	// 2) 构建 available_models
	var models []ruleengine.Model
	if arr := gjson.GetBytes(body, "routing.available_models"); arr.Exists() && arr.IsArray() {
		a := arr.Array()
		models = make([]ruleengine.Model, 0, len(a))
		for _, m := range a {
			models = append(models, mapFromGJSON(m))
		}
	} else if len(defaultAvailableModels) > 0 {
		models = make([]ruleengine.Model, 0, len(defaultAvailableModels))
		for _, m := range defaultAvailableModels {
			models = append(models, ruleengine.Model(m))
		}
	}

	// 3) 加载规则（优先内联，其次文件）
	rules := cfg.inlineRules
	if len(rules) == 0 && cfg.rulesFile != "" {
		if rs, err := ruleengine.LoadRulesFromFile(cfg.rulesFile); err == nil {
			rules = rs
		} else {
			return nil, err
		}
	}
	if len(models) == 0 || len(rules) == 0 {
		return nil, nil
	}

	// 4) 评估
	res, err := ruleengine.New().Evaluate(reqCtx, models, rules)
	if err != nil {
		return nil, err
	}
	if len(res.QualifiedModels) == 0 {
		return nil, nil
	}
	qms := make([]map[string]any, 0, len(res.QualifiedModels))
	for _, m := range res.QualifiedModels {
		qms = append(qms, m)
	}
	return qms, nil
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
	// 若上游通过请求头注入了合格模型集合，可在此扩展结合合格集合进行再过滤
	// 本次保持与既有策略兼容，仅据 label/分数及 tieBreakOrder 选择
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
