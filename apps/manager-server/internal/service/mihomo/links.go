package mihomo

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// ErrUnsupportedLink 表示分享链接的协议暂不被支持。
var ErrUnsupportedLink = errors.New("unsupported proxy share link")

// ParseShareLink 把常见的代理分享链接转换成 mihomo 的 proxy 配置。
// 支持 ss / vmess / vless / trojan / hysteria2(hy2) / tuic。
func ParseShareLink(link string) (map[string]any, error) {
	raw := strings.TrimSpace(link)
	if raw == "" {
		return nil, errors.New("empty proxy share link")
	}
	lower := strings.ToLower(raw)
	switch {
	case strings.HasPrefix(lower, "ss://"):
		return parseShadowsocks(raw)
	case strings.HasPrefix(lower, "vmess://"):
		return parseVMess(raw)
	case strings.HasPrefix(lower, "vless://"):
		return parseVLESS(raw)
	case strings.HasPrefix(lower, "trojan://"):
		return parseTrojan(raw)
	case strings.HasPrefix(lower, "hysteria2://"), strings.HasPrefix(lower, "hy2://"):
		return parseHysteria2(raw)
	case strings.HasPrefix(lower, "tuic://"):
		return parseTUIC(raw)
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedLink, schemeOf(raw))
	}
}

// ParseShareLinks 批量解析分享链接，返回解析成功的节点与失败明细。
func ParseShareLinks(links []string) ([]map[string]any, []string) {
	nodes := make([]map[string]any, 0, len(links))
	failures := make([]string, 0)
	for _, link := range links {
		trimmed := strings.TrimSpace(link)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		node, err := ParseShareLink(trimmed)
		if err != nil {
			failures = append(failures, err.Error())
			continue
		}
		nodes = append(nodes, node)
	}
	return nodes, failures
}

func schemeOf(raw string) string {
	if index := strings.Index(raw, "://"); index > 0 {
		return raw[:index+3]
	}
	return "unknown"
}

func fragmentName(raw string) string {
	_, fragment, found := strings.Cut(raw, "#")
	if !found || fragment == "" {
		return ""
	}
	name, err := url.QueryUnescape(fragment)
	if err != nil {
		return fragment
	}
	return strings.TrimSpace(name)
}

func queryBool(values url.Values, key string) bool {
	switch strings.ToLower(values.Get(key)) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

func setIfNotEmpty(node map[string]any, key string, value string) {
	if value = strings.TrimSpace(value); value != "" {
		node[key] = value
	}
}

func fallbackName(node map[string]any, explicit string, index int) string {
	if name := strings.TrimSpace(explicit); name != "" {
		node["name"] = name
		return name
	}
	server := fmt.Sprint(node["server"])
	port := fmt.Sprint(node["port"])
	name := fmt.Sprintf("%s-%s:%s", node["type"], server, port)
	if index > 0 {
		name = fmt.Sprintf("%s #%d", name, index+1)
	}
	node["name"] = name
	return name
}

func applyTransportOptions(node map[string]any, values url.Values) {
	network := strings.ToLower(values.Get("type"))
	if network == "" {
		network = strings.ToLower(values.Get("network"))
	}
	switch network {
	case "", "tcp":
		return
	case "ws":
		node["network"] = "ws"
		options := map[string]any{}
		if path := values.Get("path"); path != "" {
			options["path"] = path
		}
		if host := values.Get("host"); host != "" {
			options["headers"] = map[string]any{"Host": host}
		}
		if len(options) > 0 {
			node["ws-opts"] = options
		}
	case "grpc":
		node["network"] = "grpc"
		serviceName := values.Get("serviceName")
		if serviceName == "" {
			serviceName = values.Get("path")
		}
		if serviceName != "" {
			node["grpc-opts"] = map[string]any{"grpc-service-name": serviceName}
		}
	case "h2", "http":
		node["network"] = "h2"
		options := map[string]any{}
		if path := values.Get("path"); path != "" {
			options["path"] = path
		}
		if host := values.Get("host"); host != "" {
			options["host"] = strings.Split(host, ",")
		}
		if len(options) > 0 {
			node["h2-opts"] = options
		}
	default:
		node["network"] = network
	}
}

func applyTLS(node map[string]any, values url.Values) {
	security := strings.ToLower(values.Get("security"))
	if security == "" && queryBool(values, "tls") {
		security = "tls"
	}
	switch security {
	case "tls", "xtls":
		node["tls"] = true
		setIfNotEmpty(node, "servername", values.Get("sni"))
		setIfNotEmpty(node, "sni", values.Get("sni"))
		if queryBool(values, "allowInsecure") || queryBool(values, "insecure") {
			node["skip-cert-verify"] = true
		}
		if alpn := values.Get("alpn"); alpn != "" {
			node["alpn"] = strings.Split(alpn, ",")
		}
		setIfNotEmpty(node, "client-fingerprint", values.Get("fp"))
	case "reality":
		node["tls"] = true
		setIfNotEmpty(node, "servername", values.Get("sni"))
		reality := map[string]any{}
		setIfNotEmpty(reality, "public-key", values.Get("pbk"))
		setIfNotEmpty(reality, "short-id", values.Get("sid"))
		if len(reality) > 0 {
			node["reality-opts"] = reality
		}
		setIfNotEmpty(node, "client-fingerprint", values.Get("fp"))
	}
}

func parseShadowsocks(raw string) (map[string]any, error) {
	name := fragmentName(raw)
	body := raw[len("ss://"):]
	if hash := strings.Index(body, "#"); hash >= 0 {
		body = body[:hash]
	}

	node := map[string]any{"type": "ss", "udp": true}

	var userInfo, hostPort string
	if at := strings.LastIndex(body, "@"); at >= 0 {
		userInfo = body[:at]
		hostPort = body[at+1:]
		decoded, err := decodeBase64Loose(userInfo)
		if err == nil && strings.Contains(decoded, ":") {
			userInfo = decoded
		} else {
			if unescaped, unescapeErr := url.QueryUnescape(userInfo); unescapeErr == nil {
				userInfo = unescaped
			}
		}
	} else {
		// 纯 base64 的老格式：base64(method:password@host:port)
		decoded, err := decodeBase64Loose(body)
		if err != nil {
			return nil, fmt.Errorf("invalid ss link: %w", err)
		}
		at := strings.LastIndex(decoded, "@")
		if at < 0 {
			return nil, errors.New("invalid ss link: missing credentials")
		}
		userInfo = decoded[:at]
		hostPort = decoded[at+1:]
	}

	query := ""
	if index := strings.Index(hostPort, "?"); index >= 0 {
		query = hostPort[index+1:]
		hostPort = hostPort[:index]
	}
	hostPort = strings.TrimSuffix(hostPort, "/")

	method, password, found := strings.Cut(userInfo, ":")
	if !found || method == "" {
		return nil, errors.New("invalid ss link: missing cipher or password")
	}
	host, port, err := splitHostPort(hostPort)
	if err != nil {
		return nil, err
	}
	node["server"] = host
	node["port"] = port
	node["cipher"] = method
	node["password"] = password

	if query != "" {
		values, err := url.ParseQuery(query)
		if err == nil {
			applySSPlugin(node, values.Get("plugin"))
		}
	}

	fallbackName(node, name, 0)
	return node, nil
}

func applySSPlugin(node map[string]any, plugin string) {
	if plugin == "" {
		return
	}
	parts := strings.Split(plugin, ";")
	switch strings.TrimSpace(parts[0]) {
	case "obfs-local", "simple-obfs", "obfs":
		options := map[string]any{"mode": "http"}
		for _, part := range parts[1:] {
			key, value, found := strings.Cut(part, "=")
			if !found {
				continue
			}
			switch key {
			case "obfs":
				options["mode"] = value
			case "obfs-host":
				options["host"] = value
			}
		}
		node["plugin"] = "obfs"
		node["plugin-opts"] = options
	case "v2ray-plugin":
		options := map[string]any{}
		for _, part := range parts[1:] {
			key, value, found := strings.Cut(part, "=")
			if !found {
				options[part] = true
				continue
			}
			switch key {
			case "host":
				options["host"] = value
			case "path":
				options["path"] = value
			case "tls":
				options["tls"] = true
			case "mux":
				options["mux"] = value != "0"
			}
		}
		if _, ok := options["mode"]; !ok {
			options["mode"] = "websocket"
		}
		node["plugin"] = "v2ray-plugin"
		node["plugin-opts"] = options
	}
}

type vmessPayload struct {
	Version string `json:"v"`
	Name    string `json:"ps"`
	Server  string `json:"add"`
	Port    any    `json:"port"`
	UUID    string `json:"id"`
	Aid     any    `json:"aid"`
	Network string `json:"net"`
	Type    string `json:"type"`
	Host    string `json:"host"`
	Path    string `json:"path"`
	TLS     string `json:"tls"`
	SNI     string `json:"sni"`
	Alpn    string `json:"alpn"`
	Fp      string `json:"fp"`
	Cipher  string `json:"scy"`
}

func parseVMess(raw string) (map[string]any, error) {
	payload := strings.TrimSpace(raw[len("vmess://"):])
	if hash := strings.Index(payload, "#"); hash >= 0 {
		payload = payload[:hash]
	}
	decoded, err := decodeBase64Loose(payload)
	if err != nil {
		return nil, fmt.Errorf("invalid vmess link: %w", err)
	}

	var data vmessPayload
	if err := json.Unmarshal([]byte(decoded), &data); err != nil {
		return nil, fmt.Errorf("invalid vmess payload: %w", err)
	}
	if data.Server == "" || data.UUID == "" {
		return nil, errors.New("invalid vmess payload: missing server or uuid")
	}
	port, err := toPort(data.Port)
	if err != nil {
		return nil, err
	}
	aid, _ := toPort(data.Aid)

	node := map[string]any{
		"type":    "vmess",
		"server":  data.Server,
		"port":    port,
		"uuid":    data.UUID,
		"alterId": aid,
		"cipher":  stringFallbackValue(data.Cipher, "auto"),
		"udp":     true,
	}

	switch strings.ToLower(data.Network) {
	case "", "tcp":
		if data.Type == "http" {
			node["network"] = "http"
			httpOpts := map[string]any{"path": []string{stringFallbackValue(data.Path, "/")}}
			if data.Host != "" {
				httpOpts["headers"] = map[string]any{"Host": strings.Split(data.Host, ",")}
			}
			node["http-opts"] = httpOpts
		}
	case "ws":
		node["network"] = "ws"
		wsOpts := map[string]any{}
		if data.Path != "" {
			wsOpts["path"] = data.Path
		}
		if data.Host != "" {
			wsOpts["headers"] = map[string]any{"Host": data.Host}
		}
		if len(wsOpts) > 0 {
			node["ws-opts"] = wsOpts
		}
	case "grpc":
		node["network"] = "grpc"
		if data.Path != "" {
			node["grpc-opts"] = map[string]any{"grpc-service-name": data.Path}
		}
	case "h2":
		node["network"] = "h2"
		h2Opts := map[string]any{}
		if data.Path != "" {
			h2Opts["path"] = data.Path
		}
		if data.Host != "" {
			h2Opts["host"] = strings.Split(data.Host, ",")
		}
		if len(h2Opts) > 0 {
			node["h2-opts"] = h2Opts
		}
	default:
		node["network"] = strings.ToLower(data.Network)
	}

	if strings.EqualFold(data.TLS, "tls") {
		node["tls"] = true
		setIfNotEmpty(node, "servername", stringFallbackValue(data.SNI, data.Host))
		if data.Alpn != "" {
			node["alpn"] = strings.Split(data.Alpn, ",")
		}
		setIfNotEmpty(node, "client-fingerprint", data.Fp)
	}

	fallbackName(node, data.Name, 0)
	return node, nil
}

func parseVLESS(raw string) (map[string]any, error) {
	name := fragmentName(raw)
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid vless link: %w", err)
	}
	uuid := parsed.User.Username()
	if uuid == "" {
		return nil, errors.New("invalid vless link: missing uuid")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		return nil, fmt.Errorf("invalid vless link: %w", err)
	}

	values := parsed.Query()
	node := map[string]any{
		"type":   "vless",
		"server": parsed.Hostname(),
		"port":   port,
		"uuid":   uuid,
		"udp":    true,
	}
	setIfNotEmpty(node, "flow", values.Get("flow"))
	applyTransportOptions(node, values)
	applyTLS(node, values)

	fallbackName(node, name, 0)
	return node, nil
}

func parseTrojan(raw string) (map[string]any, error) {
	name := fragmentName(raw)
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid trojan link: %w", err)
	}
	password := parsed.User.Username()
	if password == "" {
		return nil, errors.New("invalid trojan link: missing password")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		return nil, fmt.Errorf("invalid trojan link: %w", err)
	}

	values := parsed.Query()
	node := map[string]any{
		"type":     "trojan",
		"server":   parsed.Hostname(),
		"port":     port,
		"password": password,
		"udp":      true,
	}
	setIfNotEmpty(node, "sni", values.Get("sni"))
	if queryBool(values, "allowInsecure") || queryBool(values, "insecure") {
		node["skip-cert-verify"] = true
	}
	if alpn := values.Get("alpn"); alpn != "" {
		node["alpn"] = strings.Split(alpn, ",")
	}
	applyTransportOptions(node, values)

	fallbackName(node, name, 0)
	return node, nil
}

func parseHysteria2(raw string) (map[string]any, error) {
	name := fragmentName(raw)
	normalized := raw
	if strings.HasPrefix(strings.ToLower(raw), "hy2://") {
		normalized = "hysteria2://" + raw[len("hy2://"):]
	}
	parsed, err := url.Parse(normalized)
	if err != nil {
		return nil, fmt.Errorf("invalid hysteria2 link: %w", err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		return nil, fmt.Errorf("invalid hysteria2 link: %w", err)
	}
	password := parsed.User.Username()
	if password == "" {
		password = parsed.User.String()
	}

	values := parsed.Query()
	node := map[string]any{
		"type":     "hysteria2",
		"server":   parsed.Hostname(),
		"port":     port,
		"password": password,
	}
	setIfNotEmpty(node, "sni", values.Get("sni"))
	if queryBool(values, "insecure") || queryBool(values, "allowInsecure") {
		node["skip-cert-verify"] = true
	}
	if obfs := values.Get("obfs"); obfs != "" {
		node["obfs"] = obfs
		setIfNotEmpty(node, "obfs-password", values.Get("obfs-password"))
	}
	setIfNotEmpty(node, "up", values.Get("up"))
	setIfNotEmpty(node, "down", values.Get("down"))
	if alpn := values.Get("alpn"); alpn != "" {
		node["alpn"] = strings.Split(alpn, ",")
	}

	fallbackName(node, name, 0)
	return node, nil
}

func parseTUIC(raw string) (map[string]any, error) {
	name := fragmentName(raw)
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid tuic link: %w", err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		return nil, fmt.Errorf("invalid tuic link: %w", err)
	}
	uuid := parsed.User.Username()
	password, _ := parsed.User.Password()

	values := parsed.Query()
	node := map[string]any{
		"type":   "tuic",
		"server": parsed.Hostname(),
		"port":   port,
		"uuid":   uuid,
		"udp":    true,
	}
	if password != "" {
		node["password"] = password
	}
	setIfNotEmpty(node, "sni", values.Get("sni"))
	if queryBool(values, "allowInsecure") || queryBool(values, "insecure") {
		node["skip-cert-verify"] = true
	}
	if alpn := values.Get("alpn"); alpn != "" {
		node["alpn"] = strings.Split(alpn, ",")
	}
	setIfNotEmpty(node, "congestion-controller", values.Get("congestion_control"))
	setIfNotEmpty(node, "udp-relay-mode", values.Get("udp_relay_mode"))

	fallbackName(node, name, 0)
	return node, nil
}

func splitHostPort(raw string) (string, int, error) {
	host, portText, err := splitLastColon(raw)
	if err != nil {
		return "", 0, err
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return "", 0, fmt.Errorf("invalid port %q", portText)
	}
	return strings.Trim(host, "[]"), port, nil
}

func splitLastColon(raw string) (string, string, error) {
	index := strings.LastIndex(raw, ":")
	if index <= 0 || index == len(raw)-1 {
		return "", "", fmt.Errorf("invalid host:port %q", raw)
	}
	return raw[:index], raw[index+1:], nil
}

func toPort(value any) (int, error) {
	switch typed := value.(type) {
	case float64:
		return int(typed), nil
	case string:
		if typed == "" {
			return 0, nil
		}
		parsed, err := strconv.Atoi(typed)
		if err != nil {
			return 0, fmt.Errorf("invalid port %q", typed)
		}
		return parsed, nil
	case int:
		return typed, nil
	case nil:
		return 0, nil
	default:
		return 0, fmt.Errorf("invalid port %v", value)
	}
}

func decodeBase64Loose(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	for _, encoding := range []*base64.Encoding{
		base64.RawURLEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.StdEncoding,
	} {
		if decoded, err := encoding.DecodeString(trimmed); err == nil {
			return string(decoded), nil
		}
	}
	return "", errors.New("value is not valid base64")
}

func stringFallbackValue(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
