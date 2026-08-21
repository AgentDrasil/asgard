package workflow

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/rs/zerolog/log"
)

// ResolveDefnFunc resolves a sub-workflow definition by name.
type ResolveDefnFunc func(name string) (*WorkflowDefinition, error)

// SubWorkflowRunner executes sub-workflow nodes (single run or fan-out).
type SubWorkflowRunner struct {
	resolveDefn ResolveDefnFunc
	engine      *Engine
	maxDepth    int
}

// NewSubWorkflowRunner creates a new SubWorkflowRunner.
func NewSubWorkflowRunner(resolve ResolveDefnFunc) *SubWorkflowRunner {
	return &SubWorkflowRunner{
		resolveDefn: resolve,
		maxDepth:    4,
	}
}

// SetEngine injects the workflow Engine for inline sub-workflow execution.
func (r *SubWorkflowRunner) SetEngine(engine *Engine) {
	r.engine = engine
}

// wfCallChainKey is the context key for tracking workflow invocation call chains.
type wfCallChainKey struct{}

// Supports reports whether the runner supports the given node type.
func (r *SubWorkflowRunner) Supports(t NodeType) bool {
	return t == NodeTypeWorkflow
}

// Run executes a sub-workflow node, supporting single execution or fan-out over items_file.
func (r *SubWorkflowRunner) Run(ctx context.Context, nctx *NodeContext) (*NodeResult, error) {
	node := nctx.Node
	if node.Workflow == "" {
		return nil, fmt.Errorf("node %s: workflow name is required", node.ID)
	}
	if r.engine == nil {
		return nil, fmt.Errorf("node %s: engine is not set on SubWorkflowRunner", node.ID)
	}
	if r.resolveDefn == nil {
		return nil, fmt.Errorf("node %s: resolveDefn is not configured", node.ID)
	}

	// 1. Call chain cycle detection and depth limit check
	var chain []string
	if val := ctx.Value(wfCallChainKey{}); val != nil {
		if c, ok := val.([]string); ok {
			chain = c
		}
	}

	for _, wName := range chain {
		if wName == node.Workflow {
			return nil, fmt.Errorf("workflow cycle detected: %s already in call chain %v", node.Workflow, chain)
		}
	}

	maxDepth := r.maxDepth
	if maxDepth <= 0 {
		maxDepth = 4
	}
	if len(chain) >= maxDepth {
		return nil, fmt.Errorf("workflow recursion depth limit exceeded (max %d)", maxDepth)
	}

	childCtx := context.WithValue(ctx, wfCallChainKey{}, append(append([]string(nil), chain...), node.Workflow))

	// 2. Resolve and validate sub-workflow definition
	subDefn, err := r.resolveDefn(node.Workflow)
	if err != nil {
		return nil, fmt.Errorf("node %s: resolving sub-workflow %q: %w", node.ID, node.Workflow, err)
	}
	if subDefn == nil {
		return nil, fmt.Errorf("node %s: sub-workflow %q definition is nil", node.ID, node.Workflow)
	}
	if err := subDefn.Validate(); err != nil {
		return nil, fmt.Errorf("node %s: sub-workflow %q validation failed: %w", node.ID, node.Workflow, err)
	}

	// Apply node timeout
	childCtx, cancel := withNodeTimeout(childCtx, node)
	defer cancel()

	// 3. Execution: Fan-out mode or Single-run mode
	if node.Fanout != nil {
		return r.runFanout(childCtx, nctx, node, subDefn)
	}
	return r.runSingle(childCtx, nctx, node, subDefn)
}

func (r *SubWorkflowRunner) runSingle(ctx context.Context, nctx *NodeContext, node *NodeSpec, subDefn *WorkflowDefinition) (*NodeResult, error) {
	prompt := nctx.Interpolate(node.Prompt)
	if prompt == "" {
		prompt = nctx.Input
	}

	childRC := RunContext{
		SessionID:         nctx.SessionID,
		RunDir:            nctx.RunDir,
		TmpDir:            nctx.TmpDir,
		Input:             prompt,
		AgentName:         "",
		WorkflowRunDirs:   nctx.WorkflowRunDirs,
		WorkflowMountDirs: nctx.WorkflowMountDirs,
		Inline:            true,
		Headless:          nctx.Headless,
		ParentRunID:       nctx.RunID,
		EmitEvent: func(ev WorkflowEvent) {
			if nctx.EventEmitter == nil {
				return
			}
			meta := make(map[string]any)
			for k, v := range ev.Metadata {
				meta[k] = v
			}
			meta["sub_node_id"] = ev.NodeID
			meta["sub_node_type"] = string(ev.NodeType)
			meta["sub_status"] = string(ev.Status)

			nctx.EventEmitter(WorkflowEvent{
				Type:      EventNodeStatusUpdate,
				NodeID:    node.ID,
				NodeType:  NodeTypeWorkflow,
				Status:    StatusRunning,
				Message:   ev.Message,
				EntryType: "fanout_progress",
				Metadata:  meta,
			})
		},
	}

	runRes, err := r.engine.Execute(ctx, subDefn, childRC)
	if err != nil {
		return &NodeResult{
			Status: StatusFailed,
			Error:  err,
		}, nil
	}

	if runRes.Status == RunStatusCompleted {
		// Collect output from leaf or settled nodes
		var lastOutput string
		for _, sn := range subDefn.Nodes {
			if res, ok := runRes.Nodes[sn.ID]; ok && res.Output != "" {
				lastOutput = res.Output
			}
		}
		res := &NodeResult{
			Status: StatusSucceeded,
			Output: lastOutput,
		}
		collectArtifact(nctx, node, res)
		return res, nil
	}

	return &NodeResult{
		Status: StatusFailed,
		Error:  runRes.Error,
	}, nil
}

type fanoutItemResult struct {
	itemIndex int
	item      string
	status    string
	output    string
	err       error
}

func (r *SubWorkflowRunner) runFanout(ctx context.Context, nctx *NodeContext, node *NodeSpec, subDefn *WorkflowDefinition) (*NodeResult, error) {
	fanout := node.Fanout
	itemsFilePath := nctx.Interpolate(fanout.ItemsFile)
	if !filepath.IsAbs(itemsFilePath) {
		itemsFilePath = filepath.Join(nctx.TmpDir, itemsFilePath)
	}

	var outputFilePath string
	if fanout.OutputFile != "" {
		outputFilePath = nctx.Interpolate(fanout.OutputFile)
		if !filepath.IsAbs(outputFilePath) {
			outputFilePath = filepath.Join(nctx.TmpDir, outputFilePath)
		}
	}

	// Read items file
	var lines []string
	fileExists := true
	file, err := os.Open(itemsFilePath)
	if err != nil {
		fileExists = false
	} else {
		defer func() { _ = file.Close() }()
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			trimmed := strings.TrimSpace(scanner.Text())
			if trimmed != "" {
				lines = append(lines, trimmed)
			}
		}
	}

	// Empty set handling
	if !fileExists || len(lines) == 0 {
		if outputFilePath != "" {
			if err := os.MkdirAll(filepath.Dir(outputFilePath), 0o755); err != nil {
				log.Warn().Err(err).Str("path", outputFilePath).Msg("creating empty output file dir failed")
			}
			if err := os.WriteFile(outputFilePath, []byte{}, 0o644); err != nil {
				log.Warn().Err(err).Str("path", outputFilePath).Msg("writing empty output file failed")
			}
		}
		if nctx.EventEmitter != nil {
			nctx.EventEmitter(WorkflowEvent{
				Type:      EventNodeStatusUpdate,
				NodeID:    node.ID,
				NodeType:  NodeTypeWorkflow,
				Status:    StatusRunning,
				EntryType: "activity",
				Metadata: map[string]any{
					"item_count":         0,
					"items_file_missing": !fileExists,
				},
				Message: "fanout: items list is empty",
			})
		}
		res := &NodeResult{
			Status: StatusSucceeded,
			Output: "",
		}
		collectArtifact(nctx, node, res)
		return res, nil
	}

	maxParallel := 3
	if fanout.MaxParallel != nil && *fanout.MaxParallel > 0 {
		maxParallel = *fanout.MaxParallel
	}

	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup
	var mu sync.Mutex
	itemResults := make([]fanoutItemResult, 0, len(lines))

	for i, line := range lines {
		itemIndex := i + 1
		itemVal := line

		wg.Add(1)
		go func(idx int, itm string) {
			defer wg.Done()

			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				mu.Lock()
				itemResults = append(itemResults, fanoutItemResult{
					itemIndex: idx,
					item:      itm,
					status:    string(StatusFailed),
					err:       ctx.Err(),
				})
				mu.Unlock()
				return
			}
			defer func() { <-sem }()

			if ctx.Err() != nil {
				mu.Lock()
				itemResults = append(itemResults, fanoutItemResult{
					itemIndex: idx,
					item:      itm,
					status:    string(StatusFailed),
					err:       ctx.Err(),
				})
				mu.Unlock()
				return
			}

			childRC := RunContext{
				SessionID:         nctx.SessionID,
				RunDir:            nctx.RunDir,
				TmpDir:            nctx.TmpDir,
				Input:             itm,
				AgentName:         "",
				WorkflowRunDirs:   nctx.WorkflowRunDirs,
				WorkflowMountDirs: nctx.WorkflowMountDirs,
				Inline:            true,
				Headless:          nctx.Headless,
				ParentRunID:       nctx.RunID,
				EmitEvent: func(ev WorkflowEvent) {
					if nctx.EventEmitter == nil {
						return
					}
					meta := make(map[string]any)
					for k, v := range ev.Metadata {
						meta[k] = v
					}
					meta["item_index"] = idx
					meta["sub_node_id"] = ev.NodeID
					meta["sub_node_type"] = string(ev.NodeType)
					meta["sub_status"] = string(ev.Status)

					nctx.EventEmitter(WorkflowEvent{
						Type:      EventNodeStatusUpdate,
						NodeID:    node.ID,
						NodeType:  NodeTypeWorkflow,
						Status:    StatusRunning,
						Message:   ev.Message,
						EntryType: "fanout_progress",
						Metadata:  meta,
					})
				},
			}

			runRes, runErr := r.engine.Execute(ctx, subDefn, childRC)
			var subOutput string
			subStatus := string(StatusSucceeded)
			var finalErr error

			if runErr != nil {
				subStatus = string(StatusFailed)
				finalErr = runErr
			} else if runRes != nil {
				if runRes.Status != RunStatusCompleted {
					subStatus = string(StatusFailed)
					finalErr = runRes.Error
					if finalErr == nil {
						finalErr = fmt.Errorf("sub-workflow item %d settled with status %s", idx, runRes.Status)
					}
				}
				for _, sn := range subDefn.Nodes {
					if nr, ok := runRes.Nodes[sn.ID]; ok && nr.Output != "" {
						subOutput = nr.Output
					}
				}
			}

			mu.Lock()
			itemResults = append(itemResults, fanoutItemResult{
				itemIndex: idx,
				item:      itm,
				status:    subStatus,
				output:    subOutput,
				err:       finalErr,
			})
			mu.Unlock()
		}(itemIndex, itemVal)
	}

	wg.Wait()

	// Sort results strictly by itemIndex ascending
	sort.Slice(itemResults, func(i, j int) bool {
		return itemResults[i].itemIndex < itemResults[j].itemIndex
	})

	hasFailure := false
	var jsonlBuilder strings.Builder
	succeededCount := 0
	failedCount := 0

	type jsonlEntry struct {
		ItemIndex int    `json:"item_index"`
		Item      string `json:"item"`
		Status    string `json:"status"`
		Output    string `json:"output"`
	}

	for _, ir := range itemResults {
		if ir.status != string(StatusSucceeded) {
			hasFailure = true
			failedCount++
		} else {
			succeededCount++
		}

		entry := jsonlEntry{
			ItemIndex: ir.itemIndex,
			Item:      ir.item,
			Status:    ir.status,
			Output:    ir.output,
		}
		data, _ := json.Marshal(entry)
		jsonlBuilder.Write(data)
		jsonlBuilder.WriteByte('\n')
	}

	aggregatedJSONL := jsonlBuilder.String()

	// Write OutputFile if configured
	if outputFilePath != "" {
		if err := os.MkdirAll(filepath.Dir(outputFilePath), 0o755); err != nil {
			log.Warn().Err(err).Str("path", outputFilePath).Msg("creating fanout output dir failed")
		}
		if err := os.WriteFile(outputFilePath, []byte(aggregatedJSONL), 0o644); err != nil {
			log.Warn().Err(err).Str("path", outputFilePath).Msg("writing fanout output file failed")
		}
	}

	if nctx.EventEmitter != nil {
		nctx.EventEmitter(WorkflowEvent{
			Type:      EventNodeStatusUpdate,
			NodeID:    node.ID,
			NodeType:  NodeTypeWorkflow,
			Status:    StatusRunning,
			EntryType: "activity",
			Metadata: map[string]any{
				"total_items":     len(lines),
				"succeeded_items": succeededCount,
				"failed_items":    failedCount,
			},
			Message: fmt.Sprintf("fanout completed: %d total, %d succeeded, %d failed", len(lines), succeededCount, failedCount),
		})
	}

	res := &NodeResult{
		Output: aggregatedJSONL,
	}
	collectArtifact(nctx, node, res)

	if ctx.Err() != nil {
		res.Status = StatusFailed
		res.Error = ctx.Err()
		return res, nil
	}

	if hasFailure {
		res.Status = StatusFailed
		res.Error = fmt.Errorf("fanout sub-workflow had %d failed item(s)", failedCount)
		return res, nil
	}

	res.Status = StatusSucceeded
	return res, nil
}
