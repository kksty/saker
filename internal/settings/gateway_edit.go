package settings

// The tool API gateway (cyber-harness exts/gateway) binds external APIs to the
// scan node's existing clients: `gateway.<instance>` holds the upstream
// address, kind and credentials, and `cyberhub.proxy` selects a Clash instance
// or a proxy URL. The console edits that fragment as YAML because the service
// schemas belong to the harness and every instance differs (base_url/key,
// office url/refresh/strategy, shodan-idb enabled only).
//
// The read path masks the harness's secret fields (key/url/proxy) and a proxy
// URL with userinfo; the save path restores them from the stored document, so a
// round trip cannot overwrite a working credential with the mask. Invalid shapes
// and unknown fields are rejected here; the scan's own offline validator is the
// final authority on what the host will accept.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrGatewayConfig marks a rejected gateway document: the API answers 400 with
// the reason and nothing is written.
var ErrGatewayConfig = errors.New("工具 API 网关配置无效")

// GatewayInput is one console save of the editable gateway document.
type GatewayInput struct {
	Document string `json:"document"`
}

// SaveGateway replaces the `gateway` section and `cyberhub.proxy` with the
// submitted document, leaving every other section of cyber.yaml untouched.
//
// A `***` in key/url/proxy keeps the stored value; deleting the line removes
// it. validate, when present, runs the scan binary's offline validator against
// the candidate sections before anything is written, and its text is returned
// with stored and submitted credentials redacted.
func (s *Store) SaveGateway(in GatewayInput, validate func([]byte) error) (bool, error) {
	if s.harness == "" {
		return false, fmt.Errorf("%w：没有 harness 配置路径，设置 SAKER_HARNESS_CONFIG", ErrGatewayConfig)
	}
	gateway, proxy, err := parseGatewayDocument(in.Document)
	if err != nil {
		return false, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	root, doc, err := s.readDocument()
	if err != nil {
		return false, err
	}
	flattenLegacyExtensions(doc)
	if cyberhub := mapGet(doc, "cyberhub"); cyberhub != nil && cyberhub.Kind != yaml.MappingNode {
		return false, fmt.Errorf("%w：已保存的 cyberhub 段不是映射，请先在 cyber.yaml 修正", ErrGatewayConfig)
	}
	if err := s.mergeGatewaySecrets(gateway, &proxy, doc); err != nil {
		return false, err
	}
	changed, err := gatewayChanged(doc, gateway, proxy)
	if err != nil {
		return false, err
	}
	if !changed {
		return false, nil
	}
	if validate != nil {
		candidate, err := gatewayCandidate(gateway, proxy)
		if err != nil {
			return false, err
		}
		if err := validate(candidate); err != nil {
			return false, fmt.Errorf("%w：%s", ErrGatewayConfig, s.redactGatewayText(err.Error(), gateway, proxy))
		}
	}
	setGatewaySection(doc, gateway)
	setCyberhubProxy(doc, proxy)
	if err := validateRootDocument(doc); err != nil {
		return false, fmt.Errorf("%w：%s", ErrGatewayConfig, err)
	}
	if err := s.writeDocument(root); err != nil {
		return false, err
	}
	return true, nil
}

func parseGatewayDocument(text string) (*yaml.Node, string, error) {
	if strings.TrimSpace(text) == "" {
		return nil, "", fmt.Errorf("%w：配置为空；清空网关请写 gateway: {}", ErrGatewayConfig)
	}
	var root yaml.Node
	decoder := yaml.NewDecoder(strings.NewReader(text))
	if err := decoder.Decode(&root); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, "", fmt.Errorf("%w：配置为空；清空网关请写 gateway: {}", ErrGatewayConfig)
		}
		return nil, "", fmt.Errorf("%w：不是合法 YAML（%s）", ErrGatewayConfig, yamlMessage(err))
	}
	var extra yaml.Node
	switch err := decoder.Decode(&extra); {
	case err == nil:
		return nil, "", fmt.Errorf("%w：只能包含一个 YAML 文档", ErrGatewayConfig)
	case !errors.Is(err, io.EOF):
		return nil, "", fmt.Errorf("%w：不是合法 YAML（%s）", ErrGatewayConfig, yamlMessage(err))
	}
	doc := documentRoot(&root)
	if doc == nil || doc.Kind != yaml.MappingNode {
		return nil, "", fmt.Errorf("%w：根节点必须是映射，只写 gateway 与 cyberhub 两段", ErrGatewayConfig)
	}
	for i := 0; i+1 < len(doc.Content); i += 2 {
		switch key := doc.Content[i].Value; key {
		case "gateway", "cyberhub":
		default:
			return nil, "", fmt.Errorf("%w：只接受 gateway 与 cyberhub 两段，发现 %q", ErrGatewayConfig, key)
		}
	}
	if err := rejectDuplicateGatewayKeys(doc, ""); err != nil {
		return nil, "", err
	}
	gateway := mapGet(doc, "gateway")
	if gateway == nil {
		gateway = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	} else if gateway.Kind != yaml.MappingNode {
		return nil, "", fmt.Errorf("%w：gateway 必须是映射（实例名 → 配置）", ErrGatewayConfig)
	}
	if err := validateGatewayInstances(gateway); err != nil {
		return nil, "", err
	}
	proxy := ""
	if cyberhub := mapGet(doc, "cyberhub"); cyberhub != nil {
		if cyberhub.Kind != yaml.MappingNode {
			return nil, "", fmt.Errorf("%w：cyberhub 必须是映射", ErrGatewayConfig)
		}
		for i := 0; i+1 < len(cyberhub.Content); i += 2 {
			if key := cyberhub.Content[i].Value; key != "proxy" {
				return nil, "", fmt.Errorf("%w：cyberhub 段只接受 proxy；%q 请直接编辑 cyber.yaml", ErrGatewayConfig, key)
			}
		}
		if node := mapGet(cyberhub, "proxy"); node != nil {
			if node.Kind != yaml.ScalarNode || (node.Tag != "!!str" && node.Tag != "!!null") {
				return nil, "", fmt.Errorf("%w：cyberhub.proxy 必须是字符串", ErrGatewayConfig)
			}
			proxy = strings.TrimSpace(node.Value)
		}
	}
	return gateway, proxy, nil
}

func validateGatewayInstances(gateway *yaml.Node) error {
	for i := 0; i+1 < len(gateway.Content); i += 2 {
		name, instance := gateway.Content[i].Value, gateway.Content[i+1]
		if !validGatewayName(name) {
			return fmt.Errorf("%w：实例名 %q 不合法（不能为空、带首尾空白或含 . / \\）", ErrGatewayConfig, name)
		}
		if instance.Kind != yaml.MappingNode {
			return fmt.Errorf("%w：gateway.%s 必须是映射", ErrGatewayConfig, name)
		}
		for j := 0; j+1 < len(instance.Content); j += 2 {
			field, value := instance.Content[j].Value, instance.Content[j+1]
			if strings.TrimSpace(field) == "" {
				return fmt.Errorf("%w：gateway.%s 含空字段名", ErrGatewayConfig, name)
			}
			if value.Kind != yaml.ScalarNode {
				return fmt.Errorf("%w：gateway.%s.%s 只能是标量值", ErrGatewayConfig, name, field)
			}
			switch field {
			case "enabled":
				if value.Tag != "!!bool" {
					return fmt.Errorf("%w：gateway.%s.enabled 必须是 true 或 false", ErrGatewayConfig, name)
				}
			case "kind":
				if value.Tag != "!!str" || strings.TrimSpace(value.Value) == "" {
					return fmt.Errorf("%w：gateway.%s.kind 必须是非空字符串", ErrGatewayConfig, name)
				}
			}
		}
	}
	return nil
}

// validGatewayName mirrors cyber-harness exts/gateway kindName: an instance name
// must survive a round trip as a YAML key without separators.
func validGatewayName(name string) bool {
	return name != "" && strings.TrimSpace(name) == name && !strings.ContainsAny(name, ". /\\\t\r\n")
}

func rejectDuplicateGatewayKeys(node *yaml.Node, path string) error {
	if node.Kind != yaml.MappingNode {
		return nil
	}
	seen := make(map[string]bool, len(node.Content)/2)
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i].Value
		where := key
		if path != "" {
			where = path + "." + key
		}
		if seen[key] {
			return fmt.Errorf("%w：%s 出现了多次", ErrGatewayConfig, where)
		}
		seen[key] = true
		if err := rejectDuplicateGatewayKeys(node.Content[i+1], where); err != nil {
			return err
		}
	}
	return nil
}

// mergeGatewaySecrets restores a masked placeholder from the stored document. A
// placeholder with no stored value is an error, not a silent drop.
func (s *Store) mergeGatewaySecrets(gateway *yaml.Node, proxy *string, doc *yaml.Node) error {
	stored := sectionNode(doc, "gateway")
	for i := 0; i+1 < len(gateway.Content); i += 2 {
		name, instance := gateway.Content[i].Value, gateway.Content[i+1]
		for _, field := range gatewaySecretFields {
			value := mapGet(instance, field)
			if value == nil || value.Tag != "!!str" || value.Value != maskedValue {
				continue
			}
			previous := ""
			if stored != nil && stored.Kind == yaml.MappingNode {
				previous = scalar(mapGet(mapGet(stored, name), field))
			}
			if previous == "" {
				return fmt.Errorf("%w：gateway.%s.%s 是脱敏占位符，但没有已保存的值；请重新填写", ErrGatewayConfig, name, field)
			}
			value.Value = previous
			value.Tag = "!!str"
			value.Style = 0
		}
	}
	if *proxy == maskedValue {
		previous := scalar(mapGet(sectionNode(doc, "cyberhub"), "proxy"))
		if previous == "" {
			return fmt.Errorf("%w：cyberhub.proxy 是脱敏占位符，但没有已保存的值；请重新填写", ErrGatewayConfig)
		}
		*proxy = previous
	}
	return nil
}

func gatewayChanged(doc, gateway *yaml.Node, proxy string) (bool, error) {
	var stored, candidate map[string]any
	if node := sectionNode(doc, "gateway"); node != nil {
		// A shape the harness would reject is worth replacing: treat a decode
		// failure as a change instead of reporting "nothing to save".
		if node.Kind != yaml.MappingNode || node.Decode(&stored) != nil {
			return true, nil
		}
	}
	if len(gateway.Content) > 0 {
		if err := gateway.Decode(&candidate); err != nil {
			return false, fmt.Errorf("%w：%s", ErrGatewayConfig, err)
		}
	}
	if !reflect.DeepEqual(stored, candidate) {
		return true, nil
	}
	return scalar(mapGet(sectionNode(doc, "cyberhub"), "proxy")) != proxy, nil
}

func setGatewaySection(doc, gateway *yaml.Node) {
	if len(gateway.Content) == 0 {
		mapDelete(doc, "gateway")
		return
	}
	mapSet(doc, "gateway", gateway)
}

func setCyberhubProxy(doc *yaml.Node, proxy string) {
	if strings.TrimSpace(proxy) == "" {
		cyberhub := mapGet(doc, "cyberhub")
		if cyberhub == nil || cyberhub.Kind != yaml.MappingNode {
			return
		}
		mapDelete(cyberhub, "proxy")
		if len(cyberhub.Content) == 0 {
			mapDelete(doc, "cyberhub")
		}
		return
	}
	mapSet(ensureMapping(doc, "cyberhub"), "proxy", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: proxy})
}

func gatewayCandidate(gateway *yaml.Node, proxy string) ([]byte, error) {
	sections := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	if len(gateway.Content) > 0 {
		mapSet(sections, "gateway", gateway)
	}
	if proxy != "" {
		cyberhub := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		mapSet(cyberhub, "proxy", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: proxy})
		mapSet(sections, "cyberhub", cyberhub)
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	if err := enc.Encode(&yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{sections}}); err != nil {
		return nil, fmt.Errorf("%w：无法编码候选配置", ErrGatewayConfig)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("%w：无法编码候选配置", ErrGatewayConfig)
	}
	return buf.Bytes(), nil
}

// redactGatewayText keeps stored and submitted credentials out of a validator
// message the console displays.
func (s *Store) redactGatewayText(text string, gateway *yaml.Node, proxy string) string {
	text = s.redactSecrets(text)
	for i := 0; i+1 < len(gateway.Content); i += 2 {
		for _, field := range gatewaySecretFields {
			if value := scalar(mapGet(gateway.Content[i+1], field)); value != "" {
				text = strings.ReplaceAll(text, value, maskedValue)
			}
		}
	}
	if strings.Contains(proxy, "://") || strings.Contains(proxy, "@") {
		text = strings.ReplaceAll(text, proxy, maskedValue)
	}
	return text
}

// yamlMessage trims a decoder error to its first line: the operator needs the
// line number and the reason, not a multi-line dump.
func yamlMessage(err error) string {
	message := strings.TrimSpace(err.Error())
	if line, _, found := strings.Cut(message, "\n"); found {
		message = strings.TrimSpace(line)
	}
	if len(message) > 200 {
		message = message[:200] + "…"
	}
	return message
}
