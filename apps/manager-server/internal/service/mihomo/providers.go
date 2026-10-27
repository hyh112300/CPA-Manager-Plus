package mihomo

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// ReloadConfig 让 mihomo 重新加载指定配置文件。
func (c *Client) ReloadConfig(ctx context.Context, path string) error {
	if !c.Enabled() {
		return ErrDisabled
	}
	body := map[string]string{"path": path}
	return c.doJSON(ctx, http.MethodPut, "/configs?force=true", body, nil)
}

// Providers 返回所有 provider 的运行时状态。
func (c *Client) Providers(ctx context.Context) ([]ProviderRuntime, error) {
	if !c.Enabled() {
		return nil, ErrDisabled
	}
	var payload providersPayload
	if err := c.doJSON(ctx, http.MethodGet, "/providers/proxies", nil, &payload); err != nil {
		return nil, err
	}

	result := make([]ProviderRuntime, 0, len(payload.Providers))
	for name, entry := range payload.Providers {
		result = append(result, ProviderRuntime{
			Name:        name,
			VehicleType: entry.VehicleType,
			UpdatedAt:   entry.UpdatedAt,
			NodeCount:   len(entry.Proxies),
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

// providerProxies 返回 provider 里的节点（它们不会出现在 /proxies 里）
// 以及 节点名 -> provider 名 的归属映射。
func (c *Client) providerProxies(ctx context.Context) (map[string]proxyEntry, map[string]string) {
	nodes := map[string]proxyEntry{}
	ownership := map[string]string{}
	var payload providersPayload
	if err := c.doJSON(ctx, http.MethodGet, "/providers/proxies", nil, &payload); err != nil {
		return nodes, ownership
	}
	for name, provider := range payload.Providers {
		// Compatible provider 是 mihomo 为分组内联节点生成的影子 provider，
		// 不是真正的订阅来源，忽略它才能区分「内联节点」和「导入节点」。
		if strings.EqualFold(provider.VehicleType, "Compatible") {
			continue
		}
		for _, proxy := range provider.Proxies {
			if proxy.Name == "" {
				continue
			}
			owned := proxy
			owned.Name = proxy.Name
			// provider 响应里缺少部分字段时按 group 成员名补全
			nodes[proxy.Name] = owned
			ownership[proxy.Name] = name
		}
	}
	return nodes, ownership
}

// UpdateProvider 触发一次 provider 更新（订阅重新下载 / 本地文件重读）。
func (c *Client) UpdateProvider(ctx context.Context, name string) error {
	if !c.Enabled() {
		return ErrDisabled
	}
	return c.doJSON(ctx, http.MethodPut, "/providers/proxies/"+url.PathEscape(name), nil, nil)
}

// HealthCheckProvider 触发 provider 的健康检查。
func (c *Client) HealthCheckProvider(ctx context.Context, name string, timeout time.Duration) error {
	if !c.Enabled() {
		return ErrDisabled
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	path := fmt.Sprintf("/providers/proxies/%s/healthcheck?timeout=%d", url.PathEscape(name), timeout.Milliseconds())
	return c.doJSON(ctx, http.MethodGet, path, nil, nil)
}

// GroupDelay 触发分组内所有节点的延迟测试，返回 节点名 -> 延迟。
func (c *Client) GroupDelay(ctx context.Context, name string, timeout time.Duration) (map[string]int, error) {
	if !c.Enabled() {
		return nil, ErrDisabled
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	query := url.Values{}
	query.Set("timeout", fmt.Sprintf("%d", timeout.Milliseconds()))
	query.Set("url", c.testURL)
	path := fmt.Sprintf("/group/%s/delay?%s", url.PathEscape(name), query.Encode())

	var payload map[string]int
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// HealthCheckResult 是一次探活的汇总。
type HealthCheckResult struct {
	Delays    map[string]int      `json:"delays"`
	Providers []ProviderHealthLog `json:"providers"`
}

// ProviderHealthLog 记录单个 provider 的探活结果。
type ProviderHealthLog struct {
	Name  string `json:"name"`
	Error string `json:"error,omitempty"`
}

// HealthCheck 对分组与所有 provider 执行一次健康检查。
func (c *Client) HealthCheck(ctx context.Context, timeout time.Duration) (*HealthCheckResult, error) {
	if !c.Enabled() {
		return nil, ErrDisabled
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	providers, err := c.Providers(ctx)
	if err != nil {
		return nil, err
	}

	result := &HealthCheckResult{
		Delays:    map[string]int{},
		Providers: make([]ProviderHealthLog, 0, len(providers)),
	}
	for _, provider := range providers {
		if provider.VehicleType != "File" && provider.VehicleType != "HTTP" {
			continue
		}
		log := ProviderHealthLog{Name: provider.Name}
		if err := c.HealthCheckProvider(ctx, provider.Name, timeout); err != nil {
			log.Error = err.Error()
		}
		result.Providers = append(result.Providers, log)
	}

	if delays, err := c.GroupDelay(ctx, c.group, timeout); err == nil {
		for name, delay := range delays {
			if delay > 0 {
				result.Delays[name] = delay
			}
		}
	}

	return result, nil
}
