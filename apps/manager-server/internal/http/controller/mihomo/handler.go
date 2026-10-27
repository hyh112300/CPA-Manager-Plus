package mihomo

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
	mihomosvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/mihomo"
)

const (
	basePath   = "/v0/management/proxy-nodes"
	selectPath = basePath + "/select"
	testPath   = basePath + "/test"
	healthPath = basePath + "/healthcheck"
	sourcePath = basePath + "/sources"
	nodePath   = basePath + "/nodes"

	defaultTestTimeout   = 5 * time.Second
	maxTestTimeout       = 30 * time.Second
	maxImportedNodeCount = 500
)

type Handler struct {
	App *app.Context
}

type overviewResponse struct {
	*mihomosvc.Overview
	CanEdit bool `json:"canEdit"`
}

type sourceItem struct {
	Name            string   `json:"name"`
	Type            string   `json:"type"`
	URL             string   `json:"url,omitempty"`
	IntervalSeconds int      `json:"intervalSeconds,omitempty"`
	UpdatedAt       string   `json:"updatedAt,omitempty"`
	NodeCount       int      `json:"nodeCount"`
	Excluded        []string `json:"excluded,omitempty"`
	Builtin         bool     `json:"builtin"`
}

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	if !middleware.AuthorizePanel(w, r, h.App.AdminAuthService) {
		return
	}

	path := strings.TrimRight(r.URL.Path, "/")
	switch {
	case path == basePath:
		if r.Method != http.MethodGet {
			response.MethodNotAllowed(w)
			return
		}
		h.handleOverview(w, r)
	case path == selectPath:
		if r.Method != http.MethodPost {
			response.MethodNotAllowed(w)
			return
		}
		h.handleSelect(w, r)
	case path == testPath:
		if r.Method != http.MethodPost {
			response.MethodNotAllowed(w)
			return
		}
		h.handleTest(w, r)
	case path == healthPath:
		if r.Method != http.MethodPost {
			response.MethodNotAllowed(w)
			return
		}
		h.handleHealthCheck(w, r)
	case path == sourcePath:
		switch r.Method {
		case http.MethodGet:
			h.handleListSources(w, r)
		case http.MethodPost:
			h.handleAddSource(w, r)
		default:
			response.MethodNotAllowed(w)
		}
	case path == nodePath:
		switch r.Method {
		case http.MethodGet:
			h.handleListImportedNodes(w, r)
		case http.MethodPost:
			h.handleImportNodes(w, r)
		default:
			response.MethodNotAllowed(w)
		}
	case strings.HasPrefix(path, sourcePath+"/"):
		h.handleSourceAction(w, r, strings.TrimPrefix(path, sourcePath+"/"))
	case strings.HasPrefix(path, nodePath+"/"):
		h.handleNodeAction(w, r, strings.TrimPrefix(path, nodePath+"/"))
	default:
		response.MethodNotAllowed(w)
	}
}

func (h *Handler) handleOverview(w http.ResponseWriter, r *http.Request) {
	client := h.App.MihomoService
	canEdit := h.App.MihomoConfig != nil && h.App.MihomoConfig.Enabled()

	if client == nil || !client.Enabled() {
		h.writeOverview(w, &mihomosvc.Overview{
			Enabled:   false,
			Available: false,
			Error:     mihomosvc.ErrDisabled.Error(),
			Nodes:     []mihomosvc.Node{},
		}, canEdit)
		return
	}

	overview, err := client.Overview(r.Context())
	if err != nil {
		if errors.Is(err, mihomosvc.ErrDisabled) {
			h.writeOverview(w, &mihomosvc.Overview{
				Enabled:   false,
				Available: false,
				Error:     err.Error(),
				Nodes:     []mihomosvc.Node{},
			}, canEdit)
			return
		}
		h.writeOverview(w, &mihomosvc.Overview{
			Enabled:   true,
			Available: false,
			Error:     err.Error(),
			Group:     mihomosvc.GroupInfo{Name: client.Group()},
			TestURL:   client.TestURL(),
			Nodes:     []mihomosvc.Node{},
		}, canEdit)
		return
	}

	h.writeOverview(w, overview, canEdit)
}

func (h *Handler) writeOverview(w http.ResponseWriter, overview *mihomosvc.Overview, canEdit bool) {
	response.JSON(w, http.StatusOK, overviewResponse{Overview: overview, CanEdit: canEdit})
}

func (h *Handler) handleSelect(w http.ResponseWriter, r *http.Request) {
	client := h.App.MihomoService
	if client == nil || !client.Enabled() {
		response.Error(w, http.StatusConflict, mihomosvc.ErrDisabled)
		return
	}

	var payload struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		response.Error(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(payload.Name) == "" {
		response.Error(w, http.StatusBadRequest, errors.New("name is required"))
		return
	}

	if err := client.Select(r.Context(), payload.Name); err != nil {
		if errors.Is(err, mihomosvc.ErrUnknownNode) {
			response.Error(w, http.StatusBadRequest, err)
			return
		}
		response.Error(w, http.StatusBadGateway, err)
		return
	}

	h.respondWithOverview(w, r)
}

func (h *Handler) handleTest(w http.ResponseWriter, r *http.Request) {
	client := h.App.MihomoService
	if client == nil || !client.Enabled() {
		response.Error(w, http.StatusConflict, mihomosvc.ErrDisabled)
		return
	}

	payload, err := decodeTestRequest(r)
	if err != nil {
		response.Error(w, http.StatusBadRequest, err)
		return
	}

	results, err := client.Test(r.Context(), payload.Names, payload.timeout())
	if err != nil {
		response.Error(w, http.StatusBadGateway, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]any{"results": results})
}

func (h *Handler) handleHealthCheck(w http.ResponseWriter, r *http.Request) {
	client := h.App.MihomoService
	if client == nil || !client.Enabled() {
		response.Error(w, http.StatusConflict, mihomosvc.ErrDisabled)
		return
	}

	payload, err := decodeTestRequest(r)
	if err != nil {
		response.Error(w, http.StatusBadRequest, err)
		return
	}

	ctx, cancel := contextWithTimeout(r, maxTestTimeout)
	defer cancel()
	result, err := client.HealthCheck(ctx, payload.timeout())
	if err != nil {
		response.Error(w, http.StatusBadGateway, err)
		return
	}

	overview, overviewErr := client.Overview(r.Context())
	body := map[string]any{
		"delays":    result.Delays,
		"providers": result.Providers,
	}
	if overviewErr == nil {
		body["overview"] = overview
	}
	response.JSON(w, http.StatusOK, body)
}

func (h *Handler) handleListSources(w http.ResponseWriter, r *http.Request) {
	editor := h.App.MihomoConfig
	if editor == nil || !editor.Enabled() {
		response.JSON(w, http.StatusOK, map[string]any{"sources": []sourceItem{}, "canEdit": false})
		return
	}

	providers, err := editor.ListProviders()
	if err != nil {
		response.Error(w, http.StatusInternalServerError, err)
		return
	}

	exclusions, err := editor.Exclusions()
	if err != nil {
		response.Error(w, http.StatusInternalServerError, err)
		return
	}

	runtime := map[string]mihomosvc.ProviderRuntime{}
	if h.App.MihomoService != nil && h.App.MihomoService.Enabled() {
		if items, runtimeErr := h.App.MihomoService.Providers(r.Context()); runtimeErr == nil {
			for _, item := range items {
				runtime[item.Name] = item
			}
		}
	}

	sources := make([]sourceItem, 0, len(providers))
	for _, provider := range providers {
		item := sourceItem{
			Name:            provider.Name,
			Type:            provider.Type,
			URL:             provider.URL,
			IntervalSeconds: provider.IntervalSeconds,
			Excluded:        exclusions[provider.Name],
			Builtin:         provider.Builtin,
		}
		if state, ok := runtime[provider.Name]; ok {
			item.UpdatedAt = state.UpdatedAt
			item.NodeCount = state.NodeCount
		}
		sources = append(sources, item)
	}
	response.JSON(w, http.StatusOK, map[string]any{"sources": sources, "canEdit": true})
}

func (h *Handler) handleAddSource(w http.ResponseWriter, r *http.Request) {
	editor := h.requireEditor(w)
	if editor == nil {
		return
	}

	var payload struct {
		Name            string `json:"name"`
		URL             string `json:"url"`
		IntervalSeconds int    `json:"intervalSeconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		response.Error(w, http.StatusBadRequest, err)
		return
	}

	name, err := editor.AddSubscription(payload.Name, payload.URL, payload.IntervalSeconds)
	if err != nil {
		response.Error(w, http.StatusBadRequest, err)
		return
	}

	if h.App.MihomoService != nil && h.App.MihomoService.Enabled() {
		ctx, cancel := contextWithTimeout(r, 20*time.Second)
		defer cancel()
		_ = h.App.MihomoService.UpdateProvider(ctx, name)
	}
	response.JSON(w, http.StatusOK, map[string]any{"ok": true, "name": name})
}

func (h *Handler) handleSourceAction(w http.ResponseWriter, r *http.Request, remainder string) {
	editor := h.requireEditor(w)
	if editor == nil {
		return
	}

	// {provider}/nodes/{nodeName}
	if providerPart, nodePart, found := strings.Cut(remainder, "/nodes/"); found {
		if r.Method != http.MethodDelete {
			response.MethodNotAllowed(w)
			return
		}
		provider, err := url.PathUnescape(providerPart)
		if err != nil || strings.TrimSpace(provider) == "" {
			response.Error(w, http.StatusBadRequest, errors.New("invalid provider name"))
			return
		}
		nodeName, err := url.PathUnescape(nodePart)
		if err != nil || strings.TrimSpace(nodeName) == "" {
			response.Error(w, http.StatusBadRequest, errors.New("invalid node name"))
			return
		}
		if err := h.removeProviderNode(r, editor, provider, nodeName); err != nil {
			response.Error(w, http.StatusBadRequest, err)
			return
		}
		response.JSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}

	// {provider}/restore
	if strings.HasSuffix(remainder, "/restore") {
		if r.Method != http.MethodPost {
			response.MethodNotAllowed(w)
			return
		}
		provider, err := url.PathUnescape(strings.TrimSuffix(remainder, "/restore"))
		if err != nil || strings.TrimSpace(provider) == "" {
			response.Error(w, http.StatusBadRequest, errors.New("invalid provider name"))
			return
		}
		if err := editor.RestoreProviderNodes(provider); err != nil {
			response.Error(w, http.StatusBadRequest, err)
			return
		}
		response.JSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}

	// {provider}/update
	if strings.HasSuffix(remainder, "/update") {
		if r.Method != http.MethodPost {
			response.MethodNotAllowed(w)
			return
		}
		name, err := url.PathUnescape(strings.TrimSuffix(remainder, "/update"))
		if err != nil || strings.TrimSpace(name) == "" {
			response.Error(w, http.StatusBadRequest, errors.New("invalid provider name"))
			return
		}
		if h.App.MihomoService == nil || !h.App.MihomoService.Enabled() {
			response.Error(w, http.StatusConflict, mihomosvc.ErrDisabled)
			return
		}
		ctx, cancel := contextWithTimeout(r, 30*time.Second)
		defer cancel()
		if err := h.App.MihomoService.UpdateProvider(ctx, name); err != nil {
			response.Error(w, http.StatusBadGateway, err)
			return
		}
		response.JSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}

	if r.Method != http.MethodDelete {
		response.MethodNotAllowed(w)
		return
	}
	name, err := url.PathUnescape(remainder)
	if err != nil || strings.TrimSpace(name) == "" {
		response.Error(w, http.StatusBadRequest, errors.New("invalid provider name"))
		return
	}
	if err := editor.RemoveProvider(name); err != nil {
		response.Error(w, http.StatusBadRequest, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]any{"ok": true})
}

// removeProviderNode 删除 provider 内的单个节点：
// 本地导入的 provider 直接从文件里摘除，订阅型 provider 写入 exclude-filter。
func (h *Handler) removeProviderNode(r *http.Request, editor *mihomosvc.ConfigEditor, provider string, nodeName string) error {
	if provider == mihomosvc.ImportedProviderName {
		return editor.RemoveImportedNode(nodeName)
	}

	if err := editor.HideProviderNode(provider, nodeName); err != nil {
		return err
	}
	if h.App.MihomoService != nil && h.App.MihomoService.Enabled() {
		ctx, cancel := contextWithTimeout(r, 5*time.Second)
		defer cancel()
		h.App.MihomoService.WaitForNodesGone(ctx, []string{nodeName}, 3*time.Second)
	}
	return nil
}

func (h *Handler) handleListImportedNodes(w http.ResponseWriter, r *http.Request) {
	editor := h.App.MihomoConfig
	if editor == nil || !editor.Enabled() {
		response.JSON(w, http.StatusOK, map[string]any{"nodes": []string{}, "canEdit": false})
		return
	}
	nodes, err := editor.ImportedNodes()
	if err != nil {
		response.Error(w, http.StatusInternalServerError, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]any{"nodes": nodes, "canEdit": true})
}

func (h *Handler) handleImportNodes(w http.ResponseWriter, r *http.Request) {
	editor := h.requireEditor(w)
	if editor == nil {
		return
	}

	var payload struct {
		Links []string `json:"links"`
		YAML  string   `json:"yaml"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		response.Error(w, http.StatusBadRequest, err)
		return
	}

	nodes, failures := mihomosvc.ParseShareLinks(payload.Links)
	if strings.TrimSpace(payload.YAML) != "" {
		parsed, err := mihomosvc.ExtractNodesFromYAML([]byte(payload.YAML))
		if err != nil {
			response.Error(w, http.StatusBadRequest, err)
			return
		}
		nodes = append(nodes, parsed...)
	}

	if len(nodes) == 0 {
		if len(failures) > 0 {
			response.Error(w, http.StatusBadRequest, errors.New(strings.Join(failures, "; ")))
			return
		}
		response.Error(w, http.StatusBadRequest, errors.New("no proxy node to import"))
		return
	}
	if len(nodes) > maxImportedNodeCount {
		response.Error(w, http.StatusBadRequest, errors.New("too many nodes in one import request"))
		return
	}

	added, names, err := editor.ImportNodes(nodes)
	if err != nil {
		response.Error(w, http.StatusBadRequest, err)
		return
	}

	// mihomo reload 后 provider 是异步挂载的，等节点真正进入分组再返回，避免前端拿到旧列表。
	if h.App.MihomoService != nil && h.App.MihomoService.Enabled() {
		ctx, cancel := contextWithTimeout(r, 5*time.Second)
		defer cancel()
		waitNames := make([]string, 0, len(nodes))
		for _, node := range nodes {
			if name, ok := node["name"].(string); ok {
				waitNames = append(waitNames, name)
			}
		}
		h.App.MihomoService.WaitForNodes(ctx, waitNames, 3*time.Second)
	}
	response.JSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"added":    added,
		"total":    len(names),
		"failures": failures,
	})
}

func (h *Handler) handleNodeAction(w http.ResponseWriter, r *http.Request, remainder string) {
	editor := h.requireEditor(w)
	if editor == nil {
		return
	}
	if r.Method != http.MethodDelete {
		response.MethodNotAllowed(w)
		return
	}
	name, err := url.PathUnescape(remainder)
	if err != nil || strings.TrimSpace(name) == "" {
		response.Error(w, http.StatusBadRequest, errors.New("invalid node name"))
		return
	}
	if err := editor.RemoveImportedNode(name); err != nil {
		response.Error(w, http.StatusBadRequest, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) respondWithOverview(w http.ResponseWriter, r *http.Request) {
	client := h.App.MihomoService
	if client == nil || !client.Enabled() {
		response.JSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	overview, err := client.Overview(r.Context())
	if err != nil {
		response.JSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	canEdit := h.App.MihomoConfig != nil && h.App.MihomoConfig.Enabled()
	response.JSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"overview": overviewResponse{Overview: overview, CanEdit: canEdit},
	})
}

func (h *Handler) requireEditor(w http.ResponseWriter) *mihomosvc.ConfigEditor {
	if h.App.MihomoConfig == nil || !h.App.MihomoConfig.Enabled() {
		response.Error(w, http.StatusConflict, mihomosvc.ErrNotConfigured)
		return nil
	}
	return h.App.MihomoConfig
}

type testRequest struct {
	Names     []string `json:"names"`
	TimeoutMS int      `json:"timeoutMs"`
}

func (t testRequest) timeout() time.Duration {
	if t.TimeoutMS <= 0 {
		return defaultTestTimeout
	}
	timeout := time.Duration(t.TimeoutMS) * time.Millisecond
	if timeout > maxTestTimeout {
		return maxTestTimeout
	}
	return timeout
}

func decodeTestRequest(r *http.Request) (testRequest, error) {
	var payload testRequest
	if r.ContentLength == 0 {
		return payload, nil
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		return payload, err
	}
	return payload, nil
}

func contextWithTimeout(r *http.Request, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), timeout)
}
