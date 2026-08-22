package agystatusline

import (
	"fmt"
	"strings"
)

type stateInfo struct {
	name   string
	nfIcon string
	emoji  string
	color  string
}

var (
	stateMap = map[string]stateInfo{
		"idle":           {"Idle", "", "🟢", ansiGreen},
		"thinking":       {"Hmm", "", "🤯", ansiCyan},
		"working":        {"Gen", "󰜎", "📝", ansiBlue},
		"tool_use":       {"Tool", "󱁤", "🛠️", ansiMagenta},
		"authenticating": {"Auth", "", "🔐", ansiGray},
		"initializing":   {"Init", "󱦟 ", "🌱", ansiGray},
		"error":          {"Err", "", "❌", ansiRed},
		"reviewing":      {"PTAL", "󰈈", "👀", ansiYellow},
		"default":        {"Unknown", "", "❓", ansiRed},
	}
)

const (
	stateMaxLength         = 10
	progressBarMaxLen      = 22
	progressBarMinLen      = 5
	multiLineModeThreshold = 40
)

func renderState(p Payload, mode iconMode) (string, int) {
	info := stateMap["default"]
	if v, ok := stateMap[p.AgentState]; ok {
		info = v
	} else {
		info.name = p.AgentState
		info.color = ansiGreen // Default custom states to green
	}
	switch mode {
	case nf:
		if len(info.name)+2 > stateMaxLength {
			return colorPrint(info.color, info.name), len(info.name)
		}
		return colorPrint(info.color, info.nfIcon+" "+info.name), 2 + len(info.name)
	case emoji:
		if len(info.name)+3 > stateMaxLength {
			return info.name, len(info.name)
		}
		return fmt.Sprintf("%s %s", info.emoji, info.name), 3 + len(info.name) // emoji is double width
	default:
		// never reached
		panic("unreachable")
	}
}

func renderProgressBar(p Payload, mode iconMode, size int) (string, int) {
	if size <= 0 {
		return "", 0
	}

	usedPct := p.ContextWindow.UsedPercentage
	if usedPct < 0 {
		usedPct = 0
	} else if usedPct > 100 {
		usedPct = 100
	}

	switch mode {
	case nf:
		if size < 2 {
			return "", 0
		}
		numBlocks := size - 2
		filledCount := int((usedPct/100.0)*float64(numBlocks) + 0.5)
		if filledCount < 0 {
			filledCount = 0
		} else if filledCount > numBlocks {
			filledCount = numBlocks
		}
		emptyCount := numBlocks - filledCount
		res := "[" + strings.Repeat("█", filledCount) + strings.Repeat("▒", emptyCount) + "]"
		return res, size

	case emoji:
		numBlocks := size / 2
		if numBlocks <= 0 {
			return "", 0
		}
		filledCount := int((usedPct/100.0)*float64(numBlocks) + 0.5)
		if filledCount < 0 {
			filledCount = 0
		} else if filledCount > numBlocks {
			filledCount = numBlocks
		}
		emptyCount := numBlocks - filledCount

		var filled string
		remPct := p.ContextWindow.RemainingPercentage
		switch {
		case remPct >= 80:
			filled = "🟦"
		case remPct >= 50:
			filled = "🟨"
		default:
			filled = "🟥"
		}

		res := strings.Repeat(filled, filledCount) + strings.Repeat("⬛", emptyCount)
		return res, numBlocks * 2

	default:
		return "", 0
	}
}

func renderPercentage(p Payload) (string, int) {
	color := remainingColor(p.ContextWindow.RemainingPercentage)
	raw := fmt.Sprintf("%.0f%%", p.ContextWindow.UsedPercentage)
	return colorPrint(color, raw), len(raw)
}

func renderUsage(p Payload) (string, int) {
	raw := fmt.Sprintf("(%s/%s)", formatTokens(p.ContextWindow.TotalInputTokens), formatTokens(p.ContextWindow.ContextWindowSize))
	return raw, len(raw)
}

func renderModel(p Payload) (string, int) {
	modelName := p.Model.DisplayName
	if modelName == "" {
		modelName = "Unknown"
	} else {
		parts := strings.SplitN(modelName, " ", 2)
		if len(parts) == 2 {
			modelName = parts[1]
		}
	}

	return modelName, len(modelName)
}

func renderTaskAndSubAgent(p Payload, mode iconMode) (string, int) {
	activeTasks := p.TaskCount
	activeSubagents := 0

	for _, s := range p.Subagents {
		if s.Status != "idle" {
			activeSubagents++
		}
	}

	taskStr := fmt.Sprintf("%d", activeTasks)
	agentStr := fmt.Sprintf("%d", activeSubagents)

	var parts []string
	length := 0

	switch mode {
	case nf:
		if activeTasks > 0 {
			parts = append(parts, fmt.Sprintf("  %d", activeTasks))
			length += 3 + len(taskStr)
		}
		if activeSubagents > 0 {
			parts = append(parts, fmt.Sprintf("  %d", activeSubagents))
			length += 3 + len(agentStr)
		}
	case emoji:
		if activeTasks > 0 {
			parts = append(parts, fmt.Sprintf("📋%d", activeTasks))
			length += 2 + len(taskStr)
		}
		if activeSubagents > 0 {
			parts = append(parts, fmt.Sprintf("🕵%d", activeSubagents))
			length += 2 + len(agentStr)
		}
	default:
		if activeTasks > 0 {
			parts = append(parts, fmt.Sprintf("T: %d", activeTasks))
			length += 3 + len(taskStr)
		}
		if activeSubagents > 0 {
			parts = append(parts, fmt.Sprintf("A: %d", activeSubagents))
			length += 3 + len(agentStr)
		}
	}

	if len(parts) == 0 {
		return "", 0
	}

	res := strings.Join(parts, "•")
	if len(parts) > 1 {
		length += 1
	}
	return res, length
}

// renderMultiLine formats the status line over 2 or 3 lines when space is constrained.
// First line holds state, percentage, and optionally usage (if it fits).
// Second/third lines hold the model name and tasks/subagents.
func renderMultiLine(stateStr string, stateSize int, pctStr string, pctSize int, usageStr string, usageSize int, modelStr string, modelSize int, taskSubStr string, taskSubSize int, W int) string {
	// First line: [state] [percentage] [usage]
	var line1 string
	// Check if both percentage and usage fit with 1 space separators
	if stateSize+1+pctSize+1+usageSize <= W {
		line1 = stateStr + " " + pctStr + " " + usageStr
	} else {
		// Fallback to only state and percentage
		line1 = stateStr + " " + pctStr
	}

	// Subsequent lines: model name and tasks/subagents
	var lines []string
	lines = append(lines, line1)

	// Wrap model and task/subagent output in bright gray
	grayModel := colorPrint(ansiBrightGray, modelStr)
	if taskSubStr == "" {
		lines = append(lines, grayModel)
	} else {
		grayTaskSub := colorPrint(ansiBrightGray, taskSubStr)
		// Try to put model name and active tasks/subagents on the same line (Line 2)
		if modelSize+1+taskSubSize <= W {
			lines = append(lines, grayModel+" "+grayTaskSub)
		} else {
			// If they don't fit together, split them: model on Line 2, tasks/subagents on Line 3
			lines = append(lines, grayModel)
			lines = append(lines, grayTaskSub)
		}
	}

	return strings.Join(lines, "\n")
}

// renderIcon coordinates status line rendering. It selects either a single-line
// or multi-line layout based on terminal width and layout priorities.
func renderIcon(p Payload, mode iconMode) string {
	// 1. Render all individual components
	stateStr, stateSize := renderState(p, mode)
	pctStr, pctSize := renderPercentage(p)
	usageStr, usageSize := renderUsage(p)
	modelStr, modelSize := renderModel(p)
	taskSubStr, taskSubSize := renderTaskAndSubAgent(p, mode)

	// Calculate a padded column width for the state. This keeps components
	// like the progress bar from shifting visual positions when the state changes.
	stateColWidth := stateMaxLength + 1
	if stateColWidth < stateSize+1 {
		stateColWidth = stateSize + 1
	}

	W := p.TerminalWidth
	if W <= 0 {
		W = 80 // Default to a standard width if terminal width is unspecified
	}

	// 2. If terminal width is below the threshold, bypass single-line logic
	if W < multiLineModeThreshold {
		return renderMultiLine(stateStr, stateSize, pctStr, pctSize, usageStr, usageSize, modelStr, modelSize, taskSubStr, taskSubSize, W)
	}

	// Define priority configurations to search.
	// Priorities:
	// - State, Percentage, Model, and Tasks/Subagents (if not empty) are mandatory.
	// - Tasks/Subagents: try Line 1 first, fallback to Line 2 (aligned right).
	// - Usage is preferred over the Progress Bar.
	type config struct {
		taskSubOnLine1 bool
		hasUsage       bool
		hasProgressBar bool
	}

	configs := []config{
		{true, true, true},    // 1st Priority: Everything on Line 1
		{true, true, false},   // 2nd Priority: Everything on Line 1 except Progress Bar
		{false, true, true},   // 3rd Priority: Tasks/Subagents on Line 2; has Progress Bar
		{false, true, false},  // 4th Priority: Tasks/Subagents on Line 2; no Progress Bar
		{true, false, true},   // 5th Priority: Everything on Line 1 except Usage
		{true, false, false},  // 6th Priority: Everything on Line 1 except Usage and Progress Bar
		{false, false, true},  // 7th Priority: Tasks/Subagents on Line 2; no Usage; has Progress Bar
		{false, false, false}, // 8th Priority: Minimal layout (no Usage, no Progress Bar, Tasks/Subagents on Line 2)
	}

	var bestConfig *config
	var bestPBLen int

	// Find the highest-priority configuration that fits within the terminal width
	for _, cfg := range configs {
		// If there are no tasks/subagents, skip configs that place them on Line 2
		if taskSubSize == 0 && !cfg.taskSubOnLine1 {
			continue
		}

		// Calculate required space on Line 1 (excluding progress bar)
		neededWithoutPB := stateColWidth + pctSize
		if cfg.hasUsage {
			neededWithoutPB += 1 + usageSize
		}

		// Space for right-aligned components
		rightSize := modelSize
		if cfg.taskSubOnLine1 && taskSubSize > 0 {
			rightSize += 1 + taskSubSize
		}

		neededWithoutPB += 1 + rightSize // Include 1 space separator between left and right columns

		if cfg.hasProgressBar {
			leftover := W - neededWithoutPB
			maxPBLen := leftover - 1 // 1 extra space to separate progress bar from percentage
			if maxPBLen > progressBarMaxLen {
				maxPBLen = progressBarMaxLen
			}
			// Progress bar is only displayed if it meets the minimum length
			if maxPBLen >= progressBarMinLen {
				cfgCopy := cfg
				bestConfig = &cfgCopy
				bestPBLen = maxPBLen
				break
			}
		} else {
			if neededWithoutPB <= W {
				cfgCopy := cfg
				bestConfig = &cfgCopy
				bestPBLen = 0
				break
			}
		}
	}

	// Fallback to multi-line mode if no single-line configuration fits
	if bestConfig == nil {
		return renderMultiLine(stateStr, stateSize, pctStr, pctSize, usageStr, usageSize, modelStr, modelSize, taskSubStr, taskSubSize, W)
	}

	// 3. Assemble the selected single-line layout
	// Left column: [State (padded)] [Progress Bar] [Percentage] [Usage]
	leftStr := stateStr + strings.Repeat(" ", stateColWidth-stateSize)
	leftSize := stateColWidth

	if bestConfig.hasProgressBar && bestPBLen > 0 {
		pbStr, _ := renderProgressBar(p, mode, bestPBLen)
		leftStr += pbStr + " "
		leftSize += bestPBLen + 1
	}

	leftStr += pctStr
	leftSize += pctSize

	if bestConfig.hasUsage {
		leftStr += " " + usageStr
		leftSize += 1 + usageSize
	}

	// Right column: [Model] [Tasks/Subagents]
	// Wrap model and tasks/subagents in bright gray color
	rightStr := colorPrint(ansiBrightGray, modelStr)
	rightSize := modelSize

	if bestConfig.taskSubOnLine1 && taskSubSize > 0 {
		rightStr += " " + colorPrint(ansiBrightGray, taskSubStr)
		rightSize += 1 + taskSubSize
	}

	// Pad between left and right columns to align right column to the right edge
	middlePad := W - leftSize - rightSize
	if middlePad < 0 {
		middlePad = 0
	}

	line1 := leftStr + strings.Repeat(" ", middlePad) + rightStr

	// If tasks/subagents were pushed to the second line, align them right
	if !bestConfig.taskSubOnLine1 && taskSubSize > 0 {
		line2Spaces := W - taskSubSize
		if line2Spaces < 0 {
			line2Spaces = 0
		}
		line2 := strings.Repeat(" ", line2Spaces) + colorPrint(ansiBrightGray, taskSubStr)
		return line1 + "\n" + line2
	}

	return line1
}
