module github.com/alibaba/higress/plugins/wasm-go/extensions/ai-quota

go 1.24.1

toolchain go1.24.3

replace github.com/alibaba/higress/plugins/wasm-go => ../..

require (
	github.com/alibaba/higress/plugins/wasm-go v0.0.0-00010101000000-000000000000
	github.com/go-jose/go-jose/v3 v3.0.3
	github.com/higress-group/proxy-wasm-go-sdk v0.0.0-20250611100342-5654e89a7a80
	github.com/tidwall/gjson v1.18.0
	github.com/tidwall/resp v0.1.1
)

require (
	github.com/google/uuid v1.6.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	golang.org/x/crypto v0.26.0 // indirect
)
