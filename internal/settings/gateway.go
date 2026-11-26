package settings

import "gopkg.in/yaml.v3"

// GatewayView describes the saved configuration. Runtime validation and
// connection state belong to the harness. No instance fields leave the store.
type GatewayView struct {
	Instances       int    `json:"instances"`
	Enabled         int    `json:"enabled"`
	ProxyConfigured bool   `json:"proxy_configured"`
	Reason          string `json:"reason,omitempty"`
}

func (s *Store) gatewayView() GatewayView {
	_, doc, err := s.readDocument()
	if err != nil {
		return GatewayView{Reason: "无法读取 harness 配置"}
	}
	var view GatewayView
	if proxy := mapGet(sectionNode(doc, "cyberhub"), "proxy"); proxy != nil {
		if proxy.Kind != yaml.ScalarNode || proxy.Tag != "!!str" {
			return GatewayView{Reason: "cyberhub.proxy 必须是字符串"}
		}
		view.ProxyConfigured = scalar(proxy) != ""
	}
	section := sectionNode(doc, "gateway")
	if section == nil {
		return view
	}
	if section.Kind != yaml.MappingNode {
		return GatewayView{Reason: "gateway 必须是映射"}
	}
	for i := 1; i < len(section.Content); i += 2 {
		instance := section.Content[i]
		if instance.Kind != yaml.MappingNode {
			return GatewayView{Reason: "gateway 实例必须是映射"}
		}
		enabled := mapGet(instance, "enabled")
		active := true
		if enabled != nil && (enabled.Tag != "!!bool" || enabled.Decode(&active) != nil) {
			return GatewayView{Reason: "gateway 实例的 enabled 必须是布尔值"}
		}
		view.Instances++
		if active {
			view.Enabled++
		}
	}
	return view
}
