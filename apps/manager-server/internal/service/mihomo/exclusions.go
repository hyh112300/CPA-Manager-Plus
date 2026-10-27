package mihomo

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// exclusionEntry 记录某个 provider 被屏蔽的节点。
// Base 保存第一次写入前 provider 上已有的 exclude-filter，避免覆盖手工配置。
type exclusionEntry struct {
	Base  string   `json:"base,omitempty"`
	Names []string `json:"names"`
}

type exclusionFile struct {
	Providers map[string]exclusionEntry `json:"providers"`
}

// Exclusions 返回 provider -> 被屏蔽节点名 的映射。
func (e *ConfigEditor) Exclusions() (map[string][]string, error) {
	file, err := e.readExclusions()
	if err != nil {
		return nil, err
	}
	result := make(map[string][]string, len(file.Providers))
	for provider, entry := range file.Providers {
		if len(entry.Names) == 0 {
			continue
		}
		names := append([]string(nil), entry.Names...)
		sort.Strings(names)
		result[provider] = names
	}
	return result, nil
}

// HideProviderNode 通过 provider 的 exclude-filter 持久屏蔽一个订阅节点，
// 订阅后续更新也不会把它带回来。
func (e *ConfigEditor) HideProviderNode(provider string, name string) error {
	if !e.Enabled() {
		return ErrNotConfigured
	}
	provider = strings.TrimSpace(provider)
	name = strings.TrimSpace(name)
	if provider == "" || name == "" {
		return errors.New("provider and node name are required")
	}
	if provider == ImportedProviderName {
		return e.RemoveImportedNode(name)
	}

	file, err := e.readExclusions()
	if err != nil {
		return err
	}
	entry, exists := file.Providers[provider]
	if !exists {
		base, baseErr := e.providerExcludeFilter(provider)
		if baseErr != nil {
			return baseErr
		}
		entry = exclusionEntry{Base: base, Names: []string{}}
	}
	for _, existing := range entry.Names {
		if existing == name {
			return e.applyProviderExclusion(provider, entry)
		}
	}
	entry.Names = append(entry.Names, name)
	file.Providers[provider] = entry

	if err := e.applyProviderExclusion(provider, entry); err != nil {
		return err
	}
	return e.writeExclusions(file)
}

// RestoreProviderNodes 取消某个 provider 上的全部屏蔽。
func (e *ConfigEditor) RestoreProviderNodes(provider string) error {
	if !e.Enabled() {
		return ErrNotConfigured
	}
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return errors.New("provider name is required")
	}

	file, err := e.readExclusions()
	if err != nil {
		return err
	}
	entry, exists := file.Providers[provider]
	if !exists || len(entry.Names) == 0 {
		return nil
	}
	entry.Names = []string{}
	if err := e.applyProviderExclusion(provider, entry); err != nil {
		return err
	}
	delete(file.Providers, provider)
	return e.writeExclusions(file)
}

// dropExclusions 在 provider 被删除后清理它的屏蔽记录（best effort）。
func (e *ConfigEditor) dropExclusions(provider string) {
	file, err := e.readExclusions()
	if err != nil {
		return
	}
	if _, exists := file.Providers[provider]; !exists {
		return
	}
	delete(file.Providers, provider)
	_ = e.writeExclusions(file)
}

func (e *ConfigEditor) applyProviderExclusion(provider string, entry exclusionEntry) error {
	return e.apply(func(root *yaml.Node) error {
		providersNode := mapValue(root, "proxy-providers")
		if providersNode == nil || providersNode.Kind != yaml.MappingNode {
			return fmt.Errorf("provider %q not found", provider)
		}
		spec := mapValue(providersNode, provider)
		if spec == nil || spec.Kind != yaml.MappingNode {
			return fmt.Errorf("provider %q not found", provider)
		}
		filter := composeExcludeFilter(entry.Base, entry.Names)
		if filter == "" {
			deleteMapValue(spec, "exclude-filter")
			return nil
		}
		setMapValue(spec, "exclude-filter", stringNode(filter))
		return nil
	})
}

func (e *ConfigEditor) providerExcludeFilter(provider string) (string, error) {
	document, err := e.load()
	if err != nil {
		return "", err
	}
	root := documentRoot(document)
	if root == nil {
		return "", nil
	}
	providersNode := mapValue(root, "proxy-providers")
	if providersNode == nil {
		return "", fmt.Errorf("provider %q not found", provider)
	}
	spec := mapValue(providersNode, provider)
	if spec == nil {
		return "", fmt.Errorf("provider %q not found", provider)
	}
	return scalarValue(spec, "exclude-filter"), nil
}

// composeExcludeFilter 把基础 filter 与屏蔽节点名组合成一个正则。
func composeExcludeFilter(base string, names []string) string {
	parts := make([]string, 0, 2)
	if trimmed := strings.TrimSpace(base); trimmed != "" {
		parts = append(parts, trimmed)
	}
	quoted := make([]string, 0, len(names))
	for _, name := range names {
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			quoted = append(quoted, regexp.QuoteMeta(trimmed))
		}
	}
	if len(quoted) > 0 {
		parts = append(parts, "^(?:"+strings.Join(quoted, "|")+")$")
	}
	return strings.Join(parts, "|")
}

func (e *ConfigEditor) exclusionsPath() string {
	if strings.TrimSpace(e.exclusionsPathValue) != "" {
		return e.exclusionsPathValue
	}
	base := strings.TrimSpace(e.configPath)
	if base == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(base), "proxy-node-exclusions.json")
}

func (e *ConfigEditor) readExclusions() (exclusionFile, error) {
	file := exclusionFile{Providers: map[string]exclusionEntry{}}
	path := e.exclusionsPath()
	if path == "" {
		return file, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return file, nil
		}
		return file, fmt.Errorf("read node exclusions: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return file, nil
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return file, fmt.Errorf("parse node exclusions: %w", err)
	}
	if file.Providers == nil {
		file.Providers = map[string]exclusionEntry{}
	}
	return file, nil
}

func (e *ConfigEditor) writeExclusions(file exclusionFile) error {
	path := e.exclusionsPath()
	if path == "" {
		return nil
	}
	encoded, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("encode node exclusions: %w", err)
	}
	encoded = append(encoded, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create exclusions directory: %w", err)
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, encoded, 0o600); err != nil {
		return fmt.Errorf("write node exclusions: %w", err)
	}
	return os.Rename(temporary, path)
}
