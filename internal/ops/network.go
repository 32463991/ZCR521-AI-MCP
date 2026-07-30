package ops

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func (m *Manager) networkOperation(ctx context.Context, req Request) Result {
	action, err := actionOf(req, "interfaces")
	if err != nil {
		return invalid(err.Error())
	}
	switch action {
	case "interfaces", "ip":
		interfaces, scanErr := networkInterfaces()
		if scanErr != nil {
			return fail("NETWORK_ERROR", "网络接口读取失败", scanErr, "net_interfaces")
		}
		return ok("网络接口读取成功", interfaces, "net_interfaces")
	case "dns_lookup", "resolve":
		host, parseErr := argString(req.Args, "host", "name")
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		resolver := net.DefaultResolver
		addresses, lookupErr := resolver.LookupHost(ctx, host)
		if lookupErr != nil {
			return fail("NETWORK_ERROR", "域名解析失败", lookupErr, "net_resolver")
		}
		return ok("域名解析成功", map[string]any{"host": host, "addresses": addresses}, "net_resolver")
	case "port_check", "tcp_check", "lan_test":
		host, parseErr := argString(req.Args, "host")
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		port, parseErr := argInt64(req.Args, 0, "port")
		if parseErr != nil || port <= 0 || port > 65535 {
			return invalid("port 必须在 1 到 65535 之间")
		}
		timeout, parseErr := argDuration(req.Args, 5*time.Second)
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		started := time.Now()
		dialer := net.Dialer{Timeout: timeout}
		connection, dialErr := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.FormatInt(port, 10)))
		if dialErr != nil {
			return fail("NETWORK_ERROR", "TCP 端口连接失败", dialErr, "tcp_dial")
		}
		_ = connection.Close()
		return ok("TCP 端口可连接", map[string]any{"host": host, "port": port, "latencyMs": time.Since(started).Milliseconds()}, "tcp_dial")
	case "http", "http_request", "internet_test":
		if action == "internet_test" {
			if _, exists := req.Args["url"]; !exists {
				req.Args["url"] = "https://connectivitycheck.gstatic.com/generate_204"
			}
		}
		return httpRequest(ctx, req.Args)
	case "ping":
		host, parseErr := argString(req.Args, "host")
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		count, parseErr := argInt64(req.Args, 4, "count")
		if parseErr != nil || count <= 0 || count > 20 {
			return invalid("count 必须在 1 到 20 之间")
		}
		if _, lookErr := exec.LookPath("ping"); lookErr != nil {
			return unavailable("ping")
		}
		timeout, _ := argDuration(req.Args, 30*time.Second)
		args := []string{"-c", strconv.FormatInt(count, 10), host}
		result := m.runCommand(ctx, commandSpec{Name: "ping", Args: args, Dir: m.cfg.WorkDir, Timeout: timeout, Strategy: "ping_command"})
		return result
	case "routes", "gateway":
		routes, routeErr := readRoutes()
		if routeErr != nil {
			return fail("COMMAND_UNAVAILABLE", "路由表读取失败", routeErr, "proc_net_route")
		}
		if action == "gateway" {
			gateways := make([]map[string]any, 0)
			for _, route := range routes {
				if route["destination"] == "0.0.0.0" {
					gateways = append(gateways, route)
				}
			}
			return ok("默认网关读取成功", gateways, "proc_net_route")
		}
		return ok("路由表读取成功", routes, "proc_net_route")
	case "dns":
		return ok("DNS 配置读取成功", readDNSConfiguration(), "resolv_conf_getprop")
	case "connections":
		result := runFirstAvailable(ctx, m, []commandVariant{
			{Name: "ss", Args: []string{"-tunap"}, Strategy: "ss"},
			{Name: "netstat", Args: []string{"-tunap"}, Strategy: "netstat"},
		}, 30*time.Second)
		return result
	case "wifi_info":
		result := runFirstAvailable(ctx, m, []commandVariant{
			{Name: "cmd", Args: []string{"wifi", "status"}, Strategy: "cmd_wifi"},
			{Name: "dumpsys", Args: []string{"wifi"}, Strategy: "dumpsys_wifi"},
		}, 30*time.Second)
		return result
	case "proxy":
		mode, parseErr := argOptionalString(req.Args, "get", "mode")
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		action := ""
		switch normalizeTool(mode) {
		case "get", "status":
			action = "proxy_get"
		case "set":
			action = "proxy_set"
		case "clear", "disable", "off":
			action = "proxy_clear"
		default:
			return invalid("proxy mode 必须是 get、set 或 clear")
		}
		child := copyArgs(req.Args)
		child["action"] = action
		return m.connectivityOperation(ctx, Request{Tool: "zcr521_connectivity", Args: child})
	default:
		return invalidAction(req.Tool, action, "connections", "dns", "dns_lookup", "gateway", "http", "http_request", "interfaces", "internet_test", "ip", "lan_test", "ping", "port_check", "proxy", "resolve", "routes", "tcp_check", "wifi_info")
	}
}

func networkInterfaces() ([]map[string]any, error) {
	items, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		addresses, addressErr := item.Addrs()
		if addressErr != nil {
			continue
		}
		values := make([]string, 0, len(addresses))
		for _, address := range addresses {
			values = append(values, address.String())
		}
		out = append(out, map[string]any{
			"index":           item.Index,
			"name":            item.Name,
			"hardwareAddress": item.HardwareAddr.String(),
			"mtu":             item.MTU,
			"flags":           item.Flags.String(),
			"addresses":       values,
		})
	}
	return out, nil
}

func httpRequest(ctx context.Context, args map[string]any) Result {
	rawURL, err := argString(args, "url")
	if err != nil {
		return invalid(err.Error())
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return invalid("url 必须是有效 HTTP/HTTPS 地址")
	}
	method, err := argOptionalString(args, http.MethodGet, "method")
	if err != nil {
		return invalid(err.Error())
	}
	body, err := argOptionalString(args, "", "body")
	if err != nil {
		return invalid(err.Error())
	}
	headers, err := argStringMap(args, "headers")
	if err != nil {
		return invalid(err.Error())
	}
	timeout, err := argDuration(args, 30*time.Second)
	if err != nil {
		return invalid(err.Error())
	}
	maxBytes, err := argInt64(args, 4*1024*1024, "maxResponseBytes")
	if err != nil || maxBytes <= 0 || maxBytes > 64*1024*1024 {
		return invalid("maxResponseBytes 必须在 1 到 67108864 之间")
	}
	requestContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, strings.ToUpper(method), rawURL, strings.NewReader(body))
	if err != nil {
		return invalid(err.Error())
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	started := time.Now()
	response, err := (&http.Client{Timeout: timeout}).Do(request)
	if err != nil {
		code := "NETWORK_ERROR"
		if errors.Is(err, context.DeadlineExceeded) {
			code = "TIMEOUT"
		}
		return fail(code, "HTTP 请求失败", err, "net_http")
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, maxBytes+1)
	content, err := io.ReadAll(limited)
	if err != nil {
		return fail("NETWORK_ERROR", "HTTP 响应读取失败", err, "net_http")
	}
	truncated := int64(len(content)) > maxBytes
	if truncated {
		content = content[:maxBytes]
	}
	headerMap := make(map[string][]string, len(response.Header))
	for key, values := range response.Header {
		headerMap[key] = append([]string(nil), values...)
	}
	data := map[string]any{
		"url": rawURL, "status": response.StatusCode, "statusText": response.Status,
		"headers": headerMap, "body": string(content), "truncated": truncated,
		"durationMs": time.Since(started).Milliseconds(),
	}
	if response.StatusCode < 200 || response.StatusCode >= 400 {
		result := fail("HTTP_ERROR", "HTTP 服务返回错误状态", fmt.Errorf("%s", response.Status), "net_http")
		result.Data = data
		return result
	}
	return ok("HTTP 请求成功", data, "net_http")
}

func readRoutes() ([]map[string]any, error) {
	file, err := os.Open("/proc/net/route")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	routes := make([]map[string]any, 0)
	first := true
	for scanner.Scan() {
		if first {
			first = false
			continue
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) < 8 {
			continue
		}
		destination, destErr := parseRouteIPv4(fields[1])
		gateway, gatewayErr := parseRouteIPv4(fields[2])
		mask, maskErr := parseRouteIPv4(fields[7])
		if destErr != nil || gatewayErr != nil || maskErr != nil {
			continue
		}
		flags, _ := strconv.ParseUint(fields[3], 16, 32)
		routes = append(routes, map[string]any{
			"interface": fields[0], "destination": destination, "gateway": gateway,
			"mask": mask, "flags": flags,
		})
	}
	return routes, scanner.Err()
}

func parseRouteIPv4(value string) (string, error) {
	raw, err := hex.DecodeString(value)
	if err != nil || len(raw) != 4 {
		return "", errors.New("invalid route IPv4")
	}
	number := binary.LittleEndian.Uint32(raw)
	return net.IPv4(byte(number>>24), byte(number>>16), byte(number>>8), byte(number)).String(), nil
}

func readDNSConfiguration() map[string]any {
	result := map[string]any{}
	if raw, err := os.ReadFile("/etc/resolv.conf"); err == nil {
		servers := make([]string, 0)
		for _, line := range strings.Split(string(raw), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 2 && fields[0] == "nameserver" {
				servers = append(servers, fields[1])
			}
		}
		result["resolvConf"] = servers
	}
	properties := make(map[string]string)
	if getprop, err := exec.LookPath("getprop"); err == nil {
		for _, key := range []string{"net.dns1", "net.dns2", "net.dns3", "net.dns4"} {
			output, runErr := exec.Command(getprop, key).Output()
			if runErr == nil && strings.TrimSpace(string(output)) != "" {
				properties[key] = strings.TrimSpace(string(output))
			}
		}
	}
	result["androidProperties"] = properties
	return result
}

type commandVariant struct {
	Name     string
	Args     []string
	Strategy string
}

func runFirstAvailable(ctx context.Context, m *Manager, variants []commandVariant, timeout time.Duration) Result {
	missing := make([]string, 0)
	for _, variant := range variants {
		if _, err := exec.LookPath(variant.Name); err != nil {
			missing = append(missing, variant.Name)
			continue
		}
		result := m.runCommand(ctx, commandSpec{Name: variant.Name, Args: variant.Args, Dir: m.cfg.WorkDir, Timeout: timeout, Strategy: variant.Strategy})
		if result.Code != "COMMAND_UNAVAILABLE" {
			return result
		}
	}
	return unavailable(strings.Join(missing, "/"))
}
