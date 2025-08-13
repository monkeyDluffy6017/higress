ai-llm-router (Semantic-based LLM routing with capability scoring)

ai-llm-router classifies the request via an analyzer LLM and selects the best target LLM according to per-label capability scores of candidate providers.

It works together with the built-in `ai-proxy` plugin: ai-llm-router decides which provider to use and passes the provider id via a request header; ai-proxy then rewrites host/path/authorization and forwards the request to the selected upstream.

How it works
- Extract user natural language input from the request body (strip code blocks enclosed by ``` or ````).
- Call an OpenAI-compatible analyzer LLM to classify the request into one of the labels:
  - build_new_project, add_new_feature, fix_bug, use_tool, other.
- Pick the provider with the highest score for that label:
  - Tie-breaking by `tieBreakOrder` or declaration order.
  - Always fallback to `fallbackProviderId` when the best score is below `minScore` or no candidate is available.
- Set request header `X-HI-Provider-Id` (or custom name) for ai-proxy to route; meanwhile, override the `model` field in request body to the selected provider id.
- Add response header `x-select-llm: <providerId>` for observability.
- Failures and timeouts: within `timeoutMs` (per call) and `totalTimeoutMs` (overall deadline), perform limited retries (up to 3 attempts). If no label is obtained finally, resume the request without setting the header (default path).

Deployment order
- Ensure ai-llm-router runs before ai-proxy, otherwise the selection cannot take effect.

Configuration
```yaml
analyzer:
  enabled: true
  # Access analyzer via service-source (DNS). The upstream must be registered as a Higress service.
  serviceName: "analyzer.dns"       # required, Higress DNS service name
  servicePort: 443                   # optional, default 443
  serviceDomain: "api.example.com"  # required, used as Host/SNI
  path: "/v1/chat/completions"      # required, request path
  apiToken: "sk-***"
  model: "qwen2.5-coder-32b"
  timeoutMs: 3000
  totalTimeoutMs: 10000
  maxInputBytes: 10240
  promptTemplate: ""
  protocol: "openai"
  # Optional: customize labels (defaults to the 5 built-in ones)
  labels:
    - build_new_project
    - add_new_feature
    - fix_bug
    - use_tool
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
```

Constraints
- `routing.candidates[].id` must match `providers[].id` from ai-proxy configuration.
- Only effective for requests with `Content-Type: application/json` and supported protocols.
- To protect sensitive data, code blocks are stripped and the analyzer input is truncated to `maxInputBytes`.
- Analyzer currently supports DNS service-source only. For HTTPS, use a domain as `serviceDomain` to satisfy certificate/SNI; if you must connect to an IP directly, use HTTP or bind a domain to that IP.
 - When `labels` are customized and `promptTemplate` is not provided, the plugin auto-generates a minimal default prompt listing the labels. If you need label definitions/descriptions, provide your own `promptTemplate` explicitly.

When `serviceDomain` is an IP
- You can set `serviceDomain` to an IP address. It will be used as the request Host (`:authority`) and SNI in TLS by default.
- HTTP: works out of the box (e.g., `servicePort: 80`, `serviceDomain: "10.0.0.12"`).
- HTTPS: unless the upstream certificate's SubjectAltName explicitly includes the IP and the upstream doesn't require a domain for SNI-based routing, you may hit certificate validation errors or SNI routing failures. Common approaches:
  - Bind a domain to the IP and use the domain as `serviceDomain`; or
  - Issue a certificate that includes the IP (less common).

Example (IP over HTTP):
```yaml
analyzer:
  serviceName: "analyzer.dns"
  servicePort: 80
  serviceDomain: "10.0.0.12"
  path: "/v1/chat/completions"
  apiToken: "sk-***"
  model: "qwen2.5-coder-32b"
```

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


