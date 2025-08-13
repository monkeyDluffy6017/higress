package main

import (
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
		// wrapper.ProcessResponseHeaders(onHttpResponseHeaders),
		// wrapper.ProcessStreamingResponseBody(onHttpStreamingResponseBody),
		// wrapper.ProcessResponseBody(onHttpResponseBody),
		// wrapper.ProcessStreamDone(onHttpStreamDone),
	)
}

// 自定义插件配置
type Config struct {

	// 可配置的目的 LLM Provider 列表（需与 ai-proxy 的 providers[].id 保持一致）
	allowedProviderIds []string
	// 可选：当业务逻辑没有明确指定时的默认目的 LLM
	defaultProviderId string
}

// 在控制台插件配置中填写的yaml配置会自动转换为json，此处直接从json这个参数里解析配置即可
func parseConfig(json gjson.Result, config *Config, log logs.Log) error {

	// 解析允许的目的 LLM 列表
	config.allowedProviderIds = make([]string, 0)
	for _, v := range json.Get("allowedProviderIds").Array() {
		if s := v.String(); s != "" {
			config.allowedProviderIds = append(config.allowedProviderIds, s)
		}
	}
	// 可选默认 providerId
	config.defaultProviderId = json.Get("defaultProviderId").String()
	return nil
}

func onHttpRequestHeaders(ctx wrapper.HttpContext, config Config, log logs.Log) types.Action {
	// 示例：当 mockEnable=true 时，演示如何设置目的 LLM（按需替换为你们真实逻辑）
	if config.mockEnable {
		ctx.DisableReroute()
		// 业务侧应根据模型/用户等自行选择 providerId；此处演示从配置中选择
		providerId := config.defaultProviderId
		if providerId == "" && len(config.allowedProviderIds) > 0 {
			providerId = config.allowedProviderIds[0]
		}
		if providerId != "" {
			_ = proxywasm.ReplaceHttpRequestHeader("X-HI-Provider-Id", providerId)
		}
	}
	return types.HeaderContinue
}

func onHttpRequestBody(ctx wrapper.HttpContext, config Config, body []byte) types.Action {
	return types.ActionContinue
}

func onHttpResponseHeaders(ctx wrapper.HttpContext, config Config) types.Action {
	return types.ActionContinue
}

func onHttpResponseBody(ctx wrapper.HttpContext, config Config, body []byte) types.Action {
	return types.ActionContinue
}
