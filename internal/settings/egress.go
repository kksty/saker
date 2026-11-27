package settings

// The tool egress: which upstream the scan node will dial (`cyberhub.proxy`),
// whether capture is on, and the minimal document a throwaway probe process
// needs. This file only describes saved configuration; "is it reachable" is a
// probe that belongs to the console (cmd/saker/egress.go), because running it
// here would make a settings read perform network I/O.

import (
	"bytes"
	"fmt"
	"net/url"
	"strings"

	"gopkg.in/yaml.v3"
)

// EgressView is the operator-facing snapshot of the saved egress selection.
type EgressView struct {
	// Mode is direct (no upstream), single (a proxy URL) or subscription (a
	// clash selection). It reports the effective egress: a selection the scan
	// will not load (a disabled instance) reports direct and explains itself in
	// Invalid.
	Mode string `json:"mode"`
	// Configured is true when the saved selection names an egress the scan will
	// actually load.
	Configured bool `json:"configured"`
	// Egress is the instance name, or a proxy URL with userinfo removed.
	Egress  string `json:"egress,omitempty"`
	Capture bool   `json:"capture"`
	// Invalid names a saved shape the scan refuses at startup, or a selected
	// instance the scan will not load, so the console does not report
	// "configured" as if it were working.
	Invalid string `json:"invalid,omitempty"`
}

func (s *Store) EgressView() EgressView {
	if s.harness == "" {
		return EgressView{Mode: "unknown", Invalid: "没有 harness 配置路径"}
	}
	_, doc, err := s.readDocument()
	if err != nil {
		return EgressView{Mode: "unknown", Invalid: "无法读取 harness 配置"}
	}
	return egressViewOf(doc)
}

func egressViewOf(doc *yaml.Node) EgressView {
	view := EgressView{Mode: "direct", Capture: true}
	if mitm := mapGet(sectionNode(doc, "cyberhub"), "mitm"); mitm != nil &&
		mitm.Kind == yaml.ScalarNode && mitm.Tag == "!!bool" {
		var on bool
		if mitm.Decode(&on) == nil {
			view.Capture = on
		}
	}
	proxy := scalar(mapGet(sectionNode(doc, "cyberhub"), "proxy"))
	instances := clashInstances(sectionNode(doc, "gateway"))
	switch {
	case strings.Contains(proxy, "://"):
		view.Mode, view.Configured, view.Egress = "single", true, maskEgressURL(proxy)
	case proxy != "":
		instance, found := findClash(instances, proxy)
		switch {
		case !found:
			view.Mode, view.Egress = "subscription", proxy
			view.Invalid = fmt.Sprintf("cyberhub.proxy 指向 %q，但 gateway 段里没有同名 clash 实例", proxy)
		case !instance.enabled:
			// The scan accepts the name, then loads nothing for a disabled
			// instance: the selected egress is silently not in effect.
			view.Mode = "direct"
			view.Invalid = fmt.Sprintf("cyberhub.proxy 指向已停用的 clash 实例 %q：scan 不会加载出口，流量走直连", proxy)
		default:
			view.Mode, view.Configured, view.Egress = "subscription", true, proxy
		}
	case len(instances) == 1 && instances[0].enabled:
		view.Mode, view.Configured, view.Egress = "subscription", true, instances[0].name
	case len(instances) > 1:
		// Every instance of the kind counts, enabled or not: an omitted selection
		// with more than one match is refused when the scan loads the gateway.
		view.Mode = "subscription"
		view.Invalid = fmt.Sprintf("有 %d 个 clash 实例（%s）但 cyberhub.proxy 未指定：scan 启动会拒绝", len(instances), clashNames(instances))
	}
	return view
}

// EgressProbeDocument is the document a throwaway scan process gets: the egress
// selection, capture off, and every clash instance, disabled ones included, so
// the probe's startup matches what the real scan would do with this config (an
// omitted selection with two clash instances, or a name that resolves to none,
// fails the same way there). Nothing else is copied — no llm key, no product
// sections, no data_dir — so a probe cannot read credentials or touch the
// operator's project data.
func (s *Store) EgressProbeDocument() ([]byte, error) {
	if s.harness == "" {
		return nil, fmt.Errorf("没有 harness 配置路径")
	}
	_, doc, err := s.readDocument()
	if err != nil {
		return nil, err
	}
	root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	cyberhub := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	mapSet(cyberhub, "mitm", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "false"})
	if proxy := scalar(mapGet(sectionNode(doc, "cyberhub"), "proxy")); proxy != "" {
		mapSet(cyberhub, "proxy", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: proxy})
	}
	mapSet(root, "cyberhub", cyberhub)
	if gateway := sectionNode(doc, "gateway"); gateway != nil && gateway.Kind == yaml.MappingNode {
		clash := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for i := 0; i+1 < len(gateway.Content); i += 2 {
			name, instance := gateway.Content[i], gateway.Content[i+1]
			if isClashKind(name.Value, instance) {
				clash.Content = append(clash.Content, cloneYAMLNode(name), cloneYAMLNode(instance))
			}
		}
		if len(clash.Content) > 0 {
			mapSet(root, "gateway", clash)
		}
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}); err != nil {
		return nil, fmt.Errorf("无法编码探测配置")
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("无法编码探测配置")
	}
	return buf.Bytes(), nil
}

// clashInstance is one gateway entry whose kind resolves to clash. Disabled
// instances still count: the scan's gateway binding matches every instance of
// the kind and refuses an omitted selection that matches more than one; the
// enabled flag only decides whether the selected instance is loaded.
type clashInstance struct {
	name    string
	enabled bool
}

// clashInstances lists every clash instance, defaulting a missing kind to the
// instance name the way exts/gateway does.
func clashInstances(gateway *yaml.Node) []clashInstance {
	if gateway == nil || gateway.Kind != yaml.MappingNode {
		return nil
	}
	out := make([]clashInstance, 0, len(gateway.Content)/2)
	for i := 0; i+1 < len(gateway.Content); i += 2 {
		name, instance := gateway.Content[i].Value, gateway.Content[i+1]
		if !isClashKind(name, instance) {
			continue
		}
		out = append(out, clashInstance{name: name, enabled: instanceEnabled(instance)})
	}
	return out
}

func findClash(instances []clashInstance, name string) (clashInstance, bool) {
	for _, instance := range instances {
		if instance.name == name {
			return instance, true
		}
	}
	return clashInstance{}, false
}

func clashNames(instances []clashInstance) string {
	names := make([]string, 0, len(instances))
	for _, instance := range instances {
		names = append(names, instance.name)
	}
	return strings.Join(names, "、")
}

func isClashKind(name string, instance *yaml.Node) bool {
	if instance == nil || instance.Kind != yaml.MappingNode {
		return false
	}
	kind := scalar(mapGet(instance, "kind"))
	if kind == "" {
		kind = name
	}
	return strings.EqualFold(strings.TrimSpace(kind), "clash")
}

// instanceEnabled mirrors the gateway's enabled flag: absent means enabled, and
// only a real false disables the instance.
func instanceEnabled(instance *yaml.Node) bool {
	enabled := mapGet(instance, "enabled")
	if enabled == nil {
		return true
	}
	var on bool
	return enabled.Kind == yaml.ScalarNode && enabled.Decode(&on) == nil && on
}

// maskEgressURL keeps scheme://host:port and drops credentials: a proxy URL can
// carry a password, and the status bar never needs it.
func maskEgressURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "***"
	}
	if u.User != nil {
		return u.Scheme + "://***@" + u.Host
	}
	return u.Scheme + "://" + u.Host
}
