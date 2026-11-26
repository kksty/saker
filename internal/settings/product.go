package settings

// Product configuration (MCP servers and web search) is the scan node's own
// configuration, not a shared harness option. It lives in the operator's
// cyber.yaml under `extensions` — the one place the harness's strict file
// validator accepts product sections it does not itself declare. The generated
// runtime yaml copies the document through unchanged, so the scan reads the
// same sections from the file the local host is started with.
//
// The settings API is the operator surface for these two sections. It never
// echoes a stored secret: MCP env/header values read back as maskedValue, and
// the Tavily key reads back as has_key/count only. On save a masked value is
// kept from the stored section instead of being written literally.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// maskedValue is the placeholder the read path uses for stored MCP env/header
// values. A save that sends it back keeps the previous value (it is not a
// credential anyone could legitimately want to set).
const maskedValue = "***"

// mcpAliasPattern mirrors cyber-harness tools/mcp ServerConfig.Validate.
var mcpAliasPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// MCPServer is one MCP server declaration; the JSON shape is the harness's
// tools/mcp.ServerConfig (json tags are what the scan decodes).
type MCPServer struct {
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Cwd     string            `json:"cwd,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	// Tools has no omitempty on purpose, mirroring the harness: nil means "mount
	// every tool", an explicit empty list means "mount none". A round-trip must
	// not collapse those two states.
	Tools                 []string `json:"tools"`
	StartupTimeoutSeconds int64    `json:"startupTimeoutSeconds,omitempty"`
	TimeoutSeconds        int64    `json:"timeoutSeconds,omitempty"`
}

// Product is the stored product configuration. An empty Server map means the
// section is absent (the scan does not install MCP servers).
type Product struct {
	MCPServers map[string]MCPServer
	SearchKeys string
}

// ProductInput is one settings save. Nil MCPServers keeps the stored servers;
// an empty (non-nil) map removes the section. An empty SearchKey keeps the
// stored keys; ClearSearchKey removes them.
type ProductInput struct {
	MCPServers     *map[string]MCPServer
	SearchKey      string
	ClearSearchKey bool
}

// ProductView is the API projection: no secret values, only their presence.
type ProductView struct {
	MCPServers     map[string]MCPServer
	SearchKeySet   bool
	SearchKeyCount int
}

// validateProductAlias mirrors the harness's name rule. The control plane names
// the bad alias in its error instead of failing later at scan startup.
func validateProductAlias(alias string) error {
	if !mcpAliasPattern.MatchString(alias) {
		return fmt.Errorf("MCP 服务器别名 %q 只能用字母、数字、- 和 _", alias)
	}
	return nil
}

// validateMCP mirrors cyber-harness tools/mcp ServerConfig.Validate, with the
// scan's own error text translated. The control plane writes the file without
// the harness package, so the operator gets the error while editing settings.
func validateMCP(alias string, server MCPServer) error {
	if err := validateProductAlias(alias); err != nil {
		return err
	}
	if (server.Command == "") == (server.URL == "") {
		return fmt.Errorf("MCP 服务器 %s 必须且只能配置 command 或 url 之一", alias)
	}
	if server.Command != "" {
		if len(server.Headers) != 0 {
			return fmt.Errorf("MCP 服务器 %s：headers 只能与 url 一起使用", alias)
		}
		for key, value := range server.Env {
			if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, 0) {
				return fmt.Errorf("MCP 服务器 %s：环境变量条目不合法（键不能为空或含 =）", alias)
			}
		}
	} else {
		if len(server.Args) != 0 || len(server.Env) != 0 || server.Cwd != "" {
			return fmt.Errorf("MCP 服务器 %s：args/env/cwd 只能与 command 一起使用", alias)
		}
		u, err := url.Parse(server.URL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" {
			return fmt.Errorf("MCP 服务器 %s 需要不含用户名/片段的绝对 HTTP(S) URL；认证信息放在 headers", alias)
		}
	}
	for _, seconds := range []int64{server.StartupTimeoutSeconds, server.TimeoutSeconds} {
		if seconds < 0 {
			return fmt.Errorf("MCP 服务器 %s：超时秒数不能为负", alias)
		}
	}
	seen := make(map[string]bool, len(server.Tools))
	for _, tool := range server.Tools {
		if tool == "" {
			return fmt.Errorf("MCP 服务器 %s：工具白名单不能含空项", alias)
		}
		if seen[tool] {
			return fmt.Errorf("MCP 服务器 %s：工具白名单重复：%s", alias, tool)
		}
		seen[tool] = true
	}
	return nil
}

// validateSearchKey accepts the harness's comma-separated Tavily key list. The
// value is written verbatim; keys are credentials, so only shape is checked.
func validateSearchKey(key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}
	if strings.ContainsAny(key, "\r\n\x00") {
		return fmt.Errorf("search_tavily_key 不能包含换行或 NUL")
	}
	for _, part := range strings.Split(key, ",") {
		if strings.TrimSpace(part) == "" {
			return fmt.Errorf("search_tavily_key 里逗号之间不能有空键")
		}
	}
	return nil
}

func searchKeyCount(key string) int {
	count := 0
	for _, part := range strings.Split(key, ",") {
		if strings.TrimSpace(part) != "" {
			count++
		}
	}
	return count
}

// Product reads the stored product sections. A missing file/section is an
// empty product, not an error. A malformed section is reported: silently
// showing "no MCP servers" would let a save wipe a configuration the operator
// cannot see.
func (s *Store) Product() (Product, error) {
	_, doc, err := s.readDocument()
	if err != nil {
		return Product{}, err
	}
	return productFromDocument(doc)
}

func productFromDocument(doc *yaml.Node) (Product, error) {
	var out Product
	extensions := mapGet(doc, "extensions")
	mcp := mapGet(extensions, "mcp")
	if servers := mapGet(mcp, "mcpServers"); servers != nil {
		if servers.Kind != yaml.MappingNode {
			return out, fmt.Errorf("extensions.mcp.mcpServers 必须是映射")
		}
		var raw map[string]map[string]any
		if err := servers.Decode(&raw); err != nil {
			return out, fmt.Errorf("extensions.mcp.mcpServers 结构无效：%w", err)
		}
		out.MCPServers = make(map[string]MCPServer, len(raw))
		for alias, fields := range raw {
			encoded, err := json.Marshal(fields)
			if err != nil {
				return out, fmt.Errorf("MCP 服务器 %s 结构无效：%w", alias, err)
			}
			var server MCPServer
			if err := json.Unmarshal(encoded, &server); err != nil {
				return out, fmt.Errorf("MCP 服务器 %s 结构无效：%w", alias, err)
			}
			out.MCPServers[alias] = server
		}
	}
	if search := mapGet(extensions, "search"); search != nil {
		if search.Kind != yaml.MappingNode {
			return out, fmt.Errorf("extensions.search 必须是映射")
		}
		out.SearchKeys = scalar(mapGet(search, "tavily_keys"))
	}
	return out, nil
}

// ProductView is the masked read. MCP env/header values become maskedValue.
func (s *Store) ProductView() (ProductView, error) {
	product, err := s.Product()
	if err != nil {
		return ProductView{}, err
	}
	view := ProductView{MCPServers: maskMCP(product.MCPServers), SearchKeySet: product.SearchKeys != "", SearchKeyCount: searchKeyCount(product.SearchKeys)}
	return view, nil
}

func maskMCP(servers map[string]MCPServer) map[string]MCPServer {
	if len(servers) == 0 {
		return nil
	}
	out := make(map[string]MCPServer, len(servers))
	for alias, server := range servers {
		server = cloneMCPServer(server)
		for key := range server.Env {
			server.Env[key] = maskedValue
		}
		for key := range server.Headers {
			server.Headers[key] = maskedValue
		}
		out[alias] = server
	}
	return out
}

func cloneMCPServer(server MCPServer) MCPServer {
	// Copy in a way that keeps nil and an explicit empty list distinct: the tool
	// allowlist uses that difference (omitted mounts everything, empty mounts
	// none).
	if server.Args != nil {
		server.Args = append([]string{}, server.Args...)
	}
	if server.Env != nil {
		env := make(map[string]string, len(server.Env))
		for key, value := range server.Env {
			env[key] = value
		}
		server.Env = env
	}
	if server.Headers != nil {
		headers := make(map[string]string, len(server.Headers))
		for key, value := range server.Headers {
			headers[key] = value
		}
		server.Headers = headers
	}
	if server.Tools != nil {
		server.Tools = append([]string{}, server.Tools...)
	}
	return server
}

// ValidateProduct is the save-time validation without writing: the API must be
// able to reject a bad product document before it persists the model half of a
// combined save.
func (s *Store) ValidateProduct(in ProductInput) error {
	if s.harness == "" {
		return fmt.Errorf("没有 harness 配置路径：设置 SAKER_HARNESS_CONFIG")
	}
	if in.MCPServers == nil && strings.TrimSpace(in.SearchKey) == "" && !in.ClearSearchKey {
		return nil
	}
	current, err := s.Product()
	if err != nil {
		return err
	}
	if in.MCPServers != nil {
		servers, err := mergeMCPSecrets(*in.MCPServers, current.MCPServers)
		if err != nil {
			return err
		}
		for alias, server := range servers {
			if err := validateMCP(alias, server); err != nil {
				return err
			}
		}
	}
	if in.ClearSearchKey {
		return nil
	}
	if key := strings.TrimSpace(in.SearchKey); key != "" {
		return validateSearchKey(key)
	}
	return nil
}

// ProductChanged reports whether a save would alter the stored product
// sections. The hot-update path is only safe for a shared-section (model)
// change; a product change needs the restart that makes the scan re-read its
// runtime file.
func (s *Store) ProductChanged(in ProductInput) (bool, error) {
	if in.MCPServers == nil && strings.TrimSpace(in.SearchKey) == "" && !in.ClearSearchKey {
		return false, nil
	}
	current, err := s.Product()
	if err != nil {
		return false, err
	}
	if in.MCPServers != nil {
		servers, err := mergeMCPSecrets(*in.MCPServers, current.MCPServers)
		if err != nil {
			return false, err
		}
		if !equalMCPServers(servers, current.MCPServers) {
			return true, nil
		}
	}
	search := current.SearchKeys
	if in.ClearSearchKey {
		search = ""
	} else if key := strings.TrimSpace(in.SearchKey); key != "" {
		search = key
	}
	return search != current.SearchKeys, nil
}

func equalMCPServers(a, b map[string]MCPServer) bool {
	if len(a) != len(b) {
		return false
	}
	for alias, left := range a {
		right, ok := b[alias]
		if !ok || !reflect.DeepEqual(left, right) {
			return false
		}
	}
	return true
}

// SaveProduct writes the product sections into the operator's harness file,
// preserving every other section and every other extension. writeMu is shared
// with Save: both rewrite the same YAML document, so two saves cannot each
// write back the other's stale view.
func (s *Store) SaveProduct(in ProductInput) error {
	if s.harness == "" {
		return fmt.Errorf("没有 harness 配置路径：设置 SAKER_HARNESS_CONFIG")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	root, doc, err := s.readDocument()
	if err != nil {
		return err
	}
	current, err := productFromDocument(doc)
	if err != nil {
		return err
	}
	if in.MCPServers != nil {
		servers, err := mergeMCPSecrets(*in.MCPServers, current.MCPServers)
		if err != nil {
			return err
		}
		for alias, server := range servers {
			if err := validateMCP(alias, server); err != nil {
				return err
			}
		}
		if err := setMCPSection(doc, servers); err != nil {
			return err
		}
	}
	searchKey := current.SearchKeys
	switch {
	case in.ClearSearchKey:
		searchKey = ""
	case strings.TrimSpace(in.SearchKey) != "":
		searchKey = strings.TrimSpace(in.SearchKey)
	}
	if err := validateSearchKey(searchKey); err != nil {
		return err
	}
	if in.ClearSearchKey || strings.TrimSpace(in.SearchKey) != "" {
		setSearchSection(doc, searchKey)
	}
	if err := validateRootDocument(doc); err != nil {
		return err
	}
	return s.writeDocument(root)
}

// mergeMCPSecrets replaces the maskedValue placeholder with the stored value so
// a console round-trip (read masked → edit one field → save) cannot overwrite a
// working credential with "***". A placeholder with no stored value is an error,
// not a silent drop.
func mergeMCPSecrets(incoming, stored map[string]MCPServer) (map[string]MCPServer, error) {
	out := make(map[string]MCPServer, len(incoming))
	for alias, server := range incoming {
		server = cloneMCPServer(server)
		was := stored[alias]
		for key, value := range server.Env {
			if value != maskedValue {
				continue
			}
			previous, ok := was.Env[key]
			if !ok {
				return nil, fmt.Errorf("MCP 服务器 %s 的环境变量 %s 是脱敏占位符，但没有已保存的值；请重新填写", alias, key)
			}
			server.Env[key] = previous
		}
		for key, value := range server.Headers {
			if value != maskedValue {
				continue
			}
			previous, ok := was.Headers[key]
			if !ok {
				return nil, fmt.Errorf("MCP 服务器 %s 的请求头 %s 是脱敏占位符，但没有已保存的值；请重新填写", alias, key)
			}
			server.Headers[key] = previous
		}
		out[alias] = server
	}
	return out, nil
}

func setMCPSection(doc *yaml.Node, servers map[string]MCPServer) error {
	extensions := ensureMapping(doc, "extensions")
	if len(servers) == 0 {
		mapDelete(mapGet(extensions, "mcp"), "mcpServers")
		return nil
	}
	mcp := ensureMapping(extensions, "mcp")
	encoded, err := json.Marshal(servers)
	if err != nil {
		return fmt.Errorf("编码 MCP 配置失败：%w", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return fmt.Errorf("编码 MCP 配置失败：%w", err)
	}
	var node yaml.Node
	if err := node.Encode(fields); err != nil {
		return fmt.Errorf("编码 MCP 配置失败：%w", err)
	}
	mapSet(mcp, "mcpServers", &node)
	return nil
}

func setSearchSection(doc *yaml.Node, key string) {
	extensions := ensureMapping(doc, "extensions")
	search := ensureMapping(extensions, "search")
	if key == "" {
		mapDelete(search, "tavily_keys")
		return
	}
	setScalar(search, "tavily_keys", key)
}

// writeDocument encodes the whole operator document with the same indentation
// writeHarnessModel uses, so a product save does not reflow unrelated sections.
func (s *Store) writeDocument(root *yaml.Node) error {
	var buf strings.Builder
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(4)
	if err := enc.Encode(root); err != nil {
		return fmt.Errorf("编码 %s 失败", s.harness)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("编码 %s 失败", s.harness)
	}
	return writeFileAtomic(s.harness, []byte(buf.String()))
}
