// Package mihomo 访问 mihomo(Clash.Meta) 的 external-controller，
// 用于给管理面板提供「出网代理节点状态 + 节点切换」能力。
package mihomo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	defaultTestURL        = "http://www.gstatic.com/generate_204"
	defaultRequestTimeout = 8 * time.Second
	maxTestConcurrency    = 8
)

// ErrDisabled 表示没有配置 mihomo 控制器，节点面板不可用。
var ErrDisabled = errors.New("mihomo controller is not configured")

// ErrUnknownNode 表示请求切换的节点不在当前分组里。
var ErrUnknownNode = errors.New("proxy node is not available in the current group")

// Options 是客户端配置。
type Options struct {
	Controller string
	Secret     string
	Group      string
	TestURL    string
	HTTPClient *http.Client
}

// Client 封装 mihomo REST API。
type Client struct {
	controller string
	secret     string
	group      string
	testURL    string
	httpClient *http.Client
}

// New 创建客户端。Controller 为空时返回的客户端处于禁用状态，
// 所有调用都会返回 ErrDisabled。
func New(options Options) *Client {
	group := strings.TrimSpace(options.Group)
	if group == "" {
		group = "GLOBAL"
	}
	testURL := strings.TrimSpace(options.TestURL)
	if testURL == "" {
		testURL = defaultTestURL
	}
	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultRequestTimeout}
	}
	return &Client{
		controller: strings.TrimRight(strings.TrimSpace(options.Controller), "/"),
		secret:     strings.TrimSpace(options.Secret),
		group:      group,
		testURL:    testURL,
		httpClient: httpClient,
	}
}

// Enabled 表示是否配置了控制器。
func (c *Client) Enabled() bool {
	return c != nil && c.controller != ""
}

// Group 返回当前操作的分组名。
func (c *Client) Group() string {
	if c == nil {
		return ""
	}
	return c.group
}

// TestURL 返回测速使用的探针地址。
func (c *Client) TestURL() string {
	if c == nil {
		return ""
	}
	return c.testURL
}

type historyPoint struct {
	Time  time.Time `json:"time"`
	Delay int       `json:"delay"`
}

type proxyEntry struct {
	Name    string         `json:"name"`
	Type    string         `json:"type"`
	Now     string         `json:"now"`
	All     []string       `json:"all"`
	Alive   bool           `json:"alive"`
	UDP     bool           `json:"udp"`
	History []historyPoint `json:"history"`
}

// providerEntry 是 /providers/proxies 的运行时信息。
type providerEntry struct {
	Name        string       `json:"name"`
	VehicleType string       `json:"vehicleType"`
	UpdatedAt   string       `json:"updatedAt"`
	Proxies     []proxyEntry `json:"proxies"`
}

type providersPayload struct {
	Providers map[string]providerEntry `json:"providers"`
}

// ProviderRuntime 是 provider 的运行时状态。
type ProviderRuntime struct {
	Name        string `json:"name"`
	VehicleType string `json:"vehicleType"`
	UpdatedAt   string `json:"updatedAt"`
	NodeCount   int    `json:"nodeCount"`
}

type proxiesPayload struct {
	Proxies map[string]proxyEntry `json:"proxies"`
}

// Node 是返回给面板的节点信息。
type Node struct {
	Name         string         `json:"name"`
	Type         string         `json:"type"`
	Delay        int            `json:"delay"`
	History      []historyPoint `json:"history"`
	Alive        bool           `json:"alive"`
	InAuto       bool           `json:"inAuto"`
	Selected     bool           `json:"selected"`
	AutoSelected bool           `json:"autoSelected"`
	UDP          bool           `json:"udp"`
	Provider     string         `json:"provider,omitempty"`
}

// GroupInfo 描述一个策略组。
type GroupInfo struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Now   string `json:"now"`
	Alive bool   `json:"alive"`
	Count int    `json:"count"`
}

// Overview 是面板渲染需要的完整快照。
type Overview struct {
	Enabled     bool       `json:"enabled"`
	Available   bool       `json:"available"`
	Error       string     `json:"error,omitempty"`
	Group       GroupInfo  `json:"group"`
	AutoGroup   *GroupInfo `json:"autoGroup"`
	Nodes       []Node     `json:"nodes"`
	TestURL     string     `json:"testUrl"`
	CollectedAt int64      `json:"collectedAt"`
}

// Overview 拉取分组状态与节点列表。
func (c *Client) Overview(ctx context.Context) (*Overview, error) {
	if !c.Enabled() {
		return nil, ErrDisabled
	}

	var payload proxiesPayload
	if err := c.doJSON(ctx, http.MethodGet, "/proxies", nil, &payload); err != nil {
		return nil, err
	}

	group, ok := payload.Proxies[c.group]
	if !ok {
		return nil, fmt.Errorf("proxy group %q not found in mihomo config", c.group)
	}

	overview := &Overview{
		Enabled:     true,
		Available:   true,
		TestURL:     c.testURL,
		CollectedAt: time.Now().UnixMilli(),
		Group: GroupInfo{
			Name:  group.Name,
			Type:  group.Type,
			Now:   group.Now,
			Alive: group.Alive,
			Count: len(group.All),
		},
		Nodes: make([]Node, 0, len(group.All)),
	}

	providerNodes, providerOf := c.providerProxies(ctx)

	// 找出分组里内嵌的自动选择组（fallback / url-test），面板要单独展示。
	var autoGroup *proxyEntry
	seen := make(map[string]struct{}, len(group.All))
	for _, member := range group.All {
		if _, duplicated := seen[member]; duplicated {
			continue
		}
		// 同名节点优先取 config.yaml 里的内联定义，避免同一节点出现两次。
		entry, exists := payload.Proxies[member]
		provider := ""
		if !exists {
			entry, exists = providerNodes[member]
			provider = providerOf[member]
		}
		if !exists {
			continue
		}
		seen[member] = struct{}{}
		if isAutoGroupType(entry.Type) && autoGroup == nil {
			copied := entry
			autoGroup = &copied
			continue
		}
		if isGroupType(entry.Type) {
			// 其它自定义策略组：不作为可选节点暴露，避免误切换到嵌套组。
			continue
		}
		overview.Nodes = append(overview.Nodes, buildNodeWithProvider(entry, group.Now, provider))
	}

	if autoGroup != nil {
		overview.AutoGroup = &GroupInfo{
			Name:  autoGroup.Name,
			Type:  autoGroup.Type,
			Now:   autoGroup.Now,
			Alive: autoGroup.Alive,
			Count: len(autoGroup.All),
		}
		autoMembers := make(map[string]struct{}, len(autoGroup.All))
		for _, member := range autoGroup.All {
			autoMembers[member] = struct{}{}
		}
		for index := range overview.Nodes {
			node := &overview.Nodes[index]
			if _, exists := autoMembers[node.Name]; exists {
				node.InAuto = true
			}
			if node.Name == autoGroup.Now {
				node.AutoSelected = true
			}
		}
	}

	return overview, nil
}

func buildNode(entry proxyEntry, groupNow string) Node {
	return buildNodeWithProvider(entry, groupNow, "")
}

func buildNodeWithProvider(entry proxyEntry, groupNow string, provider string) Node {
	delay := -1
	if len(entry.History) > 0 {
		last := entry.History[len(entry.History)-1]
		if last.Delay > 0 {
			delay = last.Delay
		}
	}
	history := entry.History
	if history == nil {
		history = []historyPoint{}
	}
	return Node{
		Name:     entry.Name,
		Type:     entry.Type,
		Delay:    delay,
		History:  history,
		Alive:    entry.Alive,
		Selected: groupNow == entry.Name,
		UDP:      entry.UDP,
		Provider: provider,
	}
}

// Select 手动把分组切到指定节点（或内嵌的自动选择组）。
func (c *Client) Select(ctx context.Context, name string) error {
	if !c.Enabled() {
		return ErrDisabled
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return ErrUnknownNode
	}

	var payload proxiesPayload
	if err := c.doJSON(ctx, http.MethodGet, "/proxies", nil, &payload); err != nil {
		return err
	}
	group, ok := payload.Proxies[c.group]
	if !ok {
		return fmt.Errorf("proxy group %q not found in mihomo config", c.group)
	}
	allowed := false
	for _, member := range group.All {
		if member == name {
			allowed = true
			break
		}
	}
	if !allowed {
		return ErrUnknownNode
	}

	body := map[string]string{"name": name}
	return c.doJSON(ctx, http.MethodPut, "/proxies/"+url.PathEscape(c.group), body, nil)
}

// DelayResult 是单个节点的测速结果。
type DelayResult struct {
	Name  string `json:"name"`
	Delay int    `json:"delay"`
	Error string `json:"error,omitempty"`
}

// Test 并发测量指定节点的延迟，names 为空时测量分组内全部节点。
func (c *Client) Test(ctx context.Context, names []string, timeout time.Duration) ([]DelayResult, error) {
	if !c.Enabled() {
		return nil, ErrDisabled
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	targets := names
	if len(targets) == 0 {
		overview, err := c.Overview(ctx)
		if err != nil {
			return nil, err
		}
		targets = make([]string, 0, len(overview.Nodes))
		for _, node := range overview.Nodes {
			targets = append(targets, node.Name)
		}
	}

	results := make([]DelayResult, len(targets))
	sem := make(chan struct{}, maxTestConcurrency)
	var wg sync.WaitGroup
	for index, name := range targets {
		wg.Add(1)
		go func(index int, name string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			delay, err := c.testOne(ctx, name, timeout)
			result := DelayResult{Name: name, Delay: delay}
			if err != nil {
				result.Delay = -1
				result.Error = err.Error()
			}
			results[index] = result
		}(index, name)
	}
	wg.Wait()

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

func (c *Client) testOne(ctx context.Context, name string, timeout time.Duration) (int, error) {
	query := url.Values{}
	query.Set("timeout", fmt.Sprintf("%d", timeout.Milliseconds()))
	query.Set("url", c.testURL)
	path := "/proxies/" + url.PathEscape(name) + "/delay?" + query.Encode()

	var payload struct {
		Delay int `json:"delay"`
	}
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &payload); err != nil {
		return -1, err
	}
	if payload.Delay <= 0 {
		return -1, errors.New("proxy delay test returned no result")
	}
	return payload.Delay, nil
}

func (c *Client) doJSON(ctx context.Context, method string, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode mihomo request: %w", err)
		}
		reader = strings.NewReader(string(encoded))
	}

	request, err := http.NewRequestWithContext(ctx, method, c.controller+path, reader)
	if err != nil {
		return fmt.Errorf("build mihomo request: %w", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if c.secret != "" {
		request.Header.Set("Authorization", "Bearer "+c.secret)
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("call mihomo api: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
	}()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("mihomo api %s %s returned %d", method, path, response.StatusCode)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(response.Body).Decode(out); err != nil {
		return fmt.Errorf("decode mihomo response: %w", err)
	}
	return nil
}

func isAutoGroupType(proxyType string) bool {
	switch strings.ToLower(proxyType) {
	case "fallback", "urltest", "url-test":
		return true
	default:
		return false
	}
}

func isGroupType(proxyType string) bool {
	switch strings.ToLower(proxyType) {
	case "selector", "fallback", "urltest", "url-test", "loadbalance", "load-balance", "relay":
		return true
	default:
		return false
	}
}
