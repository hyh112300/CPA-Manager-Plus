package mihomo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const testConfig = `mixed-port: 7890
external-controller: 127.0.0.1:9090
proxies:
- name: 🇹🇼TW-台湾1
  type: ss
  server: cm1.example.org
  port: 45379
  cipher: chacha20-ietf-poly1305
  password: pass
proxy-groups:
- name: GLOBAL
  type: select
  proxies:
  - AUTO
  - 🇹🇼TW-台湾1
- name: AUTO
  type: fallback
  proxies:
  - 🇹🇼TW-台湾1
  url: http://www.gstatic.com/generate_204
  interval: 300
rules:
- MATCH,GLOBAL
`

func newTestEditor(t *testing.T) *ConfigEditor {
	t.Helper()
	directory := t.TempDir()
	path := filepath.Join(directory, "config.yaml")
	if err := os.WriteFile(path, []byte(testConfig), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return NewConfigEditor(ConfigOptions{
		ConfigPath:   path,
		ProvidersDir: filepath.Join(directory, "providers"),
		Group:        "GLOBAL",
	})
}

func readConfigMap(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var payload map[string]any
	if err := yaml.Unmarshal(data, &payload); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	return payload
}

func groupByName(t *testing.T, config map[string]any, name string) map[string]any {
	t.Helper()
	groups, ok := config["proxy-groups"].([]any)
	if !ok {
		t.Fatalf("proxy-groups missing")
	}
	for _, item := range groups {
		group, ok := item.(map[string]any)
		if !ok || group["name"] != name {
			continue
		}
		return group
	}
	t.Fatalf("group %q not found", name)
	return nil
}

func useList(t *testing.T, group map[string]any) []string {
	t.Helper()
	raw, ok := group["use"].([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(raw))
	for _, item := range raw {
		result = append(result, item.(string))
	}
	return result
}

func TestImportNodesCreatesProviderAndGroupUsage(t *testing.T) {
	editor := newTestEditor(t)

	added, names, err := editor.ImportNodes([]map[string]any{
		{"name": "imported-1", "type": "ss", "server": "1.2.3.4", "port": 443, "cipher": "aes-256-gcm", "password": "x"},
	})
	if err != nil {
		t.Fatalf("import nodes: %v", err)
	}
	if added != 1 || len(names) != 1 {
		t.Fatalf("unexpected import result: added=%d names=%v", added, names)
	}

	nodes, err := editor.ImportedNodes()
	if err != nil {
		t.Fatalf("read imported nodes: %v", err)
	}
	if len(nodes) != 1 || nodes[0] != "imported-1" {
		t.Fatalf("unexpected imported nodes: %v", nodes)
	}

	config := readConfigMap(t, editor.configPath)
	providers, ok := config["proxy-providers"].(map[string]any)
	if !ok {
		t.Fatalf("proxy-providers missing: %v", config["proxy-providers"])
	}
	if _, exists := providers[ImportedProviderName]; !exists {
		t.Fatalf("imported provider not created")
	}

	for _, name := range []string{"GLOBAL", "AUTO"} {
		group := groupByName(t, config, name)
		if !containsString(useList(t, group), ImportedProviderName) {
			t.Fatalf("group %s does not reference imported provider: %v", name, group["use"])
		}
	}
}

func TestImportNodesReplacesSameName(t *testing.T) {
	editor := newTestEditor(t)

	if _, _, err := editor.ImportNodes([]map[string]any{
		{"name": "dup", "type": "ss", "server": "1.1.1.1", "port": 1, "cipher": "aes-256-gcm", "password": "a"},
	}); err != nil {
		t.Fatalf("first import: %v", err)
	}
	added, names, err := editor.ImportNodes([]map[string]any{
		{"name": "dup", "type": "ss", "server": "2.2.2.2", "port": 2, "cipher": "aes-256-gcm", "password": "b"},
	})
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if added != 0 || len(names) != 1 {
		t.Fatalf("unexpected dedupe result: added=%d names=%v", added, names)
	}

	data, err := os.ReadFile(editor.importedNodesPath())
	if err != nil {
		t.Fatalf("read provider file: %v", err)
	}
	if !strings.Contains(string(data), "2.2.2.2") || strings.Contains(string(data), "1.1.1.1") {
		t.Fatalf("node was not replaced: %s", data)
	}
}

func TestRemoveImportedNode(t *testing.T) {
	editor := newTestEditor(t)
	if _, _, err := editor.ImportNodes([]map[string]any{
		{"name": "keep", "type": "ss", "server": "1.1.1.1", "port": 1, "cipher": "aes-256-gcm", "password": "a"},
		{"name": "drop", "type": "ss", "server": "2.2.2.2", "port": 2, "cipher": "aes-256-gcm", "password": "b"},
	}); err != nil {
		t.Fatalf("import nodes: %v", err)
	}
	if err := editor.RemoveImportedNode("drop"); err != nil {
		t.Fatalf("remove node: %v", err)
	}
	nodes, err := editor.ImportedNodes()
	if err != nil {
		t.Fatalf("read imported nodes: %v", err)
	}
	if len(nodes) != 1 || nodes[0] != "keep" {
		t.Fatalf("unexpected nodes after removal: %v", nodes)
	}
	if err := editor.RemoveImportedNode("missing"); err == nil {
		t.Fatal("expected error removing unknown node")
	}
}

func TestAddAndRemoveSubscription(t *testing.T) {
	editor := newTestEditor(t)

	name, err := editor.AddSubscription("My Sub", "https://example.com/sub?token=1", 1800)
	if err != nil {
		t.Fatalf("add subscription: %v", err)
	}
	if name != "my-sub" {
		t.Fatalf("unexpected provider name: %s", name)
	}

	providers, err := editor.ListProviders()
	if err != nil {
		t.Fatalf("list providers: %v", err)
	}
	if len(providers) != 1 || providers[0].URL != "https://example.com/sub?token=1" || providers[0].IntervalSeconds != 1800 {
		t.Fatalf("unexpected providers: %#v", providers)
	}

	rawConfig, err := os.ReadFile(editor.configPath)
	if err != nil {
		t.Fatalf("read raw config: %v", err)
	}
	if !strings.Contains(string(rawConfig), "🇹🇼TW-台湾1") {
		t.Fatalf("emoji node name was escaped: %s", rawConfig)
	}

	config := readConfigMap(t, editor.configPath)
	group := groupByName(t, config, "GLOBAL")
	if !containsString(useList(t, group), name) {
		t.Fatalf("group does not reference subscription provider: %v", group["use"])
	}

	cachedPath := filepath.Join(editor.providersDir, name+".yaml")
	if err := os.MkdirAll(editor.providersDir, 0o755); err != nil {
		t.Fatalf("create providers dir: %v", err)
	}
	if err := os.WriteFile(cachedPath, []byte("proxies: []\n"), 0o644); err != nil {
		t.Fatalf("write provider cache: %v", err)
	}

	if err := editor.RemoveProvider(name); err != nil {
		t.Fatalf("remove provider: %v", err)
	}
	if _, statErr := os.Stat(cachedPath); !os.IsNotExist(statErr) {
		t.Fatalf("provider cache file was not removed")
	}
	config = readConfigMap(t, editor.configPath)
	group = groupByName(t, config, "GLOBAL")
	if containsString(useList(t, group), name) {
		t.Fatalf("provider reference not cleaned up: %v", group["use"])
	}
}

func TestAddSubscriptionRejectsInvalidURL(t *testing.T) {
	editor := newTestEditor(t)
	if _, err := editor.AddSubscription("bad", "file:///etc/passwd", 0); err == nil {
		t.Fatal("expected invalid url error")
	}
}

func TestExtractNodesFromYAML(t *testing.T) {
	nodes, err := ExtractNodesFromYAML([]byte("proxies:\n- name: a\n  type: ss\n- name: b\n  type: trojan\n"))
	if err != nil {
		t.Fatalf("extract nodes: %v", err)
	}
	if len(nodes) != 2 || nodes[0]["name"] != "a" {
		t.Fatalf("unexpected nodes: %#v", nodes)
	}

	list, err := ExtractNodesFromYAML([]byte("- name: c\n  type: ss\n"))
	if err != nil || len(list) != 1 {
		t.Fatalf("unexpected list parse: %#v %v", list, err)
	}

	if _, err := ExtractNodesFromYAML([]byte("nothing: here\n")); err == nil {
		t.Fatal("expected error for yaml without proxies")
	}
}
