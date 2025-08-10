package main

import (
	"encoding/json"

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
	// ctx.DisableReroute()

	// 添加调试日志：检查wasm-go版本和host function支持情况
	log.Infof("=== 开始调试上游主机信息获取 ===")
	log.Infof("wasm-go版本信息准备检查...")

	// 获取命中路由下的所有 LLM 集群及端点，直接读取宿主 property（双 key 兼容）
	var raw []byte
	var err error

	// 方式1：NUL 分隔路径 ["route","all_llm_clusters"]
	raw, err = proxywasm.GetProperty([]string{"route", "all_llm_clusters"})
	if err != nil || len(raw) == 0 {
		log.Warnf("读取 [\"route\",\"all_llm_clusters\"] 失败: %v", err)
		// 方式2：扁平 key ["route_all_llm_clusters"]
		raw, err = proxywasm.GetProperty([]string{"route_all_llm_clusters"})
		if err != nil || len(raw) == 0 {
			log.Warnf("读取 [\"route_all_llm_clusters\"] 失败: %v", err)
		}
	}

	if err == nil && len(raw) > 0 {
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
			log.Errorf("解析 all_llm_clusters 失败: %v, 原始: %s", err, string(raw))
		}
	} else {
		log.Warnf("获取 all_llm_clusters 失败: %v", err)
		// 进一步诊断：检查是否已选路由（按仓库内其他插件的约定：route_name/cluster_name）
		if rname, rerr := proxywasm.GetProperty([]string{"route_name"}); rerr == nil && len(rname) > 0 {
			log.Infof("route_name: %s", string(rname))
		} else {
			log.Warnf("读取 route_name 失败: %v", rerr)
		}
		if cname, cerr := proxywasm.GetProperty([]string{"cluster_name"}); cerr == nil && len(cname) > 0 {
			log.Infof("cluster_name: %s", string(cname))
		} else {
			log.Warnf("读取 cluster_name 失败: %v", cerr)
		}
		// 标准属性诊断：request.path（若也失败，极可能是 GetProperty ABI/SDK 不兼容或补丁未生效）
		if rpath, perr := proxywasm.GetProperty([]string{"request", "path"}); perr == nil && len(rpath) > 0 {
			log.Infof("request.path: %s", string(rpath))
		} else {
			log.Warnf("读取 [\"request\",\"path\"] 失败: %v", perr)
		}
		if rpath2, perr2 := proxywasm.GetProperty([]string{"request_path"}); perr2 == nil && len(rpath2) > 0 {
			log.Infof("request_path: %s", string(rpath2))
		} else {
			log.Warnf("读取 [\"request_path\"] 失败: %v", perr2)
		}
		// 直接传入包含 NUL 的单元素路径，绕过 SDK 拼接差异
		if rawNul, nerr := proxywasm.GetProperty([]string{"route\x00all_llm_clusters"}); nerr == nil && len(rawNul) > 0 {
			log.Infof("通过单元素NUL键命中 all_llm_clusters, 长度=%d", len(rawNul))
		} else {
			log.Warnf("读取 [\"route\\x00all_llm_clusters\"] 失败: %v", nerr)
		}
		// 基线诊断：读取通用可用属性，确认 GetProperty 工作正常
		if prid, perr := proxywasm.GetProperty([]string{"plugin_root_id"}); perr == nil && len(prid) > 0 {
			log.Infof("plugin_root_id: %s", string(prid))
		} else {
			log.Warnf("读取 plugin_root_id 失败: %v", perr)
		}
	}

	return types.HeaderContinue
}

func onHttpRequestBody(ctx wrapper.HttpContext, config Config, body []byte) types.Action {
	// 添加调试日志：检查wasm-go版本和host function支持情况
	logs.Infof("=== 开始调试上游主机信息获取 ===")
	logs.Infof("wasm-go版本信息准备检查...")

	// 获取命中路由下的所有 LLM 集群及端点，直接读取宿主 property（双 key 兼容）
	var raw []byte
	var err error

	// 方式1：NUL 分隔路径 ["route","all_llm_clusters"]
	raw, err = proxywasm.GetProperty([]string{"route", "all_llm_clusters"})
	if err != nil || len(raw) == 0 {
		logs.Warnf("读取 [\"route\",\"all_llm_clusters\"] 失败: %v", err)
		// 方式2：扁平 key ["route_all_llm_clusters"]
		raw, err = proxywasm.GetProperty([]string{"route_all_llm_clusters"})
		if err != nil || len(raw) == 0 {
			logs.Warnf("读取 [\"route_all_llm_clusters\"] 失败: %v", err)
		}
	}

	if err == nil && len(raw) > 0 {
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
			logs.Infof("命中路由的 LLM 集群数: %d", len(clusters))
			for _, c := range clusters {
				logs.Infof("集群 %s (权重 %d)", c.ClusterName, c.Weight)
				for _, ep := range c.Endpoints {
					logs.Infof("  - %s:%d (%s)", ep.IP, ep.Port, ep.HealthStatus)
				}
			}
		} else {
			logs.Errorf("解析 all_llm_clusters 失败: %v, 原始: %s", err, string(raw))
		}
	} else {
		logs.Warnf("获取 all_llm_clusters 失败: %v", err)
		// 进一步诊断：检查是否已选路由
		if rname, rerr := proxywasm.GetProperty([]string{"route", "name"}); rerr == nil && len(rname) > 0 {
			logs.Infof("route.name: %s", string(rname))
		} else {
			logs.Warnf("读取 route.name 失败: %v", rerr)
		}
		// 基线诊断：读取通用可用属性，确认 GetProperty 工作正常
		if prid, perr := proxywasm.GetProperty([]string{"plugin_root_id"}); perr == nil && len(prid) > 0 {
			logs.Infof("plugin_root_id: %s", string(prid))
		} else {
			logs.Warnf("读取 plugin_root_id 失败: %v", perr)
		}
	}

	return types.ActionContinue
}

func onHttpResponseHeaders(ctx wrapper.HttpContext, config Config) types.Action {
	return types.ActionContinue
}

func onHttpResponseBody(ctx wrapper.HttpContext, config Config, body []byte) types.Action {
	return types.ActionContinue
}
