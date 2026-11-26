package settings

import (
	"errors"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

func (s *Store) readDocument() (*yaml.Node, *yaml.Node, error) {
	if s.harness == "" {
		return nil, nil, errors.New("没有 harness 配置路径：设置 SAKER_HARNESS_CONFIG")
	}
	root := &yaml.Node{Kind: yaml.DocumentNode}
	data, err := os.ReadFile(s.harness)
	switch {
	case err == nil:
		if err := yaml.Unmarshal(data, root); err != nil {
			// YAML errors may quote a line containing credentials.
			return nil, nil, fmt.Errorf("%s 不是有效 YAML", s.harness)
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		return nil, nil, err
	}
	doc := documentRoot(root)
	if doc == nil {
		doc = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		root.Content = []*yaml.Node{doc}
	}
	if doc.Kind != yaml.MappingNode {
		return nil, nil, errors.New("harness 配置根节点必须是映射")
	}
	// Node decoding alone accepts duplicate mapping keys; reject ambiguous
	// documents before mapGet can silently pick a different value than harness.
	var checked map[string]any
	if err := doc.Decode(&checked); err != nil {
		return nil, nil, errors.New("harness 配置有重复字段或无效结构")
	}
	return root, doc, nil
}
