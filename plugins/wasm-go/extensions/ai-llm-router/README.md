ai-llm-router（按语义与能力分路由大模型）

ai-llm-router 是一个基于请求语义进行分类，并按照各候选大模型在不同标签上的能力打分，自动选择“最合适目的 LLM”的路由插件。

本插件与内置的 `ai-proxy` 插件配合使用：ai-llm-router 负责决定使用哪个 Provider，并通过请求头把结果传给 ai-proxy；ai-proxy 负责把请求转发到对应 Provider 的上游域名/路径/鉴权。

工作原理
- 从请求体中提取用户自然语言输入（自动去除成对 ```/```` 代码块内容）。
- 调用配置的“分析大模型”（OpenAI 兼容接口）对输入做分类：
  - 标签集合：build_new_project、add_new_feature、fix_bug、use_tool、other。
- 根据配置的候选 Provider 能力打分，选择该标签下得分最高的 Provider：
  - 如分数相同，按 tieBreakOrder 或声明顺序打破并列。
  - 如分数低于 minScore 或无可选项，回退到 fallbackProviderId。
- 写入请求头 X-HI-Provider-Id（或自定义名称），由 ai-proxy 完成最终转发。
- 在响应头返回 x-select-llm: <providerId> 便于观测。

部署关系与顺序
- 必须保证 ai-llm-router 在 ai-proxy 之前执行（优先级更小的数字/更前置的阶段），否则路由结果无法生效。

配置说明
```yaml
analyzer:
  enabled: true
  baseUrl: "https://host/v1/chat/completions"
  apiToken: "sk-***"
  model: "qwen2.5-coder-32b"
  timeoutMs: 3000
  maxInputBytes: 10240
  promptTemplate: ""
  protocol: "openai"

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
```

重要约束
- routing.candidates[].id 必须与 ai-proxy 的 providers[].id 对齐。
- 仅对 Content-Type: application/json 且符合协议的请求生效。
- 为保护敏感信息，发送给分析模型前会移除成对代码块并做长度截断。

响应头
- x-select-llm: <providerId>

与 ai-proxy 的对接
- ai-proxy 已实现请求级 Provider 覆写：读取 X-HI-Provider-Id 并优先使用对应 Provider。
- ai-llm-router 只负责选择并写头；具体 Host/Path/鉴权改写由 ai-proxy 完成。

示例：与 ai-proxy 一起使用
1. 在 ai-proxy 的控制面中确保已配置候选 Provider（包含 providers[].id）。
2. 在 ai-llm-router 配置中：配置 analyzer、inputExtraction、routing.candidates。
3. 确保执行顺序：ai-llm-router 在前，ai-proxy 在后。

限制与建议
- 首版未内置缓存；如调用分析模型开销较大，可后续引入缓存（键为输入文本 hash）。
- 如请求体不是标准 OpenAI 结构，可用 contentJsonPath 自定义提取路径，或扩展 protocol。


