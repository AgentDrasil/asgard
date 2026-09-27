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

// MigrateAndSplitResult holds the result of splitting a config into separate key, providers, and models files.
type MigrateAndSplitResult struct {
	KeyBytes       []byte
	ProvidersBytes []byte
	ModelsBytes    []byte
	Changed        bool
}

// MigrateAndSplitConfigBytes performs M1 migrations on models and splits the configuration into:
// 1. ProvidersBytes: providers.yaml (public metadata: api, baseUrl, and non-secret headers)
// 2. KeyBytes: key.yaml (secrets: apiKey, docToolAllowedDirs)
// 3. ModelsBytes: models.yaml (migrated models)
func MigrateAndSplitConfigBytes(data []byte) (*MigrateAndSplitResult, error) {
	// First run the standard M1 migrations
	migratedData, _, err := MigrateConfigBytes(data)
	if err != nil {
		return nil, fmt.Errorf("migrate config: %w", err)
	}

	var root yaml.Node
	if err := yaml.Unmarshal(migratedData, &root); err != nil {
		return nil, fmt.Errorf("failed to parse yaml: %w", err)
	}

	if len(root.Content) == 0 {
		return &MigrateAndSplitResult{KeyBytes: migratedData, Changed: false}, nil
	}

	docNode := root.Content[0]
	if docNode.Kind != yaml.MappingNode {
		return &MigrateAndSplitResult{KeyBytes: migratedData, Changed: false}, nil
	}

	var (
		modelsNode   *yaml.Node
		modelsIdx    = -1
		providersIdx = -1
	)

	for i := 0; i < len(docNode.Content)-1; i += 2 {
		keyNode := docNode.Content[i]
		valNode := docNode.Content[i+1]
		if keyNode.Value == "models" && valNode.Kind == yaml.SequenceNode {
			modelsNode = valNode
			modelsIdx = i
		} else if keyNode.Value == "providers" && valNode.Kind == yaml.MappingNode {
			providersIdx = i
		}
	}

	// Build models document if models exist
	var modelsBytes []byte
	if modelsNode != nil && len(modelsNode.Content) > 0 {
		modelsRoot := &yaml.Node{
			Kind: yaml.DocumentNode,
			Content: []*yaml.Node{
				{
					Kind: yaml.MappingNode,
					Tag:  "!!map",
					Content: []*yaml.Node{
						{
							Kind:  yaml.ScalarNode,
							Tag:   "!!str",
							Value: "models",
						},
						modelsNode,
					},
				},
			},
		}

		var modelsBuf bytes.Buffer
		modelsEncoder := yaml.NewEncoder(&modelsBuf)
		modelsEncoder.SetIndent(2)
		if err := modelsEncoder.Encode(modelsRoot); err != nil {
			return nil, fmt.Errorf("failed to encode models yaml: %w", err)
		}
		if err := modelsEncoder.Close(); err != nil {
			return nil, fmt.Errorf("failed to finalize models yaml encoding: %w", err)
		}
		modelsBytes = modelsBuf.Bytes()
	}

	// Split providers into public providers.yaml and sensitive key.yaml
	var (
		providersBytes []byte
		keyRootNode    *yaml.Node
	)

	if providersIdx != -1 {
		providersValNode := docNode.Content[providersIdx+1]

		publicProvidersMap := &yaml.Node{
			Kind: yaml.MappingNode,
			Tag:  "!!map",
		}
		keyProvidersMap := &yaml.Node{
			Kind: yaml.MappingNode,
			Tag:  "!!map",
		}

		// Iterate through each provider in docNode
		for p := 0; p < len(providersValNode.Content)-1; p += 2 {
			pNameNode := providersValNode.Content[p]
			pDefNode := providersValNode.Content[p+1]

			if pDefNode.Kind != yaml.MappingNode {
				continue
			}

			pubDefNode := &yaml.Node{
				Kind: yaml.MappingNode,
				Tag:  "!!map",
			}
			keyDefNode := &yaml.Node{
				Kind: yaml.MappingNode,
				Tag:  "!!map",
			}

			for f := 0; f < len(pDefNode.Content)-1; f += 2 {
				fKey := pDefNode.Content[f]
				fVal := pDefNode.Content[f+1]

				switch fKey.Value {
				case "apiKey", "key":
					// Sensitive -> key.yaml
					kKey := &yaml.Node{
						Kind:  yaml.ScalarNode,
						Tag:   "!!str",
						Value: "apiKey",
					}
					keyDefNode.Content = append(keyDefNode.Content, kKey, fVal)
				case "api", "baseUrl":
					// Public provider settings -> providers.yaml
					pubDefNode.Content = append(pubDefNode.Content, fKey, fVal)
				case "headers":
					// Default headers -> providers.yaml
					pubDefNode.Content = append(pubDefNode.Content, fKey, fVal)
				default:
					// Other provider fields default to providers.yaml
					pubDefNode.Content = append(pubDefNode.Content, fKey, fVal)
				}
			}

			if len(pubDefNode.Content) > 0 {
				publicProvidersMap.Content = append(publicProvidersMap.Content, pNameNode, pubDefNode)
			}
			if len(keyDefNode.Content) > 0 {
				keyProvidersMap.Content = append(keyProvidersMap.Content, pNameNode, keyDefNode)
			}
		}

		if len(publicProvidersMap.Content) > 0 {
			pubRoot := &yaml.Node{
				Kind: yaml.DocumentNode,
				Content: []*yaml.Node{
					{
						Kind: yaml.MappingNode,
						Tag:  "!!map",
						Content: []*yaml.Node{
							{
								Kind:  yaml.ScalarNode,
								Tag:   "!!str",
								Value: "providers",
							},
							publicProvidersMap,
						},
					},
				},
			}
			var pubBuf bytes.Buffer
			pubEncoder := yaml.NewEncoder(&pubBuf)
			pubEncoder.SetIndent(2)
			if err := pubEncoder.Encode(pubRoot); err != nil {
				return nil, fmt.Errorf("failed to encode providers yaml: %w", err)
			}
			if err := pubEncoder.Close(); err != nil {
				return nil, fmt.Errorf("failed to finalize providers yaml encoding: %w", err)
			}
			providersBytes = pubBuf.Bytes()
		}

		// Replace providers in docNode with keyProvidersMap
		providersValNode.Content = keyProvidersMap.Content
	}

	// Remove models key and value from docNode if present
	if modelsIdx != -1 {
		docNode.Content = append(docNode.Content[:modelsIdx], docNode.Content[modelsIdx+2:]...)
	}

	keyRootNode = &root
	var keyBuf bytes.Buffer
	keyEncoder := yaml.NewEncoder(&keyBuf)
	keyEncoder.SetIndent(2)
	if err := keyEncoder.Encode(keyRootNode); err != nil {
		return nil, fmt.Errorf("failed to encode key yaml: %w", err)
	}
	if err := keyEncoder.Close(); err != nil {
		return nil, fmt.Errorf("failed to finalize key yaml encoding: %w", err)
	}

	changed := len(modelsBytes) > 0 || len(providersBytes) > 0
	return &MigrateAndSplitResult{
		KeyBytes:       keyBuf.Bytes(),
		ProvidersBytes: providersBytes,
		ModelsBytes:    modelsBytes,
		Changed:        changed,
	}, nil
}

// MigrateAndSplitConfigFile splits models into modelsPath, providers into providersPath,
// and keys into keyPath, migrating model schemas along the way.
// If inputPath is different from keyPath, providersPath, and modelsPath, it removes inputPath.
func MigrateAndSplitConfigFile(inputPath, keyPath, providersPath, modelsPath string, dryRun bool) (bool, error) {
	data, err := os.ReadFile(inputPath)
	if err != nil {
		return false, fmt.Errorf("failed to read config file %s: %w", inputPath, err)
	}

	res, err := MigrateAndSplitConfigBytes(data)
	if err != nil {
		return false, err
	}

	if !res.Changed {
		return false, nil
	}

	dir := filepath.Dir(inputPath)
	if keyPath == "" {
		keyPath = filepath.Join(dir, "key.yaml")
	}
	if providersPath == "" {
		providersPath = filepath.Join(dir, "providers.yaml")
	}
	if modelsPath == "" {
		modelsPath = filepath.Join(dir, "models.yaml")
	}

	if dryRun {
		fmt.Printf("--- Dry run preview for key: %s ---\n", keyPath)
		fmt.Println(string(res.KeyBytes))
		if len(res.ProvidersBytes) > 0 {
			fmt.Printf("--- Dry run preview for providers: %s ---\n", providersPath)
			fmt.Println(string(res.ProvidersBytes))
		}
		if len(res.ModelsBytes) > 0 {
			fmt.Printf("--- Dry run preview for models: %s ---\n", modelsPath)
			fmt.Println(string(res.ModelsBytes))
		}
		return true, nil
	}

	// Write models file atomically first if present
	if len(res.ModelsBytes) > 0 {
		if err := atomicWriteFile(modelsPath, res.ModelsBytes); err != nil {
			return false, fmt.Errorf("write models file: %w", err)
		}
	}

	// Write providers file atomically if present
	if len(res.ProvidersBytes) > 0 {
		if err := atomicWriteFile(providersPath, res.ProvidersBytes); err != nil {
			return false, fmt.Errorf("write providers file: %w", err)
		}
	}

	// Write key file atomically
	if len(res.KeyBytes) > 0 {
		if err := atomicWriteFile(keyPath, res.KeyBytes); err != nil {
			return false, fmt.Errorf("write key file: %w", err)
		}
	}

	// If inputPath was different from keyPath, providersPath, modelsPath, remove obsolete inputPath
	absInput, _ := filepath.Abs(inputPath)
	absKey, _ := filepath.Abs(keyPath)
	absProv, _ := filepath.Abs(providersPath)
	absModels, _ := filepath.Abs(modelsPath)
	if absInput != absKey && absInput != absProv && absInput != absModels {
		if err := os.Remove(inputPath); err != nil && !os.IsNotExist(err) {
			return false, fmt.Errorf("remove old config file %s: %w", inputPath, err)
		}
	}

	return true, nil
}

func atomicWriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmpFile, err := os.CreateTemp(dir, ".tmp-config-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file in %s: %w", dir, err)
	}
	tmpName := tmpFile.Name()

	cleanupNeeded := true
	defer func() {
		if cleanupNeeded {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("failed to write temp file: %w", err)
	}

	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("failed to sync temp file: %w", err)
	}

	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	if fi, statErr := os.Stat(path); statErr == nil {
		_ = os.Chmod(tmpName, fi.Mode())
	}

	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("failed to atomically replace file %s: %w", path, err)
	}

	cleanupNeeded = false
	return nil
}
