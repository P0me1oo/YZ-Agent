package agentcli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// 安装器先生成迁移后的配置，再在持锁和停止服务后替换原文件。
func runConfigMigrateRoot(args []string) error {
	values := map[string]string{}
	for len(args) > 0 {
		if len(args) < 2 {
			return errors.New("migrate-root requires --config, --output, --from and --to")
		}
		key := args[0]
		switch key {
		case "--config", "--output", "--from", "--to":
		default:
			return fmt.Errorf("unknown migrate-root option: %s", key)
		}
		if _, exists := values[key]; exists {
			return fmt.Errorf("duplicate migrate-root option: %s", key)
		}
		values[key], args = args[1], args[2:]
	}
	for _, key := range []string{"--config", "--output", "--from", "--to"} {
		if values[key] == "" {
			return fmt.Errorf("migrate-root requires %s", key)
		}
	}
	for _, key := range []string{"--from", "--to"} {
		if _, err := validateBinDir(values[key]); err != nil {
			return err
		}
	}
	data, err := os.ReadFile(values["--config"])
	if err != nil {
		return err
	}
	data, err = migrateConfigRoot(data, strings.TrimRight(values["--from"], "/"), strings.TrimRight(values["--to"], "/"))
	if err != nil {
		return err
	}
	return os.WriteFile(values["--output"], data, 0o600)
}

// 只改本程序定义的文件路径；凭据、地址、实例标识及用户扩展字段保持原值。
func migrateConfigRoot(data []byte, from, to string) ([]byte, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("parse migration config: %w", err)
	}
	// 先由 YAML 解码器检查循环引用和过度展开，再隔离共享值。
	// 只有确实迁移了路径才输出展开后的文档；无改动时返回原始字节。
	var decoded any
	if err := document.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode migration config: %w", err)
	}
	isolated := expandMigrationAliases(&document)
	changed := false
	var walk func(*yaml.Node, string)
	walk = func(node *yaml.Node, section string) {
		if node.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(node.Content); i += 2 {
				key, value := node.Content[i].Value, node.Content[i+1]
				if node.Content[i].Tag == "!!merge" {
					walk(value, section)
					continue
				}
				isPath := section == "kernel" && (key == "config_dir" || key == "geo_data_dir" || key == "custom_config") ||
					section == "cert" && (key == "cert_file" || key == "key_file" || key == "cert_dir") ||
					section == "firewall" && key == "state_dir" || section == "log" && key == "output"
				if isPath && value.Kind == yaml.ScalarNode && value.Tag == "!!str" &&
					(value.Value == from || strings.HasPrefix(value.Value, from+"/")) {
					value.Value = to + strings.TrimPrefix(value.Value, from)
					changed = true
				}
				// 只进入配置结构定义的位置，避免改写扩展字段中的同名键。
				if section == "root" || section == "instance" {
					switch key {
					case "kernel", "cert", "firewall", "log", "nodes":
						walk(value, key)
					case "instances":
						if section == "root" {
							walk(value, key)
						}
					}
				} else if section == "node" && (key == "kernel" || key == "cert") {
					walk(value, key)
				}
			}
		} else {
			if section == "instances" {
				section = "instance"
			} else if section == "nodes" {
				section = "node"
			}
			for _, child := range node.Content {
				walk(child, section)
			}
		}
	}
	walk(isolated, "root")
	if !changed {
		return data, nil
	}
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(isolated); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

// 展开别名时复制节点，避免路径锚点的修改传播到凭据或其他非路径字段。
func expandMigrationAliases(node *yaml.Node) *yaml.Node {
	if node.Kind == yaml.AliasNode {
		copy := expandMigrationAliases(node.Alias)
		copy.HeadComment = node.HeadComment
		copy.LineComment = node.LineComment
		copy.FootComment = node.FootComment
		return copy
	}
	copy := *node
	copy.Anchor = ""
	copy.Content = make([]*yaml.Node, len(node.Content))
	for index, child := range node.Content {
		copy.Content[index] = expandMigrationAliases(child)
	}
	return &copy
}
