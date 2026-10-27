package mihomo

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestParseShadowsocksSIP002(t *testing.T) {
	node, err := ParseShareLink("ss://YWVzLTI1Ni1nY206cGFzc3dvcmQ=@1.2.3.4:8388#HK-01")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if node["type"] != "ss" || node["server"] != "1.2.3.4" || node["port"] != 8388 {
		t.Fatalf("unexpected node: %#v", node)
	}
	if node["cipher"] != "aes-256-gcm" || node["password"] != "password" {
		t.Fatalf("unexpected credentials: %#v", node)
	}
	if node["name"] != "HK-01" {
		t.Fatalf("unexpected name: %v", node["name"])
	}
}

func TestParseShadowsocksLegacy(t *testing.T) {
	payload := base64.StdEncoding.EncodeToString([]byte("chacha20-ietf-poly1305:secret@5.6.7.8:443"))
	node, err := ParseShareLink("ss://" + payload)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if node["server"] != "5.6.7.8" || node["port"] != 443 || node["cipher"] != "chacha20-ietf-poly1305" {
		t.Fatalf("unexpected node: %#v", node)
	}
	if node["name"] != "ss-5.6.7.8:443" {
		t.Fatalf("unexpected fallback name: %v", node["name"])
	}
}

func TestParseVMess(t *testing.T) {
	payload, err := json.Marshal(map[string]any{
		"v": "2", "ps": "JP-01", "add": "jp.example.com", "port": "443",
		"id": "b831381d-6324-4d53-ad4f-8cda48b30811", "aid": "0",
		"net": "ws", "host": "cdn.example.com", "path": "/ray",
		"tls": "tls", "sni": "cdn.example.com",
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	link := "vmess://" + base64.StdEncoding.EncodeToString(payload)

	node, err := ParseShareLink(link)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if node["type"] != "vmess" || node["server"] != "jp.example.com" || node["port"] != 443 {
		t.Fatalf("unexpected node: %#v", node)
	}
	if node["tls"] != true || node["network"] != "ws" {
		t.Fatalf("unexpected transport: %#v", node)
	}
	wsOpts, ok := node["ws-opts"].(map[string]any)
	if !ok || wsOpts["path"] != "/ray" {
		t.Fatalf("unexpected ws opts: %#v", node["ws-opts"])
	}
	if node["name"] != "JP-01" {
		t.Fatalf("unexpected name: %v", node["name"])
	}
}

func TestParseVLESSReality(t *testing.T) {
	link := "vless://11111111-2222-3333-4444-555555555555@example.com:8443" +
		"?encryption=none&security=reality&sni=www.microsoft.com&pbk=pubkey&sid=abcd&type=grpc&serviceName=grpcsvc#US-Reality"

	node, err := ParseShareLink(link)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if node["type"] != "vless" || node["port"] != 8443 {
		t.Fatalf("unexpected node: %#v", node)
	}
	if node["tls"] != true || node["servername"] != "www.microsoft.com" {
		t.Fatalf("unexpected tls opts: %#v", node)
	}
	reality, ok := node["reality-opts"].(map[string]any)
	if !ok || reality["public-key"] != "pubkey" || reality["short-id"] != "abcd" {
		t.Fatalf("unexpected reality opts: %#v", node["reality-opts"])
	}
	grpc, ok := node["grpc-opts"].(map[string]any)
	if !ok || grpc["grpc-service-name"] != "grpcsvc" {
		t.Fatalf("unexpected grpc opts: %#v", node["grpc-opts"])
	}
	if node["name"] != "US-Reality" {
		t.Fatalf("unexpected name: %v", node["name"])
	}
}

func TestParseTrojanAndHysteria2(t *testing.T) {
	trojan, err := ParseShareLink("trojan://pa%24%24word@example.com:443?sni=example.com&allowInsecure=1#Trojan-01")
	if err != nil {
		t.Fatalf("trojan parse failed: %v", err)
	}
	if trojan["password"] != "pa$$word" || trojan["skip-cert-verify"] != true {
		t.Fatalf("unexpected trojan node: %#v", trojan)
	}

	hy2, err := ParseShareLink("hysteria2://authpass@example.com:8443?sni=example.com&obfs=salamander&obfs-password=x#HY2")
	if err != nil {
		t.Fatalf("hysteria2 parse failed: %v", err)
	}
	if hy2["type"] != "hysteria2" || hy2["password"] != "authpass" {
		t.Fatalf("unexpected hysteria2 node: %#v", hy2)
	}
	if hy2["obfs"] != "salamander" || hy2["obfs-password"] != "x" {
		t.Fatalf("unexpected obfs opts: %#v", hy2)
	}
}

func TestParseShareLinksCollectsFailures(t *testing.T) {
	nodes, failures := ParseShareLinks([]string{
		"ss://YWVzLTI1Ni1nY206cGFzc3dvcmQ=@1.2.3.4:8388#ok",
		"",
		"ftp://unsupported",
	})
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(nodes))
	}
	if len(failures) != 1 {
		t.Fatalf("expected 1 failure, got %d", len(failures))
	}
}
