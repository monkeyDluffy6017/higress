package main

import (
	"encoding/json"

	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm"
	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm/types"
	logs "github.com/higress-group/wasm-go/pkg/log"
	"github.com/higress-group/wasm-go/pkg/wrapper"
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
		wrapper.ProcessResponseBody(onHttpResponseBody),
		// wrapper.ProcessStreamDone(onHttpStreamDone),
	)
}

// 自定义插件配置
type Config struct {
	mockEnable bool
}

// 在控制台插件配置中填写的yaml配置会自动转换为json，此处直接从json这个参数里解析配置即可
func parseConfig(json gjson.Result, config *Config, log logs.Log) error {
	// 解析出配置，更新到config中
	config.mockEnable = json.Get("mockEnable").Bool()
	return nil
}

// func onHttpRequestHeaders(ctx wrapper.HttpContext, config MyConfig, log logs.Log) types.Action {
// 	proxywasm.AddHttpRequestHeader("hello", "world")
// 	if config.mockEnable {
// 		proxywasm.SendHttpResponse(200, nil, []byte("hello world"), -1)
// 	}
// 	return types.HeaderContinue
// }

func onHttpRequestHeaders(ctx wrapper.HttpContext, config Config, log logs.Log) types.Action {
	ctx.DisableReroute()

	// 添加调试日志：检查wasm-go版本和host function支持情况
	log.Infof("=== 开始调试上游主机信息获取 ===")
	log.Infof("wasm-go版本信息准备检查...")

	// 获取命中路由下的所有 LLM 集群及端点（新接口）
	if raw, err := proxywasm.GetAllLLMClusters(); err == nil {
		var clusters []struct {
			ClusterName string `json:"cluster_name"`
			Weight      int    `json:"weight"`
			Endpoints   []struct {
				IP           string `json:"ip"`
				Port         int    `json:"port"`
				HealthStatus string `json:"health_status"`
			} `json:"endpoints"`
		}
		if err := json.Unmarshal(raw, &clusters); err == nil {
			log.Infof("命中路由的 LLM 集群数: %d", len(clusters))
			for _, c := range clusters {
				log.Infof("集群 %s (权重 %d)", c.ClusterName, c.Weight)
				for _, ep := range c.Endpoints {
					log.Infof("  - %s:%d (%s)", ep.IP, ep.Port, ep.HealthStatus)
				}
			}
		} else {
			log.Errorf("解析 all_llm_clusters 失败: %v", err)
		}
	} else {
		log.Warnf("获取 all_llm_clusters 失败: %v", err)
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
