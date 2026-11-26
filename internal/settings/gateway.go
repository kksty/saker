package settings

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// GatewayView describes the saved configuration. Runtime validation and
// connection state belong to the harness.
//
// Editable/Document make the panel an editor rather than a status card: the
// document is the operator-facing YAML for the `gateway` section plus
// `cyberhub.proxy`, with the fields the harness treats as secrets (key/url/
// proxy, and a proxy URL carrying userinfo) replaced by maskedValue. Saving a
// placeholder keeps the stored value; see SaveGateway.
type GatewayView struct {
	Instances       int    `json:"instances"`
	Enabled         int    `json:"enabled"`
	ProxyConfigured bool   `json:"proxy_configured"`
	Reason          string `json:"reason,omitempty"`
	// Editable says the console may save the section. It is false when the
	// operator file cannot be read or the saved shape cannot be rendered as a
	// mapping (Reason then names the problem and the file has to be fixed by
	// hand).
	Editable bool `json:"editable"`
	// Document is the masked YAML the console edits. It always carries both
	// top-level keys so an empty configuration still shows where instances and
	// the proxy selector go.
	Document string `json:"document,omitempty"`
}

// gatewaySecretFields are the per-instance fields the harness (and Saker's own
// redaction backstop in SecretValues) treats as key material. They are masked on
// read and preserved from the stored document when a save posts maskedValue.
var gatewaySecretFields = []string{"key", "url", "proxy"}

func (s *Store) gatewayView() GatewayView {
	_, doc, err := s.readDocument()
	if err != nil {
		return GatewayView{Reason: "无法读取 harness 配置"}
	}
	var view GatewayView
	view.Editable = true
	problem := ""
	if proxy := mapGet(sectionNode(doc, "cyberhub"), "proxy"); proxy != nil {
		if proxy.Kind != yaml.ScalarNode || proxy.Tag != "!!str" {
			problem = "cyberhub.proxy 必须是字符串"
		} else {
			view.ProxyConfigured = scalar(proxy) != ""
		}
	}
	if problem == "" {
		if section := sectionNode(doc, "gateway"); section != nil {
			switch section.Kind {
			case yaml.MappingNode:
				problem = countGatewayInstances(section, &view)
			default:
				problem = "gateway 必须是映射"
			}
		}
	}
	// Render even a malformed-but-flat section: an operator-facing editor is
	// where a wrong enabled flag gets fixed. Only a shape that cannot be shown
	// as an instance mapping disables editing.
	if document, err := gatewayDocument(doc); err != nil {
		view.Reason = err.Error()
		view.Editable = false
	} else {
		view.Document = document
		if problem != "" {
			view.Reason = problem
		}
	}
	return view
}

// countGatewayInstances adds the instance/enabled tallies and reports the first
// structural problem, leaving the counts it reached before it.
func countGatewayInstances(section *yaml.Node, view *GatewayView) string {
	for i := 1; i < len(section.Content); i += 2 {
		instance := section.Content[i]
		if instance.Kind != yaml.MappingNode {
			return "gateway 实例必须是映射"
		}
		enabled := mapGet(instance, "enabled")
		active := true
		if enabled != nil && (enabled.Tag != "!!bool" || enabled.Decode(&active) != nil) {
			return "gateway 实例的 enabled 必须是布尔值"
		}
		view.Instances++
		if active {
			view.Enabled++
		}
	}
	return ""
}

// gatewayDocument renders the editable projection: `gateway` plus
// `cyberhub.proxy`, masked. It never returns credential values.
func gatewayDocument(doc *yaml.Node) (string, error) {
	gateway := sectionNode(doc, "gateway")
	if gateway == nil {
		gateway = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	}
	if gateway.Kind != yaml.MappingNode {
		return "", fmt.Errorf("gateway 必须是映射，无法在设置页编辑")
	}
	rendered := cloneYAMLNode(gateway)
	maskGatewaySecrets(rendered)

	proxy := ""
	cyberhub := sectionNode(doc, "cyberhub")
	if cyberhub != nil && cyberhub.Kind != yaml.MappingNode {
		return "", fmt.Errorf("cyberhub 必须是映射，无法在设置页编辑")
	}
	if node := mapGet(cyberhub, "proxy"); node != nil {
		if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
			return "", fmt.Errorf("cyberhub.proxy 必须是字符串，无法在设置页编辑")
		}
		proxy = node.Value
		// A proxy URL can embed credentials; an instance name cannot.
		if strings.Contains(proxy, "@") {
			proxy = maskedValue
		}
	}
	proxyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: proxy}
	if proxy == "" {
		proxyNode.Style = yaml.SingleQuotedStyle
	}

	root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
		scalarKey("gateway"), rendered,
		scalarKey("cyberhub"), &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
			scalarKey("proxy"), proxyNode,
		}},
	}}
	var buf strings.Builder
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}); err != nil {
		return "", fmt.Errorf("无法渲染网关配置")
	}
	if err := enc.Close(); err != nil {
		return "", fmt.Errorf("无法渲染网关配置")
	}
	return buf.String(), nil
}

func maskGatewaySecrets(gateway *yaml.Node) {
	for i := 0; i+1 < len(gateway.Content); i += 2 {
		instance := gateway.Content[i+1]
		if instance.Kind != yaml.MappingNode {
			continue
		}
		for _, field := range gatewaySecretFields {
			if node := mapGet(instance, field); node != nil && node.Kind == yaml.ScalarNode && node.Value != "" {
				node.Value = maskedValue
				node.Tag = "!!str"
				node.Style = 0
			}
		}
	}
}

func scalarKey(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

// cloneYAMLNode copies a node tree, comments included, so masking a projection
// never touches the document that will be written back.
func cloneYAMLNode(node *yaml.Node) *yaml.Node {
	if node == nil {
		return nil
	}
	clone := *node
	if node.Content != nil {
		clone.Content = make([]*yaml.Node, len(node.Content))
		for i, child := range node.Content {
			clone.Content[i] = cloneYAMLNode(child)
		}
	}
	return &clone
}
