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
	"github.com/tidwall/resp"

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

const defaultPromptTemplate = `You are a classification specialist. Classify ONLY based on the CURRENT turn. Use history strictly for disambiguation of short messages (e.g., "retry", "continue", "same as above").

Labels and definitions:
1) simple_request: Non-technical, conversational queries. Includes greetings (e.g., "hello"), identity questions (e.g., "who are you?"), or general chat not involving programming/code/dev tasks.
2) planning_request: Requests for analysis/planning/explanation about code or a task without directly writing or editing code. Examples: review code and give feedback, create a technical plan/outline, discuss architecture, explain an algorithm.
3) code_modification: Requests that require generating, editing, or modifying code. Examples: implement a function, fix a bug, add a new feature, refactor, translate comments, or convert code between languages.

Special rules:
- Classify ONLY using the Current section below. Do NOT summarize or rewrite anything.
- History may be referenced only to interpret very short Current inputs.
- If Current contains file paths, line ranges (e.g., foo.go:12-20), diffs, or code blocks indicating an edit intent, prefer code_modification unless clearly chit-chat.
- If the Current contains imperative phrases indicating immediate implementation (e.g., "实施", "实现", "开始实现", "开始编码", "按计划实施", "落地", "修改", "修复", "apply the plan", "go ahead and implement", "implement now"), classify as code_modification.

History (older → newer):
{HISTORY}

Current:
{CURRENT}

Instructions:
- Output exactly one of: simple_request, planning_request, code_modification
- Output the label only. No extra words.`

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
	analysisLabels []string
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
	// dynamic metrics for rule engine filtering
	dynamicMetricsPrefix string
	redisServiceName     string
	redisServicePort     int
	redisUsername        string
	redisPassword        string
	redisTimeoutMs       int
	redisDatabase        int
	redisClient          wrapper.RedisClient
}

type InputExtractionConfig struct {
	protocol        string
	userJoinSep     string
	stripCodeFences bool
	codeFenceRegex  string
	contentJsonPath string
	maxUserMessages int
	maxHistoryBytes int
}

type Candidate struct {
	id        string
	modelName string
	enabled   bool
	scores    map[string]int
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
	// 仅支持内联规则 inlineRules
	inlineRules []ruleengine.Rule
	// 前缀配置：用于 request_context 的动态填充来源
	// 约定：
	// - 以 bodyPrefix 开头的 request.xxx，会从请求体 JSON 路径提取（去掉前缀后作为 gjson 路径）
	// - 以 headerPrefix 开头的 request.xxx，会从请求头读取（去掉前缀后作为 header 名）
	// - 其他 request.xxx 保持原样（可由上游通过 extraRequestContext 注入）
	bodyPrefix   string
	headerPrefix string
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
	// 默认不生效：当未配置或<=0时，不做长度限制（保持为0）
	s.analyzer.promptTemplate = j.Get("analyzer.promptTemplate").String()
	s.analyzer.protocol = j.Get("analyzer.protocol").String()
	if s.analyzer.protocol == "" {
		s.analyzer.protocol = "openai"
	}
	// analysisLabels (used strictly for semantic classification)
	s.analyzer.analysisLabels = make([]string, 0)
	if arr := j.Get("analyzer.analysisLabels"); arr.Exists() && arr.IsArray() {
		for _, v := range arr.Array() {
			sv := strings.TrimSpace(v.String())
			if sv != "" {
				s.analyzer.analysisLabels = append(s.analyzer.analysisLabels, sv)
			}
		}
	}
	// Defaults: if none provided, use built-in labels
	if len(s.analyzer.analysisLabels) == 0 {
		s.analyzer.analysisLabels = []string{"simple_request", "planning_request", "code_modification"}
	}
	// compile regex based on analysisLabels
	{
		var b strings.Builder
		b.WriteString("\\b(")
		for i, sv := range s.analyzer.analysisLabels {
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
			// 当域名或服务名包含点号时，视为已提供完整域，优先使用 FQDN 集群；否则使用 DNS 集群
			useFQDN := strings.Contains(s.analyzer.serviceDomain, ".") || strings.Contains(s.analyzer.serviceName, ".")
			if useFQDN {
				fqdn := s.analyzer.serviceName
				if !strings.Contains(fqdn, ".") {
					fqdn = s.analyzer.serviceName + "." + s.analyzer.serviceDomain
				}
				s.analyzer.domain = fqdn
				s.analyzer.port = port
				s.analyzer.client = wrapper.NewClusterClient(wrapper.FQDNCluster{
					FQDN: fqdn,
					Port: port,
				})
			} else {
				s.analyzer.domain = s.analyzer.serviceDomain
				s.analyzer.port = port
				s.analyzer.client = wrapper.NewClusterClient(wrapper.DnsCluster{
					ServiceName: s.analyzer.serviceName,
					Port:        port,
					Domain:      s.analyzer.serviceDomain,
				})
			}
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
	// history window config (optional)
	if j.Get("inputExtraction.maxUserMessages").Exists() {
		s.inputExtraction.maxUserMessages = int(j.Get("inputExtraction.maxUserMessages").Int())
	}
	if s.inputExtraction.maxUserMessages <= 0 {
		s.inputExtraction.maxUserMessages = 100
	}
	if j.Get("inputExtraction.maxHistoryBytes").Exists() {
		s.inputExtraction.maxHistoryBytes = int(j.Get("inputExtraction.maxHistoryBytes").Int())
	}

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
		c.modelName = strings.TrimSpace(v.Get("modelName").String())
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
	// rule engine request context prefixes
	s.ruleEngine.bodyPrefix = strings.TrimSpace(j.Get("ruleEngine.bodyPrefix").String())
	if s.ruleEngine.bodyPrefix == "" {
		s.ruleEngine.bodyPrefix = "body."
	}
	s.ruleEngine.headerPrefix = strings.TrimSpace(j.Get("ruleEngine.headerPrefix").String())
	if s.ruleEngine.headerPrefix == "" {
		s.ruleEngine.headerPrefix = "header."
	}

	// dynamic metrics (optional) now under analyzer
	dm := j.Get("analyzer.dynamicMetrics")
	s.analyzer.dynamicMetricsPrefix = strings.TrimSpace(dm.Get("redisPrefix").String())
	if s.analyzer.dynamicMetricsPrefix != "" {
		s.analyzer.redisServiceName = dm.Get("serviceName").String()
		s.analyzer.redisServicePort = int(dm.Get("servicePort").Int())
		if s.analyzer.redisServicePort == 0 {
			if strings.HasSuffix(s.analyzer.redisServiceName, ".static") {
				s.analyzer.redisServicePort = 80
			} else {
				s.analyzer.redisServicePort = 6379
			}
		}
		s.analyzer.redisUsername = dm.Get("username").String()
		s.analyzer.redisPassword = dm.Get("password").String()
		s.analyzer.redisTimeoutMs = int(dm.Get("timeout").Int())
		if s.analyzer.redisTimeoutMs == 0 {
			s.analyzer.redisTimeoutMs = 1000
		}
		s.analyzer.redisDatabase = int(dm.Get("database").Int())
		if s.analyzer.redisServiceName != "" {
			s.analyzer.redisClient = wrapper.NewRedisClusterClient(wrapper.FQDNCluster{
				FQDN: s.analyzer.redisServiceName,
				Port: int64(s.analyzer.redisServicePort),
			})
			_ = s.analyzer.redisClient.Init(s.analyzer.redisUsername, s.analyzer.redisPassword, int64(s.analyzer.redisTimeoutMs), wrapper.WithDataBase(s.analyzer.redisDatabase))
			log.Infof("[ai-llm-router] dynamic metrics redis ready: service=%s port=%d prefix=%s", s.analyzer.redisServiceName, s.analyzer.redisServicePort, s.analyzer.dynamicMetricsPrefix)
		} else {
			log.Warnf("[ai-llm-router] dynamicMetrics.redisPrefix is set but serviceName is empty; dynamic metrics disabled")
		}
	}
	log.Infof("[ai-llm-router] ruleEngine.enabled=%v inlineRules=%d",
		s.ruleEngine.enabled, len(s.ruleEngine.inlineRules))
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
		// If dynamic metrics are needed, handle asynchronously
		if needsDynamicMetrics(s.analyzer) {
			startRuleEngineWithDynamic(ctx, s.analyzer, s.ruleEngine, body, func(qms []map[string]any) {
				// 若无合格模型，按需求：优先使用 fallback，若 fallback 为空则直接放行
				if len(qms) == 0 {
					if s.routing.fallbackProviderId != "" {
						_ = proxywasm.ReplaceHttpRequestHeader(s.routing.providerIdHeader, s.routing.fallbackProviderId)
						// prefer candidate.modelName when set
						effModel := modelNameForProvider(s.routing.fallbackProviderId, s.routing)
						if effModel == "" {
							effModel = s.routing.fallbackProviderId
						}
						if b, ok := overrideRequestModelInBody(body, effModel); ok {
							_ = proxywasm.ReplaceHttpRequestBody(b)
							logs.Debugf("[ai-llm-router] override request model to fallback provider=%s model=%s", s.routing.fallbackProviderId, effModel)
						}
						ctx.SetContext("selectedProviderId", s.routing.fallbackProviderId)
						logs.Infof("[ai-llm-router] no qualified models, use fallback provider=%s", s.routing.fallbackProviderId)
					} else {
						logs.Infof("[ai-llm-router] no qualified models and no fallback, pass through")
					}
					_ = proxywasm.ResumeHttpRequest()
					return
				}
				// 存在合格模型：基于合格集合过滤候选并继续分析流程
				routing := buildRoutingFromQualified(qms, s.routing)
				continueAnalyzerFlowWithRouting(ctx, s, body, &routing)
			})
			return types.ActionPause
		}
		if qms, err := runRuleEngine(ctx, s.ruleEngine, body, nil, nil); err != nil {
			logs.Warnf("[ai-llm-router] rule engine evaluate error: %v", err)
		} else {
			// 允许 qms 为空：表示没有合格模型
			prefiltered = qms
			if len(prefiltered) == 0 {
				if s.routing.fallbackProviderId != "" {
					_ = proxywasm.ReplaceHttpRequestHeader(s.routing.providerIdHeader, s.routing.fallbackProviderId)
					// prefer candidate.modelName if set
					effModel := modelNameForProvider(s.routing.fallbackProviderId, s.routing)
					if effModel == "" {
						effModel = s.routing.fallbackProviderId
					}
					if b, ok := overrideRequestModelInBody(body, effModel); ok {
						_ = proxywasm.ReplaceHttpRequestBody(b)
						logs.Debugf("[ai-llm-router] override request model to fallback provider=%s model=%s", s.routing.fallbackProviderId, effModel)
					}
					ctx.SetContext("selectedProviderId", s.routing.fallbackProviderId)
					logs.Infof("[ai-llm-router] no qualified models, use fallback provider=%s", s.routing.fallbackProviderId)
				} else {
					logs.Infof("[ai-llm-router] no qualified models and no fallback, pass through")
				}
				return types.ActionContinue
			}
		}
	}

	if !s.analyzer.enabled || s.analyzer.client == nil || s.analyzer.path == "" || s.analyzer.apiToken == "" || s.analyzer.model == "" {
		logs.Debugf("[ai-llm-router] analyzer disabled or not configured, skip routing")
		return types.ActionContinue
	}
	historyText, currentText := extractHistoryAndCurrent(body, s.inputExtraction, s.analyzer.maxInputBytes)
	if strings.TrimSpace(currentText) == "" {
		logs.Debugf("[ai-llm-router] empty user text after extraction, skip routing")
		return types.ActionContinue
	}
	logs.Debugf("[ai-llm-router] extracted (history bytes=%d, current bytes=%d) protocol=%s", len([]byte(historyText)), len([]byte(currentText)), s.inputExtraction.protocol)
	logs.Debugf("[ai-llm-router] extracted current content: %s", currentText)
	// 保存当前轮用户输入，供响应头返回
	ctx.SetContext("currentUserInput", currentText)

	prompt := buildPrompt(s.analyzer.promptTemplate, historyText, currentText)
	reqBody, _ := json.Marshal(map[string]interface{}{
		"model":    s.analyzer.model,
		"messages": []map[string]string{{"role": "user", "content": prompt}},
	})
	headers := [][2]string{{"Content-Type", "application/json"}, {"Authorization", "Bearer " + s.analyzer.apiToken}}
	logs.Debugf("[ai-llm-router] analyzer request: host=%s port=%d path=%s body=%s", s.analyzer.domain, s.analyzer.port, s.analyzer.path, string(reqBody))

	maxRetries := 3
	deadline := time.Now().Add(time.Duration(s.analyzer.totalTimeoutMs) * time.Millisecond)
	attempt := 0

	// 规则引擎开启时：若有合格集合则只从合格集合中选择；若为空则已在前面处理（fallback 或放行）
	routingUsed := s.routing
	if s.ruleEngine.enabled && len(prefiltered) > 0 {
		routingUsed = buildRoutingFromQualified(prefiltered, s.routing)
	}

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
				label := classifyLabel(statusCode, responseBody, s.analyzer.labelRegex, s.analyzer.analysisLabels)
				if label != "" {
					logs.Infof("[ai-llm-router] analyzer classified label=%s", label)
					// 第二阶段策略：基于候选模型（若有规则引擎预筛选）选择 provider
					selected := selectProvider(label, routingUsed)
					if selected != "" {
						_ = proxywasm.ReplaceHttpRequestHeader(s.routing.providerIdHeader, selected)
						// prefer configured modelName for this provider
						effModel := modelNameForProvider(selected, routingUsed)
						if effModel == "" {
							effModel = selected
						}
						if b, ok := overrideRequestModelInBody(body, effModel); ok {
							_ = proxywasm.ReplaceHttpRequestBody(b)
							logs.Debugf("[ai-llm-router] override request model to provider=%s model=%s", selected, effModel)
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
	if v := ctx.GetContext("currentUserInput"); v != nil {
		if cur, _ := v.(string); strings.TrimSpace(cur) != "" {
			_ = proxywasm.ReplaceHttpResponseHeader("x-user-input", cur)
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
	var text string
	switch ie.protocol {
	case "openai":
		// 更智能的提取：
		// 1) 优先提取 <task>/<feedback> 标签内容
		// 2) 跳过疑似工具生成的内容（工具XML、[read_file] Result、<files>、<environment_details> 等）
		// 3) 若本轮没有有效用户输入，回溯上一轮用户消息，直到找到
		msgs := gjson.GetBytes(body, "messages").Array()

		isLikelyToolGenerated := func(s string) bool {
			t := strings.TrimSpace(s)
			if t == "" {
				return false
			}
			// XML 风格工具标签
			if re := regexp.MustCompile("(?s)<(read_file|search_files|list_files|apply_diff|insert_content|write_to_file|search_and_replace|ask_followup_question|attempt_completion|switch_mode|new_task|update_todo_list)[^>]*>"); re != nil && re.MatchString(t) {
				return true
			}
			// 结果/环境块
			if re := regexp.MustCompile("(?s)<files>.*?</files>"); re != nil && re.MatchString(t) {
				return true
			}
			if re := regexp.MustCompile("(?s)<environment_details>.*?</environment_details>"); re != nil && re.MatchString(t) {
				return true
			}
			// 方括号工具输出行，如 [read_file ...] Result:
			if re := regexp.MustCompile(`(?m)^\[(read_file|search_files|list_files|apply_diff|insert_content|write_to_file|search_and_replace|ask_followup_question|attempt_completion|switch_mode|new_task|update_todo_list)[^\]]*\]`); re != nil && re.MatchString(t) {
				return true
			}
			// 常见的环境/系统提示关键字
			if strings.Contains(t, "VSCode Visible Files") || strings.Contains(t, "VSCode Open Tabs") || strings.Contains(t, "Current Workspace Directory") {
				return true
			}
			return false
		}

		extractExplicit := func(s string) (string, bool) {
			if re := regexp.MustCompile(`(?s)<task>\\n?\\s*(.*?)\\s*\\n?</task>`); re != nil {
				if m := re.FindStringSubmatch(s); len(m) >= 2 {
					return strings.TrimSpace(m[1]), true
				}
			}
			if re := regexp.MustCompile(`(?s)<user_message>\\n?\\s*(.*?)\\s*\\n?</user_message>`); re != nil {
				if m := re.FindStringSubmatch(s); len(m) >= 2 {
					return strings.TrimSpace(m[1]), true
				}
			}
			// 支持 <answer> 作为用户显式输入来源
			if re := regexp.MustCompile(`(?s)<answer>\\n?\\s*(.*?)\\s*\\n?</answer>`); re != nil {
				if m := re.FindStringSubmatch(s); len(m) >= 2 {
					return strings.TrimSpace(m[1]), true
				}
			}
			if re := regexp.MustCompile(`(?s)<feedback>\\n?\\s*(.*?)\\s*\\n?</feedback>`); re != nil {
				if m := re.FindStringSubmatch(s); len(m) >= 2 {
					fs := strings.TrimSpace(m[1])
					if re2 := regexp.MustCompile(`^@/?\\s*`); re2 != nil {
						fs = re2.ReplaceAllString(fs, "")
					}
					return strings.TrimSpace(fs), true
				}
			}
			return "", false
		}

		// 先回溯用户消息
		for i := len(msgs) - 1; i >= 0; i-- {
			m := msgs[i]
			if m.Get("role").String() != "user" {
				continue
			}
			c := m.Get("content")
			if !c.Exists() {
				continue
			}
			var raw string
			if c.IsArray() {
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
				raw = strings.TrimSpace(b.String())
			} else {
				raw = c.String()
			}

			if v, ok := extractExplicit(raw); ok && strings.TrimSpace(v) != "" {
				text = v
				break
			}
			cleaned := cleanUserText(raw)
			if isLikelyToolGenerated(raw) || isLikelyToolGenerated(cleaned) {
				continue
			}
			if strings.TrimSpace(cleaned) != "" {
				text = cleaned
				break
			}
		}
		// 若仍为空，从最近到最远遍历所有消息（忽略 role），同样优先显式标签并过滤工具内容
		if strings.TrimSpace(text) == "" {
			for i := len(msgs) - 1; i >= 0; i-- {
				m := msgs[i]
				c := m.Get("content")
				if !c.Exists() {
					continue
				}
				var raw string
				if c.IsArray() {
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
					raw = strings.TrimSpace(b.String())
				} else {
					raw = c.String()
				}

				if v, ok := extractExplicit(raw); ok && strings.TrimSpace(v) != "" {
					text = v
					break
				}
				cleaned := cleanUserText(raw)
				if isLikelyToolGenerated(raw) || isLikelyToolGenerated(cleaned) {
					continue
				}
				if strings.TrimSpace(cleaned) != "" {
					text = cleaned
					break
				}
			}
		}
		// 最后回退：直接从原始 body 中解析 <task>/<user_message>...
		if strings.TrimSpace(text) == "" {
			if re := regexp.MustCompile(`(?s)<task>\\n?\\s*(.*?)\\s*\\n?</task>`); re != nil {
				if m := re.FindSubmatch(body); len(m) >= 2 {
					text = strings.TrimSpace(string(m[1]))
				}
			}
			if strings.TrimSpace(text) == "" {
				if re := regexp.MustCompile(`(?s)<user_message>\\n?\\s*(.*?)\\s*\\n?</user_message>`); re != nil {
					if m := re.FindSubmatch(body); len(m) >= 2 {
						text = strings.TrimSpace(string(m[1]))
					}
				}
			}
		}
	default:
		if path := ie.contentJsonPath; path != "" {
			s := gjson.GetBytes(body, path).String()
			if s != "" {
				text = s
			}
		}
	}
	// 清理并裁剪（清理已在循环中做过，此处保守再清一次）
	text = cleanUserText(text)
	if ie.stripCodeFences {
		pattern := ie.codeFenceRegex
		if pattern == "" {
			pattern = "(?s)```{3,4}[\\s\\S]*?```{3,4}"
		}
		if re, err := regexp.Compile(pattern); err == nil {
			text = re.ReplaceAllString(text, "")
		}
	}
	if maxBytes > 0 && len([]byte(text)) > maxBytes {
		bs := []byte(text)
		if maxBytes < len(bs) {
			bs = bs[:maxBytes]
		}
		text = string(bs)
	}
	return text
}

// extractAllUserInputs 汇总所有用户输入：
// - 遍历所有 messages 中 role=user 的消息（从旧到新）
// - 优先抽取 <task>/<feedback>，否则清洗后加入
// - 过滤工具/环境噪声
// - 使用 ie.userJoinSep 连接，并应用 stripCodeFences 与 maxBytes
func extractAllUserInputs(body []byte, ie InputExtractionConfig, maxBytes int) string {
	if ie.protocol != "openai" {
		return extractUserInput(body, ie, maxBytes)
	}
	msgs := gjson.GetBytes(body, "messages").Array()
	if len(msgs) == 0 {
		return extractUserInput(body, ie, maxBytes)
	}

	isLikelyToolGenerated := func(s string) bool {
		t := strings.TrimSpace(s)
		if t == "" {
			return false
		}
		if re := regexp.MustCompile("(?s)<(read_file|search_files|list_files|apply_diff|insert_content|write_to_file|search_and_replace|ask_followup_question|attempt_completion|switch_mode|new_task|update_todo_list)[^>]*>"); re != nil && re.MatchString(t) {
			return true
		}
		if re := regexp.MustCompile("(?s)<files>.*?</files>"); re != nil && re.MatchString(t) {
			return true
		}
		if re := regexp.MustCompile("(?s)<environment_details>.*?</environment_details>"); re != nil && re.MatchString(t) {
			return true
		}
		if re := regexp.MustCompile(`(?m)^\[(read_file|search_files|list_files|apply_diff|insert_content|write_to_file|search_and_replace|ask_followup_question|attempt_completion|switch_mode|new_task|update_todo_list)[^\]]*\]`); re != nil && re.MatchString(t) {
			return true
		}
		if strings.Contains(t, "VSCode Visible Files") || strings.Contains(t, "VSCode Open Tabs") || strings.Contains(t, "Current Workspace Directory") {
			return true
		}
		return false
	}

	extractExplicit := func(s string) (string, bool) {
		if re := regexp.MustCompile(`(?s)<task>\n?\s*(.*?)\s*\n?</task>`); re != nil {
			if m := re.FindStringSubmatch(s); len(m) >= 2 {
				return strings.TrimSpace(m[1]), true
			}
		}
		if re := regexp.MustCompile(`(?s)<user_message>\n?\s*(.*?)\s*\n?</user_message>`); re != nil {
			if m := re.FindStringSubmatch(s); len(m) >= 2 {
				return strings.TrimSpace(m[1]), true
			}
		}
		// 支持 <answer> 作为用户显式输入来源
		if re := regexp.MustCompile(`(?s)<answer>\n?\s*(.*?)\s*\n?</answer>`); re != nil {
			if m := re.FindStringSubmatch(s); len(m) >= 2 {
				return strings.TrimSpace(m[1]), true
			}
		}
		if re := regexp.MustCompile(`(?s)<feedback>\n?\s*(.*?)\s*\n?</feedback>`); re != nil {
			if m := re.FindStringSubmatch(s); len(m) >= 2 {
				fs := strings.TrimSpace(m[1])
				if re2 := regexp.MustCompile(`^@/?\s*`); re2 != nil {
					fs = re2.ReplaceAllString(fs, "")
				}
				return strings.TrimSpace(fs), true
			}
		}
		return "", false
	}

	// 限制最多聚合的用户消息条数
	startIdx := 0
	if ie.maxUserMessages > 0 && len(msgs) > ie.maxUserMessages {
		// 从最后 ie.maxUserMessages 条用户消息开始，但需要跳过非 user 的消息
		// 简化做法：先收集所有 user 索引
		userIdx := make([]int, 0, len(msgs))
		for i := 0; i < len(msgs); i++ {
			if msgs[i].Get("role").String() == "user" {
				userIdx = append(userIdx, i)
			}
		}
		if len(userIdx) > ie.maxUserMessages {
			userIdx = userIdx[len(userIdx)-ie.maxUserMessages:]
		}
		if len(userIdx) > 0 {
			startIdx = userIdx[0]
		}
	}

	parts := make([]string, 0)
	for i := startIdx; i < len(msgs); i++ {
		m := msgs[i]
		if m.Get("role").String() != "user" {
			continue
		}
		c := m.Get("content")
		if !c.Exists() {
			continue
		}
		var raw string
		if c.IsArray() {
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
			raw = strings.TrimSpace(b.String())
		} else {
			raw = c.String()
		}

		if v, ok := extractExplicit(raw); ok && strings.TrimSpace(v) != "" {
			parts = append(parts, v)
			continue
		}
		cleaned := cleanUserText(raw)
		if isLikelyToolGenerated(raw) || isLikelyToolGenerated(cleaned) {
			continue
		}
		if strings.TrimSpace(cleaned) != "" {
			parts = append(parts, cleaned)
		}
	}

	var text string
	if len(parts) > 0 {
		sep := ie.userJoinSep
		if sep == "" {
			sep = "\n\n"
		}
		text = strings.Join(parts, sep)
	} else {
		// 回退：直接从原始 body 中解析 <task>/<user_message>...
		if re := regexp.MustCompile(`(?s)<task>\n?\s*(.*?)\s*\n?</task>`); re != nil {
			if m := re.FindSubmatch(body); len(m) >= 2 {
				text = strings.TrimSpace(string(m[1]))
			}
		}
		if strings.TrimSpace(text) == "" {
			if re := regexp.MustCompile(`(?s)<user_message>\n?\s*(.*?)\s*\n?</user_message>`); re != nil {
				if m := re.FindSubmatch(body); len(m) >= 2 {
					text = strings.TrimSpace(string(m[1]))
				}
			}
		}
	}

	text = cleanUserText(text)
	if ie.stripCodeFences {
		pattern := ie.codeFenceRegex
		if pattern == "" {
			pattern = "(?s)```{3,4}[\\s\\S]*?```{3,4}"
		}
		if re, err := regexp.Compile(pattern); err == nil {
			text = re.ReplaceAllString(text, "")
		}
	}
	// 综合 maxHistoryBytes 与 maxBytes 限制，取更严格的限制
	effectiveLimit := maxBytes
	if ie.maxHistoryBytes > 0 && (effectiveLimit == 0 || ie.maxHistoryBytes < effectiveLimit) {
		effectiveLimit = ie.maxHistoryBytes
	}
	if effectiveLimit > 0 && len([]byte(text)) > effectiveLimit {
		bs := []byte(text)
		if effectiveLimit < len(bs) {
			bs = bs[:effectiveLimit]
		}
		text = string(bs)
	}
	return text
}

// cleanUserText removes environment/tooling metadata and prefers explicit <task> content when present.
func cleanUserText(text string) string {
	if text == "" {
		return ""
	}
	// 轻量清洗：不改变语义，仅做格式规范与无形字符去除
	// 1) 规范换行：\r\n / \r -> \n
	s := strings.ReplaceAll(text, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	// 2) 去除零宽字符：U+200B/U+200C/U+200D/U+FEFF
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\u200B', '\u200C', '\u200D', '\uFEFF':
			return -1
		}
		return r
	}, s)
	// 3) 去首尾空白
	return strings.TrimSpace(s)
}

func buildPrompt(tpl string, history string, current string) string {
	// When promptTemplate is not provided, always use the defaultPromptTemplate.
	if strings.TrimSpace(tpl) == "" {
		tpl = defaultPromptTemplate
	}
	// New template style only (no backward compatibility)
	tpl = strings.ReplaceAll(tpl, "{HISTORY}", history)
	tpl = strings.ReplaceAll(tpl, "{CURRENT}", current)
	return tpl
}

// extractHistoryAndCurrent 将消息划分为 History 与 Current 两部分：
// - Current：最后一条 role=user 的消息（优先取 <task>/<feedback>/<answer>，否则清洗）
// - History：其余的 role=user 消息（从旧到新连接），同样优先显式标签并清洗
// - 应用 stripCodeFences 与字节截断；History 受 maxHistoryBytes 限制，Current 受 maxBytes 限制
func extractHistoryAndCurrent(body []byte, ie InputExtractionConfig, maxBytes int) (string, string) {
	if ie.protocol != "openai" {
		// 对非 openai 协议，沿用旧逻辑，全部算作 current
		cur := extractUserInput(body, ie, maxBytes)
		return "", cur
	}
	msgs := gjson.GetBytes(body, "messages").Array()
	if len(msgs) == 0 {
		cur := extractUserInput(body, ie, maxBytes)
		return "", cur
	}

	// NOTE: tool-generated content detection is unused when only explicit tags are accepted

	extractExplicit := func(s string) (string, bool) {
		if re := regexp.MustCompile(`(?s)<task>\n?\s*(.*?)\s*\n?</task>`); re != nil {
			if m := re.FindStringSubmatch(s); len(m) >= 2 {
				return strings.TrimSpace(m[1]), true
			}
		}
		if re := regexp.MustCompile(`(?s)<user_message>\n?\s*(.*?)\s*\n?</user_message>`); re != nil {
			if m := re.FindStringSubmatch(s); len(m) >= 2 {
				return strings.TrimSpace(m[1]), true
			}
		}
		if re := regexp.MustCompile(`(?s)<answer>\n?\s*(.*?)\s*\n?</answer>`); re != nil {
			if m := re.FindStringSubmatch(s); len(m) >= 2 {
				return strings.TrimSpace(m[1]), true
			}
		}
		if re := regexp.MustCompile(`(?s)<feedback>\n?\s*(.*?)\s*\n?</feedback>`); re != nil {
			if m := re.FindStringSubmatch(s); len(m) >= 2 {
				fs := strings.TrimSpace(m[1])
				if re2 := regexp.MustCompile(`^@/?\s*`); re2 != nil {
					fs = re2.ReplaceAllString(fs, "")
				}
				return strings.TrimSpace(fs), true
			}
		}
		return "", false
	}

	// 收集所有 user 消息索引
	userIdx := make([]int, 0, len(msgs))
	for i := 0; i < len(msgs); i++ {
		if msgs[i].Get("role").String() == "user" {
			userIdx = append(userIdx, i)
		}
	}
	if len(userIdx) == 0 {
		return "", ""
	}
	curIdx := userIdx[len(userIdx)-1]

	// 处理 current
	getRaw := func(c gjson.Result) string {
		if !c.Exists() {
			return ""
		}
		if c.IsArray() {
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
			return strings.TrimSpace(b.String())
		}
		return c.String()
	}

	curRaw := getRaw(msgs[curIdx].Get("content"))
	var cur string
	if v, ok := extractExplicit(curRaw); ok && strings.TrimSpace(v) != "" {
		cur = strings.TrimSpace(v)
	}
	// 若最后一条 user 消息无有效内容，回退寻找上一条有效的用户输入作为 Current
	chosenCurIdx := curIdx
	if strings.TrimSpace(cur) == "" {
		for i := len(userIdx) - 2; i >= 0; i-- {
			idx := userIdx[i]
			raw := getRaw(msgs[idx].Get("content"))
			if v, ok := extractExplicit(raw); ok && strings.TrimSpace(v) != "" {
				cur = strings.TrimSpace(v)
				chosenCurIdx = idx
				break
			}
		}
	}
	if ie.stripCodeFences {
		pattern := ie.codeFenceRegex
		if pattern == "" {
			pattern = "(?s)```{3,4}[\\s\\S]*?```{3,4}"
		}
		if re, err := regexp.Compile(pattern); err == nil {
			cur = re.ReplaceAllString(cur, "")
		}
	}
	if maxBytes > 0 && len([]byte(cur)) > maxBytes {
		bs := []byte(cur)
		if maxBytes < len(bs) {
			bs = bs[:maxBytes]
		}
		cur = string(bs)
	}

	// 处理 history（除去 current 的其他 user 消息，仅提取显式标签）
	parts := make([]string, 0)
	if ie.maxUserMessages > 0 && len(userIdx) > ie.maxUserMessages {
		userIdx = userIdx[len(userIdx)-ie.maxUserMessages:]
	}
	for _, idx := range userIdx {
		// History 仅包含早于 Current 的用户消息
		if idx >= chosenCurIdx {
			continue
		}
		c := msgs[idx].Get("content")
		raw := getRaw(c)
		if v, ok := extractExplicit(raw); ok && strings.TrimSpace(v) != "" {
			parts = append(parts, v)
			continue
		}
	}
	var history string
	if len(parts) > 0 {
		sep := ie.userJoinSep
		if sep == "" {
			sep = "\n\n"
		}
		history = strings.Join(parts, sep)
	}
	if ie.stripCodeFences {
		pattern := ie.codeFenceRegex
		if pattern == "" {
			pattern = "(?s)```{3,4}[\\s\\S]*?```{3,4}"
		}
		if re, err := regexp.Compile(pattern); err == nil {
			history = re.ReplaceAllString(history, "")
		}
	}
	// History 字节限制优先生效
	effectiveHistoryLimit := ie.maxHistoryBytes
	if effectiveHistoryLimit > 0 && len([]byte(history)) > effectiveHistoryLimit {
		bs := []byte(history)
		if effectiveHistoryLimit < len(bs) {
			bs = bs[:effectiveHistoryLimit]
		}
		history = string(bs)
	}

	return history, cur
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

// overrideRequestModelInBody 尝试将请求体中的 "model" 字段改为指定值
// 返回修改后的字节切片与是否成功找到并替换
func overrideRequestModelInBody(original []byte, newModel string) ([]byte, bool) {
	if len(original) == 0 || newModel == "" {
		return nil, false
	}
	bs := original
	needle := []byte("\"model\":\"")
	idx := indexOf(bs, needle)
	if idx < 0 {
		return nil, false
	}
	start := idx + len(needle)
	end := start
	for end < len(bs) && bs[end] != '"' {
		end++
	}
	var b []byte
	b = append(b, bs[:start]...)
	b = append(b, []byte(newModel)...)
	b = append(b, bs[end:]...)
	return b, true
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
func runRuleEngine(ctx wrapper.HttpContext, cfg RuleEngineConfig, body []byte, extraRequestContext map[string]any, defaultAvailableModels []map[string]any) ([]map[string]any, error) {
	if !cfg.enabled {
		return nil, nil
	}

	// 1) 构建 request_context
	reqCtx := buildRequestContext(ctx, body, cfg)
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

	// 3) 加载规则（仅内联）
	rules := cfg.inlineRules
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

// needsDynamicMetrics checks if dynamic metrics are configured
func needsDynamicMetrics(a AnalyzerConfig) bool {
	return a.dynamicMetricsPrefix != "" && a.redisClient != nil
}

// startRuleEngineWithDynamic loads dy_ metrics from Redis and evaluates rules asynchronously
func startRuleEngineWithDynamic(ctx wrapper.HttpContext, a AnalyzerConfig, cfg RuleEngineConfig, body []byte, done func([]map[string]any)) {
	// Build reqCtx and models same as runRuleEngine
	reqCtx := buildRequestContext(ctx, body, cfg)
	// available models
	var models []ruleengine.Model
	if arr := gjson.GetBytes(body, "routing.available_models"); arr.Exists() && arr.IsArray() {
		a := arr.Array()
		models = make([]ruleengine.Model, 0, len(a))
		for _, m := range a {
			models = append(models, mapFromGJSON(m))
		}
	}
	rules := cfg.inlineRules
	if len(models) == 0 || len(rules) == 0 {
		done(nil)
		return
	}

	// Scan dynamic metrics in rules: look for facts with prefix model.dy_
	dynSet := map[string]struct{}{}
	for _, r := range rules {
		// all/any/not conditions
		for _, c := range r.Conditions.All {
			if strings.HasPrefix(c.Fact, "model.dy_") {
				dynSet[c.Fact[len("model."):]] = struct{}{}
			}
		}
		for _, c := range r.Conditions.Any {
			if strings.HasPrefix(c.Fact, "model.dy_") {
				dynSet[c.Fact[len("model."):]] = struct{}{}
			}
		}
		if r.Conditions.Not != nil && strings.HasPrefix(r.Conditions.Not.Fact, "model.dy_") {
			dynSet[r.Conditions.Not.Fact[len("model."):]] = struct{}{}
		}
	}
	if len(dynSet) == 0 {
		// No dynamic metrics needed, evaluate directly
		res, err := ruleengine.New().Evaluate(reqCtx, models, rules)
		if err != nil || len(res.QualifiedModels) == 0 {
			done(nil)
			return
		}
		qms := make([]map[string]any, 0, len(res.QualifiedModels))
		for _, m := range res.QualifiedModels {
			qms = append(qms, m)
		}
		done(qms)
		return
	}

	// Collect metrics list
	metrics := make([]string, 0, len(dynSet))
	for k := range dynSet {
		metrics = append(metrics, k) // k like dy_xxx
	}

	// For each model and each metric, fetch from Redis: prefix:metric:modelId
	// Assume a model id field exists: model_id
	pending := 0
	// Protect when no async calls scheduled
	scheduled := false
	for i := range models {
		modelId, _ := models[i]["model_id"].(string)
		if modelId == "" {
			continue
		}
		for _, mname := range metrics {
			key := a.dynamicMetricsPrefix + ":" + mname + ":" + modelId
			pending++
			scheduled = true
			a.redisClient.Get(key, func(respVal resp.Value) {
				// On response
				if err := respVal.Error(); err == nil && !respVal.IsNull() {
					// Try parse float first, else keep string
					valStr := respVal.String()
					// store as float if possible
					if f := respVal.Float(); !(f == 0 && (valStr != "0" && valStr != "0.0")) {
						models[i]["dy_"+mname[len("dy_"):]] = f
					} else {
						models[i]["dy_"+mname[len("dy_"):]] = valStr
					}
				}
				pending--
				if pending == 0 {
					// All loaded, evaluate
					res, err := ruleengine.New().Evaluate(reqCtx, models, rules)
					if err != nil || len(res.QualifiedModels) == 0 {
						done(nil)
						return
					}
					qms := make([]map[string]any, 0, len(res.QualifiedModels))
					for _, m := range res.QualifiedModels {
						qms = append(qms, m)
					}
					done(qms)
				}
			})
		}
	}
	if !scheduled {
		// No valid model ids or nothing scheduled; evaluate directly
		res, err := ruleengine.New().Evaluate(reqCtx, models, rules)
		if err != nil || len(res.QualifiedModels) == 0 {
			done(nil)
			return
		}
		qms := make([]map[string]any, 0, len(res.QualifiedModels))
		for _, m := range res.QualifiedModels {
			qms = append(qms, m)
		}
		done(qms)
	}
}

// buildRequestContext 基于前缀从请求体/请求头提取 request_context
// 约定：
// - 在规则中使用 request.<key>，<key> 可以以 cfg.bodyPrefix / cfg.headerPrefix 开头
// - 去掉前缀后：
//   - bodyPrefix：按 gjson 路径从 body 取值
//   - headerPrefix：按 header 名从请求头取值
//
// - 无前缀：不做特殊处理（保留空值，等待 extra 注入或模型字段条件）
func buildRequestContext(ctx wrapper.HttpContext, body []byte, cfg RuleEngineConfig) ruleengine.RequestContext {
	rc := ruleengine.RequestContext{}
	// 收集规则中出现的 request.* fact
	keys := collectRequestFacts(cfg.inlineRules)
	for _, k := range keys {
		// k 是 request.xxx；我们只取 xxx 部分
		if !strings.HasPrefix(k, "request.") {
			continue
		}
		sub := k[len("request."):]
		switch {
		case strings.HasPrefix(sub, cfg.bodyPrefix):
			path := strings.TrimPrefix(sub, cfg.bodyPrefix)
			if path != "" {
				v := gjson.GetBytes(body, path)
				if v.Exists() {
					// request.body.<path>
					value := valueFromGJSON(v)
					keys := append([]string{"body"}, strings.Split(path, ".")...)
					setNestedRequestContext(rc, keys, value)
				}
			}
		case strings.HasPrefix(sub, cfg.headerPrefix):
			h := strings.TrimPrefix(sub, cfg.headerPrefix)
			if h != "" {
				if val, err := proxywasm.GetHttpRequestHeader(h); err == nil && val != "" {
					// request.header.<Header-Name>
					setNestedRequestContext(rc, []string{"header", h}, val)
				}
			}
		default:
			// 留空，允许额外注入
		}
	}
	return rc
}

func collectRequestFacts(rules []ruleengine.Rule) []string {
	set := map[string]struct{}{}
	add := func(f string) {
		if strings.HasPrefix(f, "request.") {
			set[f] = struct{}{}
		}
	}
	for _, r := range rules {
		for _, c := range r.Conditions.All {
			add(c.Fact)
		}
		for _, c := range r.Conditions.Any {
			add(c.Fact)
		}
		if r.Conditions.Not != nil {
			add(r.Conditions.Not.Fact)
		}
		for _, sk := range r.Action.SortBy {
			if strings.HasPrefix(sk.Fact, "request.") {
				add(sk.Fact)
			}
		}
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	return keys
}

// setNestedRequestContext 根据路径键设置嵌套的 request_context 结构
func setNestedRequestContext(rc ruleengine.RequestContext, keys []string, value any) {
	if len(keys) == 0 {
		return
	}
	cur := map[string]any(rc)
	for i := 0; i < len(keys)-1; i++ {
		k := keys[i]
		next, ok := cur[k]
		if !ok {
			child := map[string]any{}
			cur[k] = child
			cur = child
			continue
		}
		if m, ok := next.(map[string]any); ok {
			cur = m
		} else {
			// 覆盖为 map，保证结构一致
			child := map[string]any{}
			cur[k] = child
			cur = child
		}
	}
	cur[keys[len(keys)-1]] = value
}

// continueAnalyzerFlow continues analyzer flow after async rule engine completes
func continueAnalyzerFlowWithRouting(ctx wrapper.HttpContext, s *SemanticStrategy, body []byte, prouting *RoutingConfig) {
	if !s.analyzer.enabled || s.analyzer.client == nil || s.analyzer.path == "" || s.analyzer.apiToken == "" || s.analyzer.model == "" {
		logs.Debugf("[ai-llm-router] analyzer disabled or not configured, skip routing")
		_ = proxywasm.ResumeHttpRequest()
		return
	}
	historyText, currentText := extractHistoryAndCurrent(body, s.inputExtraction, s.analyzer.maxInputBytes)
	if strings.TrimSpace(currentText) == "" {
		logs.Debugf("[ai-llm-router] empty user text after extraction, skip routing")
		_ = proxywasm.ResumeHttpRequest()
		return
	}
	prompt := buildPrompt(s.analyzer.promptTemplate, historyText, currentText)
	reqBody, _ := json.Marshal(map[string]interface{}{
		"model":    s.analyzer.model,
		"messages": []map[string]string{{"role": "user", "content": prompt}},
	})
	headers := [][2]string{{"Content-Type", "application/json"}, {"Authorization", "Bearer " + s.analyzer.apiToken}}
	// choose routing: filtered if provided, otherwise default
	routingUsed := s.routing
	if prouting != nil {
		routingUsed = *prouting
	}
	deadline := time.Now().Add(time.Duration(s.analyzer.totalTimeoutMs) * time.Millisecond)
	attempt := 0
	var send func()
	send = func() {
		remaining := time.Until(deadline) / time.Millisecond
		if remaining <= 0 {
			_ = proxywasm.ResumeHttpRequest()
			return
		}
		callTimeout := s.analyzer.timeoutMs
		if callTimeout == 0 || int64(callTimeout) > int64(remaining) {
			callTimeout = uint32(remaining)
		}
		err := s.analyzer.client.Post(
			s.analyzer.path,
			headers,
			reqBody,
			func(statusCode int, responseHeaders http.Header, responseBody []byte) {
				label := classifyLabel(statusCode, responseBody, s.analyzer.labelRegex, s.analyzer.analysisLabels)
				if label != "" {
					selected := selectProvider(label, routingUsed)
					if selected != "" {
						_ = proxywasm.ReplaceHttpRequestHeader(s.routing.providerIdHeader, selected)
						// prefer configured modelName
						effModel := modelNameForProvider(selected, routingUsed)
						if effModel == "" {
							effModel = selected
						}
						if b, ok := overrideRequestModelInBody(body, effModel); ok {
							_ = proxywasm.ReplaceHttpRequestBody(b)
						}
					}
					_ = proxywasm.ResumeHttpRequest()
					return
				}
				if attempt < 3 && time.Until(deadline) > 0 {
					attempt++
					send()
					return
				}
				_ = proxywasm.ResumeHttpRequest()
			},
			callTimeout,
		)
		if err != nil {
			_ = proxywasm.ResumeHttpRequest()
			return
		}
	}
	send()
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

// modelNameForProvider returns the configured modelName for a provider id
// from the given routing config. Empty string means no override is configured.
func modelNameForProvider(providerId string, r RoutingConfig) string {
	if providerId == "" {
		return ""
	}
	for _, c := range r.candidates {
		if c.id == providerId && strings.TrimSpace(c.modelName) != "" {
			return c.modelName
		}
	}
	return ""
}

// buildRoutingFromQualified 基于规则引擎产出的合格模型集合过滤路由候选集
// 允许的匹配键：provider_id、provider、id、model_id（字符串）
func buildRoutingFromQualified(qms []map[string]any, base RoutingConfig) RoutingConfig {
	allow := map[string]struct{}{}
	for _, m := range qms {
		// check common keys
		if v, ok := m["provider_id"].(string); ok && v != "" {
			allow[v] = struct{}{}
		}
		if v, ok := m["provider"].(string); ok && v != "" {
			allow[v] = struct{}{}
		}
		if v, ok := m["id"].(string); ok && v != "" {
			allow[v] = struct{}{}
		}
		if v, ok := m["model_id"].(string); ok && v != "" {
			allow[v] = struct{}{}
		}
	}

	filtered := make([]Candidate, 0, len(base.candidates))
	for _, c := range base.candidates {
		if _, ok := allow[c.id]; ok {
			filtered = append(filtered, c)
		}
	}

	// filter tieBreakOrder accordingly
	filteredOrder := make([]string, 0, len(base.tieBreakOrder))
	for _, id := range base.tieBreakOrder {
		if _, ok := allow[id]; ok {
			filteredOrder = append(filteredOrder, id)
		}
	}

	// fallback 只能在合格集合内可用；否则置空
	fallback := base.fallbackProviderId
	if fallback != "" {
		if _, ok := allow[fallback]; !ok {
			fallback = ""
		}
	}

	return RoutingConfig{
		providerIdHeader:   base.providerIdHeader,
		candidates:         filtered,
		fallbackProviderId: fallback,
		minScore:           base.minScore,
		tieBreakOrder:      filteredOrder,
	}
}
