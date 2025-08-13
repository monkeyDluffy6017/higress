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
		"ai-llm-router",
		wrapper.ParseConfigBy(parseConfig),
		wrapper.ProcessRequestHeadersBy(onHttpRequestHeaders),
		wrapper.ProcessRequestBody(onHttpRequestBody),
		// wrapper.ProcessResponseHeaders(onHttpResponseHeaders),
		// wrapper.ProcessStreamingResponseBody(onHttpStreamingResponseBody),
		// wrapper.ProcessResponseBody(onHttpResponseBody),
		// wrapper.ProcessStreamDone(onHttpStreamDone),
	)
}

type Config struct {
}

type LLMEndpoint struct {
	IP           string `json:"ip,omitempty"`
	Port         int    `json:"port,omitempty"`
	HealthStatus string `json:"health_status,omitempty"`
}

type LLMCluster struct {
	ClusterName string        `json:"cluster_name"`
	Weight      int           `json:"weight"`
	Endpoints   []LLMEndpoint `json:"endpoints"`
	Provider    *ProviderInfo `json:"provider,omitempty"`
}

// ProviderCredential 既兼容字符串（直接作为凭证值），也兼容对象结构（header/value/secret_ref/type）
type ProviderCredential struct {
	RawString string `json:"-"`
	Header    string `json:"header,omitempty"`
	Value     string `json:"value,omitempty"`
	SecretRef string `json:"secret_ref,omitempty"`
	Type      string `json:"type,omitempty"`
}

func (c *ProviderCredential) UnmarshalJSON(b []byte) error {
	// 字符串形式
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		c.RawString = s
		c.Value = s
		return nil
	}
	// 对象形式
	var obj struct {
		Header    string `json:"header,omitempty"`
		Value     string `json:"value,omitempty"`
		SecretRef string `json:"secret_ref,omitempty"`
		Type      string `json:"type,omitempty"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return err
	}
	c.Header = obj.Header
	c.Value = obj.Value
	c.SecretRef = obj.SecretRef
	c.Type = obj.Type
	return nil
}

type ProviderInfo struct {
	Provider   string              `json:"provider,omitempty"`
	Type       string              `json:"type,omitempty"`
	BaseURL    string              `json:"base_url,omitempty"`
	Credential *ProviderCredential `json:"credential,omitempty"`
}

func parseConfig(json gjson.Result, config *Config, log logs.Log) error {
	return nil
}

func onHttpRequestHeaders(ctx wrapper.HttpContext, config Config, log logs.Log) types.Action {
	val, err := proxywasm.GetProperty([]string{"route", "all_llm_clusters"})
	if err != nil {
		log.Errorf("GetProperty route/all_llm_clusters error: %v", err)
		return types.HeaderContinue
	}

	var clusters []LLMCluster
	if uerr := json.Unmarshal(val, &clusters); uerr != nil {
		log.Errorf("Unmarshal route/all_llm_clusters error: %v, raw=%s", uerr, string(val))
		return types.HeaderContinue
	}

	pretty, _ := json.MarshalIndent(clusters, "", "  ")
	log.Infof("route/all_llm_clusters struct: %s", string(pretty))

	// 提取并输出每个 cluster 的 provider 关键信息（包含 base_url 与凭证摘要信息）
	for _, c := range clusters {
		if c.Provider == nil {
			continue
		}
		credHeader := ""
		credType := ""
		credValueLen := 0
		credSecretRef := ""
		if c.Provider.Credential != nil {
			credHeader = c.Provider.Credential.Header
			credType = c.Provider.Credential.Type
			if c.Provider.Credential.Value != "" {
				credValueLen = len(c.Provider.Credential.Value)
			} else if c.Provider.Credential.RawString != "" {
				credValueLen = len(c.Provider.Credential.RawString)
			}
			credSecretRef = c.Provider.Credential.SecretRef
		}
		log.Infof(
			"llm provider: cluster=%s, name=%s, type=%s, base_url=%s, cred_header=%s, cred_type=%s, cred_value_len=%d, cred_secret_ref=%s",
			c.ClusterName,
			c.Provider.Provider,
			c.Provider.Type,
			c.Provider.BaseURL,
			credHeader,
			credType,
			credValueLen,
			credSecretRef,
		)
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
