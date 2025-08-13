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

Configuration (now strategy-based)
```yaml
strategy:
  type: semantic                  # Strategy type; currently supports "semantic" (semantic-based selection)
  semantic:
    analyzer:
      enabled: true
      # Access analyzer via service-source (DNS). The upstream must be registered as a Higress service.
      serviceName: "analyzer.dns"     # required, Higress DNS service name
      servicePort: 443                 # optional, default 443
      serviceDomain: "api.example.com"# required, used as Host/SNI
      path: "/v1/chat/completions"    # required, request path
      apiToken: "sk-***"
      model: "qwen2.5-coder-32b"
      timeoutMs: 3000
      totalTimeoutMs: 10000
      maxInputBytes: 10240
      promptTemplate: ""
      protocol: "openai"
      # Optional: labels used for provider scoring and rule engine references
      labels:
        - build_new_project
        - add_new_feature
        - fix_bug
        - other
      # Required when labels are provided: subset used strictly for semantic classification.
      # When labels are not provided, this can be omitted and defaults to the built-in set.
      # If labels are provided but analysisLabels are missing, the plugin will return an error.
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

    # Rule Engine (declarative eligibility filtering, runs before preference strategy)
    ruleEngine:
      enabled: true
      # Only inlineRules is supported now
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
      # Loading rules from file has been removed (rulesFile is no longer supported)
```

Constraints
- `routing.candidates[].id` must match `providers[].id` from ai-proxy configuration.
- Only effective for requests with `Content-Type: application/json` and supported protocols.
- To protect sensitive data, code blocks are stripped and the analyzer input is truncated to `maxInputBytes`.
- Analyzer currently supports DNS service-source only. For HTTPS, use a domain as `serviceDomain` to satisfy certificate/SNI; if you must connect to an IP directly, use HTTP or bind a domain to that IP.
- When `labels` are customized and `promptTemplate` is not provided, the plugin auto-generates a minimal default prompt listing `analysisLabels`. If you need label definitions/descriptions, provide your own `promptTemplate` explicitly.

When `serviceDomain` is an IP
- You can set `serviceDomain` to an IP address. It will be used as the request Host (`:authority`) and SNI in TLS by default.
- HTTP: works out of the box (e.g., `servicePort: 80`, `serviceDomain: "10.0.0.12"`).
- HTTPS: unless the upstream certificate's SubjectAltName explicitly includes the IP and the upstream doesn't require a domain for SNI-based routing, you may hit certificate validation errors or SNI routing failures. Common approaches:
  - Bind a domain to the IP and use the domain as `serviceDomain`; or
  - Issue a certificate that includes the IP (less common).

Example (IP over HTTP):
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

Response header
- `x-select-llm: <providerId>`

Integration with ai-proxy
- ai-proxy supports per-request provider override via `X-HI-Provider-Id`.
- ai-llm-router only selects and sets the header; ai-proxy handles host/path/auth rewriting.

Usage with ai-proxy
1. Ensure candidates are configured in ai-proxy (including `providers[].id`).
2. In ai-llm-router, set `strategy.type=semantic` and configure `strategy.semantic.analyzer`, `strategy.semantic.inputExtraction`, and `strategy.semantic.routing.candidates`.
3. Ensure execution order: ai-llm-router first, ai-proxy next.

Notes
- No cache by default. If the analyzer cost is high, consider adding a cache (keyed by hashed input) later.
- If your request is not in OpenAI format, use `contentJsonPath` or extend `protocol` to customize extraction.

Rule Engine: optional request body fields (for building request_context and available_models)
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

Remarks
- The rule engine handles eligibility (which models can do the job), while the strategy handles preference (which is best among eligible ones).
- The engine output is written to request header `x-qualified-models` for observability or further strategy usage.


