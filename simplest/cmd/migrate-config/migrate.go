package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Standard reasoning effort mapping to thinkingLevelMap.
// Standard canonical levels: off, minimal, low, medium, high, xhigh, max.
var standardThinkingLevels = []string{
	"off",
	"minimal",
	"low",
	"medium",
	"high",
	"xhigh",
	"max",
}

// MigrateConfigBytes inspects YAML data and updates it to meet M1 metadata standards:
// - sets type: "chat" if missing on models
// - migrates reasoningEffort to thinkingLevelMap if thinkingLevelMap is missing
// - injects standard inputLimits for models that support "image" input if missing
// It preserves existing comments, formatting, and key order using yaml.Node.
// Returns migrated bytes, whether changes occurred, and any error.
func MigrateConfigBytes(data []byte) ([]byte, bool, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, false, fmt.Errorf("failed to parse yaml: %w", err)
	}

	if len(root.Content) == 0 {
		return data, false, nil
	}

	docNode := root.Content[0]
	if docNode.Kind != yaml.MappingNode {
		return data, false, nil
	}

	var modelsNode *yaml.Node
	for i := 0; i < len(docNode.Content)-1; i += 2 {
		keyNode := docNode.Content[i]
		valNode := docNode.Content[i+1]
		if keyNode.Value == "models" {
			if valNode.Kind == yaml.SequenceNode {
				modelsNode = valNode
				break
			}
		}
	}

	if modelsNode == nil || len(modelsNode.Content) == 0 {
		return data, false, nil
	}

	changed := false

	for _, modelNode := range modelsNode.Content {
		if modelNode.Kind != yaml.MappingNode {
			continue
		}

		var (
			modelID          string
			hasType          = false
			hasThinkingMap   = false
			reasoningEfforts []string
			supportsImage    = false
			hasInputLimits   = false
		)

		for i := 0; i < len(modelNode.Content)-1; i += 2 {
			kNode := modelNode.Content[i]
			vNode := modelNode.Content[i+1]

			switch kNode.Value {
			case "id":
				modelID = vNode.Value
			case "type":
				hasType = true
			case "thinkingLevelMap":
				hasThinkingMap = true
			case "reasoningEffort", "reasoning_effort":
				if vNode.Kind == yaml.SequenceNode {
					for _, item := range vNode.Content {
						reasoningEfforts = append(reasoningEfforts, item.Value)
					}
				}
			case "input":
				if vNode.Kind == yaml.SequenceNode {
					for _, item := range vNode.Content {
						if strings.ToLower(item.Value) == "image" {
							supportsImage = true
						}
					}
				}
			case "inputLimits":
				hasInputLimits = true
			}
		}

		// 1. Missing type -> set type: "chat"
		if !hasType {
			typeKey := &yaml.Node{
				Kind:  yaml.ScalarNode,
				Tag:   "!!str",
				Value: "type",
			}
			typeVal := &yaml.Node{
				Kind:  yaml.ScalarNode,
				Tag:   "!!str",
				Value: "chat",
			}
			modelNode.Content = append(modelNode.Content, typeKey, typeVal)
			changed = true
		}

		// 2. Has reasoningEffort and missing thinkingLevelMap
		if !hasThinkingMap && len(reasoningEfforts) > 0 {
			effortSet := make(map[string]bool)
			for _, effort := range reasoningEfforts {
				effortSet[strings.ToLower(effort)] = true
			}

			mapNode := &yaml.Node{
				Kind: yaml.MappingNode,
				Tag:  "!!map",
			}

			// Warn if legacy reasoningEfforts contain non-standard levels that cannot be automatically mapped
			for _, effort := range reasoningEfforts {
				norm := strings.ToLower(effort)
				found := false
				for _, lvl := range standardThinkingLevels {
					if lvl == norm {
						found = true
						break
					}
				}
				if !found {
					fmt.Fprintf(os.Stderr, "Warning: model %s specifies non-standard reasoningEffort %q which will be omitted from thinkingLevelMap; manual review recommended\n", modelID, effort)
				}
			}

			for _, lvl := range standardThinkingLevels {
				kNode := &yaml.Node{
					Kind:  yaml.ScalarNode,
					Tag:   "!!str",
					Value: lvl,
				}
				var vNode *yaml.Node
				if effortSet[lvl] {
					vNode = &yaml.Node{
						Kind:  yaml.ScalarNode,
						Tag:   "!!str",
						Value: lvl,
					}
				} else if lvl != "off" {
					// Levels higher than off that are absent in legacy reasoningEfforts are explicitly set to null
					vNode = &yaml.Node{
						Kind:  yaml.ScalarNode,
						Tag:   "!!null",
						Value: "null",
					}
				} else {
					// "off" is intentionally skipped if not explicitly in reasoningEfforts,
					// matching the default behavior where reasoning off is the baseline.
					continue
				}
				mapNode.Content = append(mapNode.Content, kNode, vNode)
			}

			mapKey := &yaml.Node{
				Kind:  yaml.ScalarNode,
				Tag:   "!!str",
				Value: "thinkingLevelMap",
			}
			modelNode.Content = append(modelNode.Content, mapKey, mapNode)
			changed = true
		}

		// 3. Supports image and missing inputLimits
		if supportsImage && !hasInputLimits {
			// Construct standard protection template:
			// inputLimits:
			//   images:
			//     resize:
			//       maxWidth: 2000
			//       maxHeight: 2000
			//       maxBytes: 4718592
			resizeMap := &yaml.Node{
				Kind: yaml.MappingNode,
				Tag:  "!!map",
				Content: []*yaml.Node{
					{Kind: yaml.ScalarNode, Tag: "!!str", Value: "maxWidth"},
					{Kind: yaml.ScalarNode, Tag: "!!int", Value: "2000"},
					{Kind: yaml.ScalarNode, Tag: "!!str", Value: "maxHeight"},
					{Kind: yaml.ScalarNode, Tag: "!!int", Value: "2000"},
					{Kind: yaml.ScalarNode, Tag: "!!str", Value: "maxBytes"},
					{Kind: yaml.ScalarNode, Tag: "!!int", Value: "4718592"},
				},
			}

			imagesMap := &yaml.Node{
				Kind: yaml.MappingNode,
				Tag:  "!!map",
				Content: []*yaml.Node{
					{Kind: yaml.ScalarNode, Tag: "!!str", Value: "resize"},
					resizeMap,
				},
			}

			limitsMap := &yaml.Node{
				Kind: yaml.MappingNode,
				Tag:  "!!map",
				Content: []*yaml.Node{
					{Kind: yaml.ScalarNode, Tag: "!!str", Value: "images"},
					imagesMap,
				},
			}

			keyNode := &yaml.Node{
				Kind:  yaml.ScalarNode,
				Tag:   "!!str",
				Value: "inputLimits",
			}
			modelNode.Content = append(modelNode.Content, keyNode, limitsMap)
			changed = true
		}
	}

	if !changed {
		return data, false, nil
	}

	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(&root); err != nil {
		return nil, false, fmt.Errorf("failed to encode migrated yaml: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, false, fmt.Errorf("failed to finalize yaml encoding: %w", err)
	}

	return buf.Bytes(), true, nil
}

// MigrateConfigFile reads the target configuration file, calls MigrateConfigBytes,
// and if not dry-run, atomically writes the updated file.
func MigrateConfigFile(path string, dryRun bool) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("failed to read config file %s: %w", path, err)
	}

	newData, changed, err := MigrateConfigBytes(data)
	if err != nil {
		return false, fmt.Errorf("failed to migrate config: %w", err)
	}

	if !changed {
		return false, nil
	}

	if dryRun {
		fmt.Printf("--- Dry run preview for %s ---\n", path)
		fmt.Println(string(newData))
		return true, nil
	}

	// Atomic write via temp file in the same directory
	dir := filepath.Dir(path)
	tmpFile, err := os.CreateTemp(dir, ".tmp-config-*")
	if err != nil {
		return false, fmt.Errorf("failed to create temp file in %s: %w", dir, err)
	}
	tmpName := tmpFile.Name()

	// Ensure cleanup if an error occurs before rename
	cleanupNeeded := true
	defer func() {
		if cleanupNeeded {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmpFile.Write(newData); err != nil {
		_ = tmpFile.Close()
		return false, fmt.Errorf("failed to write temp config file: %w", err)
	}

	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return false, fmt.Errorf("failed to sync temp config file: %w", err)
	}

	if err := tmpFile.Close(); err != nil {
		return false, fmt.Errorf("failed to close temp config file: %w", err)
	}

	// Preserve original file permissions if possible
	if fi, statErr := os.Stat(path); statErr == nil {
		_ = os.Chmod(tmpName, fi.Mode())
	}

	if err := os.Rename(tmpName, path); err != nil {
		return false, fmt.Errorf("failed to atomically replace config file %s: %w", path, err)
	}

	cleanupNeeded = false
	return true, nil
}
