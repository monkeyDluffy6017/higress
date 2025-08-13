ai-llm-router (Semantic-based LLM routing with capability scoring)

ai-llm-router classifies the request via an analyzer LLM and selects the best target LLM according to per-label capability scores of candidate providers.

It works together with the built-in `ai-proxy` plugin: ai-llm-router decides which provider to use and passes the provider id via a request header; ai-proxy then rewrites host/path/authorization and forwards the request to the selected upstream.

How it works
- Extract user natural language input from the request body (strip code blocks enclosed by ``` or ````).
- Call an OpenAI-compatible analyzer LLM to classify the request into one of the labels:
  - build_new_project, add_new_feature, fix_bug, use_tool, other.
- Pick the provider with the highest score for that label:
  - Tie-breaking by `tieBreakOrder` or declaration order.
  - Fallback to `fallbackProviderId` if the best score is below `minScore` or no candidate is available.
- Set request header `X-HI-Provider-Id` (or custom name) for ai-proxy to route.
- Add response header `x-select-llm: <providerId>` for observability.

Deployment order
- Ensure ai-llm-router runs before ai-proxy, otherwise the selection cannot take effect.

Configuration
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

Constraints
- `routing.candidates[].id` must match `providers[].id` from ai-proxy configuration.
- Only effective for requests with `Content-Type: application/json` and supported protocols.
- To protect sensitive data, code blocks are stripped and the analyzer input is truncated to `maxInputBytes`.

Response header
- `x-select-llm: <providerId>`

Integration with ai-proxy
- ai-proxy supports per-request provider override via `X-HI-Provider-Id`.
- ai-llm-router only selects and sets the header; ai-proxy handles host/path/auth rewriting.

Usage with ai-proxy
1. Ensure candidates are configured in ai-proxy (including `providers[].id`).
2. Configure `analyzer`, `inputExtraction`, and `routing.candidates` in ai-llm-router.
3. Ensure execution order: ai-llm-router first, ai-proxy next.

Notes
- No cache by default. If the analyzer cost is high, consider adding a cache (keyed by hashed input) later.
- If your request is not in OpenAI format, use `contentJsonPath` or extend `protocol` to customize extraction.


