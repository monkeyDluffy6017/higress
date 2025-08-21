ai-llm-router（按语义与能力分路由大模型）

ai-llm-router 是一个基于请求语义进行分类，并按照各候选大模型在不同标签上的能力打分，自动选择“最合适目的 LLM”的路由插件。

本插件与内置的 `ai-proxy` 插件配合使用：ai-llm-router 负责决定使用哪个 Provider，并通过请求头把结果传给 ai-proxy；ai-proxy 负责把请求转发到对应 Provider 的上游域名/路径/鉴权。

工作原理
- 从请求体中提取用户自然语言输入（自动去除成对 ```/```` 代码块内容）。
- 调用配置的“分析大模型”（OpenAI 兼容接口）对输入做分类：
  - 标签集合：build_new_project、add_new_feature、fix_bug、use_tool、other。
- 根据配置的候选 Provider 能力打分，选择该标签下得分最高的 Provider：
  - 如分数相同，按 tieBreakOrder 或声明顺序打破并列。
  - 如分数低于 minScore 或无可选项，统一回退到 fallbackProviderId。
- 写入请求头 X-HI-Provider-Id（或自定义名称），由 ai-proxy 完成最终转发；同时覆盖请求体中的 model 字段为所选 providerId。
- 在响应头返回 x-select-llm: <providerId> 便于观测。
- 失败与超时：在 analyzer 的单次调用超时（timeoutMs）与总时间窗（totalTimeoutMs）内进行有限重试（最多 3 次）；若最终无法得到标签，则不设置头部，直接恢复请求走默认链路。

部署关系与顺序
- 必须保证 ai-llm-router 在 ai-proxy 之前执行（优先级更小的数字/更前置的阶段），否则路由结果无法生效。

配置说明（已采用策略模式）
```yaml
strategy:
  type: semantic                  # 策略类型；目前支持 semantic（语义选择）
  semantic:
    analyzer:
      enabled: true
      # 通过服务源（DNS）访问 analyzer，上游需在 Higress 中注册服务
      serviceName: "analyzer.dns"   # 必填，Higress 服务名（DNS 类型）
      servicePort: 443               # 选填，默认 443
      serviceDomain: "api.example.com"  # 必填，用于 Host/SNI
      path: "/v1/chat/completions"  # 必填，请求路径
      apiToken: "sk-***"
      model: "qwen2.5-coder-32b"
      timeoutMs: 3000
      totalTimeoutMs: 10000
      maxInputBytes: 10240
      promptTemplate: ""
      protocol: "openai"
      # 定义“语义分析分类”的标签集合（仅此字段保留）。
      analysisLabels:
        - build_new_project
        - add_new_feature
        - fix_bug
        - other

    inputExtraction:
      protocol: "openai"
      userJoinSep: "\n\n"
      stripCodeFences: true
      codeFenceRegex: ""
      contentJsonPath: ""

    routing:
      providerIdHeader: "X-HI-Provider-Id"
      candidates:
        - id: "openai"
          enabled: true
          scores:
            build_new_project: 5
            add_new_feature: 4
            fix_bug: 3
            use_tool: 4
            other: 2
        - id: "deepseek"
          enabled: true
          scores:
            build_new_project: 3
            add_new_feature: 5
            fix_bug: 5
            use_tool: 3
            other: 2
      minScore: 1
      fallbackProviderId: "openai"
      tieBreakOrder: ["deepseek", "openai"]

      # 动态指标（可选）：以 dy_ 开头的模型指标，仅用于规则引擎过滤
      dynamicMetrics:
        redisPrefix: "llm_metrics"       # Redis Key 前缀
        serviceName: "redis.svc"         # Higress DNS 服务名
        servicePort: 6379                 # 端口，默认 6379（.static 服务默认 80）
        username: ""                      # 可选
        password: ""                      # 可选
        timeout: 1000                     # 毫秒，可选，默认 1000
        database: 0                       # 可选，默认 0

    # 规则引擎（声明式模型资格筛选，先于偏好策略执行）
    ruleEngine:
      enabled: true
      # 仅支持 inlineRules（内联规则）
      inlineRules:
        - rule_name: "Route Code Review to Expert Models"
          priority: 100
          conditions:
            all:
              - { fact: "request.task_type", operator: eq, value: "code_review" }
              - { fact: "model.tags", operator: contains, value: "code-review-expert" }
              - { fact: "model.availability_status", operator: eq, value: "UP" }
          action:
            type: FILTER_MODELS
            result: ALLOW
            sortBy:
              - { fact: "model.quality_benchmark_scores.human_eval", order: desc }
              - { fact: "model.provider", order: asc }
      # 规则文件加载已移除，不再支持 rulesFile
      # 使用说明：
      # - 规则里凡是引用到 `model.dy_xxx` 的条件，都会在评估前从 Redis 拉取该指标。
      # - Redis Key 结构：`<redisPrefix>:dy_xxx:<model_id>`，值支持数字或字符串。
      # - 拉取到的值会注入到模型对象中（字段名仍为 dy_xxx），仅用于 FILTER_MODELS 过滤，不参与 analyzer 标签。
```

 重要约束
- routing.candidates[].id 必须与 ai-proxy 的 providers[].id 对齐。
- 仅对 Content-Type: application/json 且符合协议的请求生效。
- 为保护敏感信息，发送给分析模型前会移除成对代码块并做长度截断。
- analyzer 仅支持基于服务源（DNS）的访问方式。HTTPS 场景下需使用域名作为 serviceDomain 以满足证书与 SNI 要求；如必须直连 IP，请在 HTTP 场景或为该 IP 配置对应的域名。

已简化：移除了 `labels` 字段，保留 `analysisLabels` 作为唯一的分类标签集合。未提供 `promptTemplate` 时总是使用内置 `defaultPromptTemplate`。

serviceDomain 为 IP 的情况
- 支持将 `serviceDomain` 配置为 IP。此时默认会使用该值作为请求的 Host（:authority），并在 TLS 中作为 SNI 发送。
- HTTP 场景：可以直接使用 IP（如 `servicePort: 80`，`serviceDomain: "10.0.0.12"`）。
- HTTPS 场景：除非上游证书的 SubjectAltName 显式包含该 IP，且上游对 SNI 不强制域名，否则会出现证书校验或基于 SNI 的路由失败。常见做法：
  - 给该 IP 绑定一个域名，并在 `serviceDomain` 中填写该域名；或
  - 为该服务签发包含该 IP 的证书（较少见）。

配置示例（IP 直连 HTTP）：
```yaml
strategy:
  type: semantic
  semantic:
    analyzer:
      serviceName: "analyzer.dns"
      servicePort: 80
      serviceDomain: "10.0.0.12"
      path: "/v1/chat/completions"
      apiToken: "sk-***"
      model: "qwen2.5-coder-32b"
```

响应头
- x-select-llm: <providerId>

与 ai-proxy 的对接
- ai-proxy 已实现请求级 Provider 覆写：读取 X-HI-Provider-Id 并优先使用对应 Provider。
- ai-llm-router 只负责选择并写头；具体 Host/Path/鉴权改写由 ai-proxy 完成。

示例：与 ai-proxy 一起使用
1. 在 ai-proxy 的控制面中确保已配置候选 Provider（包含 providers[].id）。
2. 在 ai-llm-router 配置中：配置 strategy.type=semantic 与 strategy.semantic 下的 analyzer、inputExtraction、routing.candidates。
3. 确保执行顺序：ai-llm-router 在前，ai-proxy 在后。

限制与建议
- 首版未内置缓存；如调用分析模型开销较大，可后续引入缓存（键为输入文本 hash）。
- 如请求体不是标准 OpenAI 结构，可用 contentJsonPath 自定义提取路径，或扩展 protocol。

规则引擎：请求体可选字段示例（用于构建 request_context 与 available_models）
```json
{
  "task_type": "code_review",
  "mode": "code_mode",
  "language": "java",
  "routing": {
    "available_models": [
      {
        "model_id": "claude-3-opus-20240229",
        "provider": "anthropic",
        "tags": ["code-review-expert", "multi-lingual"],
        "availability_status": "UP",
        "quality_benchmark_scores": { "human_eval": 92.0 }
      },
      {
        "model_id": "m2",
        "provider": "internal",
        "tags": ["code-review-expert"],
        "availability_status": "DOWN"
      }
    ]
  }
}
```

备注
- 规则引擎负责“资格筛选”（哪些模型能做这件事），策略负责“偏好选择”（在合格集合中选谁）。
- 引擎输出的合格集合会写入请求头 `x-qualified-models` 便于观测或在后续策略中引用。


