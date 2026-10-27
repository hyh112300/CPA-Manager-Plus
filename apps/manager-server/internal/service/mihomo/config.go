package mihomo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"
)

const (
	// ImportedProviderName 是本地导入节点所使用的 provider 名称。
	ImportedProviderName = "imported"
	providerNamePrefix   = "sub-"
)

// ErrNotConfigured 表示没有配置 mihomo 配置文件路径，无法执行写操作。
var ErrNotConfigured = errors.New("mihomo config file is not configured")

// ConfigOptions 描述配置文件相关路径。
type ConfigOptions struct {
	ConfigPath     string
	ProvidersDir   string
	BinaryPath     string
	Group          string
	TestURL        string
	ExclusionsPath string
	Reloader       *Client
}

// ConfigEditor 负责读写 mihomo 配置文件（provider / 导入节点）并安全重载。
type ConfigEditor struct {
	configPath          string
	providersDir        string
	binaryPath          string
	group               string
	testURL             string
	exclusionsPathValue string
	reloader            *Client
}

// NewConfigEditor 创建配置编辑器。ConfigPath 为空时编辑器处于禁用状态。
func NewConfigEditor(options ConfigOptions) *ConfigEditor {
	configPath := strings.TrimSpace(options.ConfigPath)
	providersDir := strings.TrimSpace(options.ProvidersDir)
	if providersDir == "" && configPath != "" {
		providersDir = filepath.Join(filepath.Dir(configPath), "providers")
	}
	group := strings.TrimSpace(options.Group)
	if group == "" {
		group = "GLOBAL"
	}
	testURL := strings.TrimSpace(options.TestURL)
	if testURL == "" {
		testURL = defaultTestURL
	}
	return &ConfigEditor{
		configPath:          configPath,
		providersDir:        providersDir,
		binaryPath:          strings.TrimSpace(options.BinaryPath),
		group:               group,
		testURL:             testURL,
		exclusionsPathValue: strings.TrimSpace(options.ExclusionsPath),
		reloader:            options.Reloader,
	}
}

// Enabled 表示配置文件写入能力是否可用。
func (e *ConfigEditor) Enabled() bool {
	return e != nil && e.configPath != ""
}

// ProviderConfig 描述配置里的一个 proxy-provider。
type ProviderConfig struct {
	Name            string `json:"name"`
	Type            string `json:"type"`
	URL             string `json:"url,omitempty"`
	Path            string `json:"path,omitempty"`
	IntervalSeconds int    `json:"intervalSeconds,omitempty"`
	Builtin         bool   `json:"builtin"`
}

// ListProviders 读取 config.yaml 里的 proxy-providers。
func (e *ConfigEditor) ListProviders() ([]ProviderConfig, error) {
	document, err := e.load()
	if err != nil {
		return nil, err
	}
	root := documentRoot(document)
	if root == nil {
		return nil, nil
	}
	providersNode := mapValue(root, "proxy-providers")
	if providersNode == nil || providersNode.Kind != yaml.MappingNode {
		return []ProviderConfig{}, nil
	}

	providers := make([]ProviderConfig, 0, len(providersNode.Content)/2)
	for index := 0; index+1 < len(providersNode.Content); index += 2 {
		name := providersNode.Content[index].Value
		spec := providersNode.Content[index+1]
		provider := ProviderConfig{
			Name:    name,
			Type:    scalarValue(spec, "type"),
			URL:     scalarValue(spec, "url"),
			Path:    scalarValue(spec, "path"),
			Builtin: name == ImportedProviderName,
		}
		if interval := scalarValue(spec, "interval"); interval != "" {
			provider.IntervalSeconds = parseIntervalSeconds(interval)
		}
		providers = append(providers, provider)
	}
	sort.Slice(providers, func(i, j int) bool {
		if providers[i].Builtin != providers[j].Builtin {
			return providers[i].Builtin
		}
		return providers[i].Name < providers[j].Name
	})
	return providers, nil
}

// AddSubscription 新增或更新一个订阅型 provider，并确保分组引用它。
func (e *ConfigEditor) AddSubscription(name string, rawURL string, intervalSeconds int) (string, error) {
	if !e.Enabled() {
		return "", ErrNotConfigured
	}
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", errors.New("subscription url is required")
	}
	parsed, err := validateHTTPURL(rawURL)
	if err != nil {
		return "", err
	}

	providerName := sanitizeProviderName(name)
	if providerName == "" {
		providerName = providerNamePrefix + shortHash(rawURL)
	}
	if providerName == ImportedProviderName {
		return "", errors.New("provider name is reserved")
	}
	if intervalSeconds <= 0 {
		intervalSeconds = 3600
	}
	if intervalSeconds < 300 {
		intervalSeconds = 300
	}

	providerPath := filepath.Join("providers", providerName+".yaml")
	err = e.apply(func(root *yaml.Node) error {
		spec := &yaml.Node{Kind: yaml.MappingNode}
		spec.Content = append(spec.Content,
			stringNode("type"), stringNode("http"),
			stringNode("url"), stringNode(parsed),
			stringNode("path"), stringNode(providerPath),
			stringNode("interval"), intNode(intervalSeconds),
		)
		healthCheck := &yaml.Node{Kind: yaml.MappingNode}
		healthCheck.Content = append(healthCheck.Content,
			stringNode("enable"), boolNode(true),
			stringNode("url"), stringNode(e.testURL),
			stringNode("interval"), intNode(300),
		)
		spec.Content = append(spec.Content, stringNode("health-check"), healthCheck)
		setMapValue(ensureMapping(root, "proxy-providers"), providerName, spec)
		return e.ensureProviderUsage(root, providerName)
	})
	if err != nil {
		return "", err
	}
	return providerName, nil
}

// RemoveProvider 删除一个 provider 并同步清理分组引用。
func (e *ConfigEditor) RemoveProvider(name string) error {
	if !e.Enabled() {
		return ErrNotConfigured
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("provider name is required")
	}
	if name == ImportedProviderName {
		return errors.New("the built-in imported provider cannot be removed")
	}

	err := e.apply(func(root *yaml.Node) error {
		providersNode := mapValue(root, "proxy-providers")
		if providersNode == nil || providersNode.Kind != yaml.MappingNode {
			return fmt.Errorf("provider %q not found", name)
		}
		if !deleteMapValue(providersNode, name) {
			return fmt.Errorf("provider %q not found", name)
		}
		e.ensureProviderUsage(root)
		return nil
	})
	if err != nil {
		return err
	}
	// 清理 provider 的缓存文件，避免残留失效订阅。
	e.dropExclusions(name)
	cached := filepath.Join(e.providersDir, name+".yaml")
	if removeErr := os.Remove(cached); removeErr != nil && !os.IsNotExist(removeErr) {
		return fmt.Errorf("remove provider cache: %w", removeErr)
	}
	return nil
}

// RemoveImportedProvider 清空并移除本地导入 provider，
// 下次导入节点时会自动重建。
func (e *ConfigEditor) RemoveImportedProvider() error {
	if !e.Enabled() {
		return ErrNotConfigured
	}

	nodes, err := e.readImportedNodes()
	if err != nil {
		return err
	}
	if len(nodes) > 0 {
		if err := e.writeImportedNodes(nil); err != nil {
			return err
		}
	}

	err = e.apply(func(root *yaml.Node) error {
		if providersNode := mapValue(root, "proxy-providers"); providersNode != nil {
			deleteMapValue(providersNode, ImportedProviderName)
		}
		return e.ensureProviderUsage(root)
	})
	if err != nil {
		return err
	}

	e.dropExclusions(ImportedProviderName)
	if removeErr := os.Remove(e.importedNodesPath()); removeErr != nil && !os.IsNotExist(removeErr) {
		return fmt.Errorf("remove imported nodes file: %w", removeErr)
	}
	return nil
}

// ImportedNodes 返回本地导入 provider 里的节点名。
func (e *ConfigEditor) ImportedNodes() ([]string, error) {
	nodes, err := e.readImportedNodes()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(nodes))
	for _, node := range nodes {
		if name, ok := node["name"].(string); ok {
			names = append(names, name)
		}
	}
	return names, nil
}

// ImportNodes 把节点写入本地导入 provider，返回实际新增的数量与去重后的名称。
func (e *ConfigEditor) ImportNodes(nodes []map[string]any) (int, []string, error) {
	if !e.Enabled() {
		return 0, nil, ErrNotConfigured
	}
	if len(nodes) == 0 {
		return 0, nil, errors.New("no proxy node to import")
	}

	existing, err := e.readImportedNodes()
	if err != nil {
		return 0, nil, err
	}
	index := make(map[string]int, len(existing))
	names := make([]string, 0, len(existing)+len(nodes))
	for _, node := range existing {
		name := fmt.Sprint(node["name"])
		index[name] = len(names)
		names = append(names, name)
	}

	added := 0
	for _, node := range nodes {
		name := strings.TrimSpace(fmt.Sprint(node["name"]))
		if name == "" {
			continue
		}
		if position, exists := index[name]; exists {
			existing[position] = node
			continue
		}
		index[name] = len(existing)
		existing = append(existing, node)
		names = append(names, name)
		added++
	}

	if err := e.writeImportedNodes(existing); err != nil {
		return 0, nil, err
	}
	if err := e.ensureImportedProvider(); err != nil {
		return 0, nil, err
	}
	return added, names, nil
}

// RemoveImportedNode 从本地导入 provider 中删除一个节点。
func (e *ConfigEditor) RemoveImportedNode(name string) error {
	if !e.Enabled() {
		return ErrNotConfigured
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("node name is required")
	}

	nodes, err := e.readImportedNodes()
	if err != nil {
		return err
	}
	filtered := make([]map[string]any, 0, len(nodes))
	found := false
	for _, node := range nodes {
		if fmt.Sprint(node["name"]) == name {
			found = true
			continue
		}
		filtered = append(filtered, node)
	}
	if !found {
		return fmt.Errorf("node %q is not an imported node", name)
	}
	if err := e.writeImportedNodes(filtered); err != nil {
		return err
	}
	return e.reload()
}

// ensureImportedProvider 确保 imported provider 存在并被打组引用。
func (e *ConfigEditor) ensureImportedProvider() error {
	return e.apply(func(root *yaml.Node) error {
		providersNode := ensureMapping(root, "proxy-providers")
		if mapValue(providersNode, ImportedProviderName) == nil {
			spec := &yaml.Node{Kind: yaml.MappingNode}
			spec.Content = append(spec.Content,
				stringNode("type"), stringNode("file"),
				stringNode("path"), stringNode(filepath.Join("providers", ImportedProviderName+".yaml")),
			)
			healthCheck := &yaml.Node{Kind: yaml.MappingNode}
			healthCheck.Content = append(healthCheck.Content,
				stringNode("enable"), boolNode(true),
				stringNode("url"), stringNode(e.testURL),
				stringNode("interval"), intNode(300),
			)
			spec.Content = append(spec.Content, stringNode("health-check"), healthCheck)
			setMapValue(providersNode, ImportedProviderName, spec)
		}
		return e.ensureProviderUsage(root)
	})
}

// ensureProviderUsage 把所有 provider 挂到受管分组（以及分组内嵌的子分组）上。
func (e *ConfigEditor) ensureProviderUsage(root *yaml.Node, extra ...string) error {
	providersNode := mapValue(root, "proxy-providers")
	if providersNode == nil || providersNode.Kind != yaml.MappingNode {
		return errors.New("no proxy provider configured")
	}
	providerNames := make([]string, 0, len(providersNode.Content)/2)
	for index := 0; index+1 < len(providersNode.Content); index += 2 {
		providerNames = append(providerNames, providersNode.Content[index].Value)
	}
	for _, name := range extra {
		if name != "" && !containsString(providerNames, name) {
			providerNames = append(providerNames, name)
		}
	}
	groupsNode := mapValue(root, "proxy-groups")
	if groupsNode == nil || groupsNode.Kind != yaml.SequenceNode {
		return errors.New("no proxy group configured")
	}

	targets := e.targetGroups(groupsNode)
	if len(targets) == 0 {
		return fmt.Errorf("proxy group %q not found", e.group)
	}

	allowed := make(map[string]struct{}, len(providerNames))
	for _, name := range providerNames {
		allowed[name] = struct{}{}
	}

	for _, groupNode := range targets {
		useNode := mapValue(groupNode, "use")
		if useNode == nil || useNode.Kind != yaml.SequenceNode {
			useNode = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
			setMapValue(groupNode, "use", useNode)
		}
		// 先清理已删除 provider 的残留引用，再补齐当前 provider。
		kept := make([]*yaml.Node, 0, len(useNode.Content)+len(providerNames))
		for _, item := range useNode.Content {
			if _, ok := allowed[item.Value]; ok {
				kept = append(kept, item)
			}
		}
		useNode.Content = kept
		for _, name := range providerNames {
			if !sequenceContains(useNode, name) {
				useNode.Content = append(useNode.Content, stringNode(name))
			}
		}
		if len(useNode.Content) == 0 {
			deleteMapValue(groupNode, "use")
		}
	}
	return nil
}

// targetGroups 返回需要挂载 provider 的分组节点：受管分组本身 + 它直接引用的子分组。
func (e *ConfigEditor) targetGroups(groupsNode *yaml.Node) []*yaml.Node {
	byName := make(map[string]*yaml.Node, len(groupsNode.Content))
	order := make([]string, 0, len(groupsNode.Content))
	for _, groupNode := range groupsNode.Content {
		if groupNode.Kind != yaml.MappingNode {
			continue
		}
		name := scalarValue(groupNode, "name")
		if name == "" {
			continue
		}
		byName[name] = groupNode
		order = append(order, name)
	}

	main, exists := byName[e.group]
	if !exists {
		return nil
	}

	targets := []*yaml.Node{main}
	proxiesNode := mapValue(main, "proxies")
	if proxiesNode != nil && proxiesNode.Kind == yaml.SequenceNode {
		for _, item := range proxiesNode.Content {
			if child, ok := byName[item.Value]; ok && child != main {
				targets = append(targets, child)
			}
		}
	}
	return targets
}

func (e *ConfigEditor) apply(mutate func(root *yaml.Node) error) error {
	if !e.Enabled() {
		return ErrNotConfigured
	}
	document, err := e.load()
	if err != nil {
		return err
	}
	root := documentRoot(document)
	if root == nil {
		return errors.New("mihomo config is empty")
	}
	if err := mutate(root); err != nil {
		return err
	}
	if err := e.save(document); err != nil {
		return err
	}
	return e.reload()
}

func (e *ConfigEditor) load() (*yaml.Node, error) {
	data, err := os.ReadFile(e.configPath)
	if err != nil {
		return nil, fmt.Errorf("read mihomo config: %w", err)
	}
	document := &yaml.Node{}
	if err := yaml.Unmarshal(data, document); err != nil {
		return nil, fmt.Errorf("parse mihomo config: %w", err)
	}
	return document, nil
}

// save 校验新配置后原子替换，并保留一份时间戳备份。
func (e *ConfigEditor) save(document *yaml.Node) error {
	encoded, err := encodeYAML(document)
	if err != nil {
		return err
	}

	directory := filepath.Dir(e.configPath)
	temporary, err := os.CreateTemp(directory, ".config-*.yaml")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()

	if _, err := temporary.Write(encoded); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary config: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary config: %w", err)
	}
	if err := os.Chmod(temporaryPath, 0o644); err != nil {
		return fmt.Errorf("chmod temporary config: %w", err)
	}

	if err := e.validate(temporaryPath); err != nil {
		return err
	}

	backup := e.configPath + ".bak_" + time.Now().Format("20060102_150405")
	if err := copyFile(e.configPath, backup); err != nil {
		return fmt.Errorf("backup mihomo config: %w", err)
	}
	if err := os.Rename(temporaryPath, e.configPath); err != nil {
		return fmt.Errorf("replace mihomo config: %w", err)
	}
	return nil
}

func (e *ConfigEditor) validate(path string) error {
	if e.binaryPath == "" {
		return nil
	}
	command := exec.Command(e.binaryPath, "-t", "-d", filepath.Dir(e.configPath), "-f", path)
	output, err := command.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("mihomo config validation failed: %s", message)
	}
	return nil
}

func (e *ConfigEditor) reload() error {
	if e.reloader == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return e.reloader.ReloadConfig(ctx, e.configPath)
}

func (e *ConfigEditor) importedNodesPath() string {
	return filepath.Join(e.providersDir, ImportedProviderName+".yaml")
}

func (e *ConfigEditor) readImportedNodes() ([]map[string]any, error) {
	data, err := os.ReadFile(e.importedNodesPath())
	if err != nil {
		if os.IsNotExist(err) {
			return []map[string]any{}, nil
		}
		return nil, fmt.Errorf("read imported nodes: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return []map[string]any{}, nil
	}

	var payload struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("parse imported nodes: %w", err)
	}
	return payload.Proxies, nil
}

func (e *ConfigEditor) writeImportedNodes(nodes []map[string]any) error {
	if err := os.MkdirAll(e.providersDir, 0o755); err != nil {
		return fmt.Errorf("create providers directory: %w", err)
	}
	if nodes == nil {
		nodes = []map[string]any{}
	}
	var builder strings.Builder
	encoder := yaml.NewEncoder(&builder)
	encoder.SetIndent(2)
	if err := encoder.Encode(map[string]any{"proxies": nodes}); err != nil {
		return fmt.Errorf("encode imported nodes: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return fmt.Errorf("encode imported nodes: %w", err)
	}
	encoded := unescapeUnicodeEscapes([]byte(builder.String()))
	path := e.importedNodesPath()
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, encoded, 0o644); err != nil {
		return fmt.Errorf("write imported nodes: %w", err)
	}
	return os.Rename(temporary, path)
}

func documentRoot(document *yaml.Node) *yaml.Node {
	if document == nil {
		return nil
	}
	if document.Kind == yaml.DocumentNode && len(document.Content) > 0 {
		return document.Content[0]
	}
	if document.Kind == yaml.MappingNode {
		return document
	}
	return nil
}

func mapValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index+1 < len(node.Content); index += 2 {
		if node.Content[index].Value == key {
			return node.Content[index+1]
		}
	}
	return nil
}

func setMapValue(node *yaml.Node, key string, value *yaml.Node) {
	if node == nil {
		return
	}
	for index := 0; index+1 < len(node.Content); index += 2 {
		if node.Content[index].Value == key {
			node.Content[index+1] = value
			return
		}
	}
	node.Content = append(node.Content, stringNode(key), value)
}

func deleteMapValue(node *yaml.Node, key string) bool {
	if node == nil || node.Kind != yaml.MappingNode {
		return false
	}
	for index := 0; index+1 < len(node.Content); index += 2 {
		if node.Content[index].Value == key {
			node.Content = append(node.Content[:index], node.Content[index+2:]...)
			return true
		}
	}
	return false
}

func ensureMapping(node *yaml.Node, key string) *yaml.Node {
	if existing := mapValue(node, key); existing != nil && existing.Kind == yaml.MappingNode {
		return existing
	}
	created := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	setMapValue(node, key, created)
	return created
}

func scalarValue(node *yaml.Node, key string) string {
	value := mapValue(node, key)
	if value == nil {
		return ""
	}
	return strings.TrimSpace(value.Value)
}

func sequenceContains(node *yaml.Node, value string) bool {
	if node == nil || node.Kind != yaml.SequenceNode {
		return false
	}
	for _, item := range node.Content {
		if item.Value == value {
			return true
		}
	}
	return false
}

func stringNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

func intNode(value int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(value)}
}

func boolNode(value bool) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(value)}
}

func encodeYAML(document *yaml.Node) ([]byte, error) {
	var builder strings.Builder
	encoder := yaml.NewEncoder(&builder)
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		return nil, fmt.Errorf("encode mihomo config: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("encode mihomo config: %w", err)
	}
	return unescapeUnicodeEscapes([]byte(builder.String())), nil
}

// unescapeUnicodeEscapes 把 yaml.v3 输出的 \U0001F1F9 形式转回真实字符，
// 避免节点名里的 emoji 国旗在配置里变成转义序列。
var unicodeEscapePattern = regexp.MustCompile(`\\[Uu]([0-9A-Fa-f]{4,8})`)

func unescapeUnicodeEscapes(data []byte) []byte {
	return unicodeEscapePattern.ReplaceAllFunc(data, func(match []byte) []byte {
		hex := string(match[2:])
		code, err := strconv.ParseUint(hex, 16, 32)
		if err != nil || code > 0x10FFFF {
			return match
		}
		return []byte(string(rune(code)))
	})
}

func copyFile(source string, target string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return os.WriteFile(target, data, 0o644)
}

func sanitizeProviderName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var builder strings.Builder
	for _, char := range name {
		switch {
		case unicode.IsLetter(char), unicode.IsDigit(char), char == '-', char == '_':
			builder.WriteRune(char)
		case char == ' ':
			builder.WriteRune('-')
		}
	}
	runes := []rune(strings.Trim(builder.String(), "-_"))
	if len(runes) > 40 {
		runes = runes[:40]
	}
	return string(runes)
}

func shortHash(value string) string {
	var hash uint32 = 2166136261
	for index := 0; index < len(value); index++ {
		hash ^= uint32(value[index])
		hash *= 16777619
	}
	return strconv.FormatUint(uint64(hash), 16)
}

func containsString(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func parseIntervalSeconds(value string) int {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(trimmed); err == nil {
		return seconds
	}
	if duration, err := time.ParseDuration(trimmed); err == nil {
		return int(duration.Seconds())
	}
	return 0
}

// ExtractNodesFromYAML 从一段 Clash / mihomo 配置或纯节点列表里提取 proxies。
func ExtractNodesFromYAML(data []byte) ([]map[string]any, error) {
	var document struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal(data, &document); err == nil && len(document.Proxies) > 0 {
		return document.Proxies, nil
	}

	var list []map[string]any
	if err := yaml.Unmarshal(data, &list); err == nil && len(list) > 0 {
		return list, nil
	}

	return nil, errors.New("no proxies found in the provided yaml")
}

// WaitForNodes 轮询等待指定节点出现在分组里（mihomo reload 后 provider 是异步挂载的）。
func (c *Client) WaitForNodes(ctx context.Context, names []string, timeout time.Duration) {
	if !c.Enabled() || len(names) == 0 {
		return
	}
	deadline := time.Now().Add(timeout)
	for {
		overview, err := c.Overview(ctx)
		if err == nil {
			present := make(map[string]struct{}, len(overview.Nodes))
			for _, node := range overview.Nodes {
				present[node.Name] = struct{}{}
			}
			missing := false
			for _, name := range names {
				if _, ok := present[name]; !ok {
					missing = true
					break
				}
			}
			if !missing {
				return
			}
		}
		if time.Now().After(deadline) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(150 * time.Millisecond):
		}
	}
}

func validateHTTPURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	lower := strings.ToLower(trimmed)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return "", errors.New("subscription url must start with http:// or https://")
	}
	return trimmed, nil
}
