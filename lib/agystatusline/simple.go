package agystatusline

import (
	"fmt"
	"strings"
)

func renderSimple(p Payload) string {
	color := remainingColor(p.ContextWindow.RemainingPercentage)

	modelName := p.Model.DisplayName
	if modelName == "" {
		modelName = p.Model.ID
	}

	stateUpper := strings.ToUpper(p.AgentState)

	res := fmt.Sprintf("%s | %s/%s (%s%.0f%%%s)",
		stateUpper,
		formatTokens(p.ContextWindow.TotalInputTokens),
		formatTokens(p.ContextWindow.ContextWindowSize),
		color,
		p.ContextWindow.RemainingPercentage,
		ansiReset,
	)
	if modelName != "" {
		res += fmt.Sprintf(" | %s", modelName)
	}
	taskAndSubagent, _ := renderTaskAndSubAgent(p, simple)
	if taskAndSubagent != "" {
		res += " | " + taskAndSubagent
	}
	return res
}
