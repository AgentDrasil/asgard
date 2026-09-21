package fakebash

import (
	"fmt"
)

// ShouldShortCircuit determines whether the command output qualifies for immediate
// fast-path pass-through without any compression or evaluation.
// Both successful and failed commands (any exitCode) short-circuit if totalBytes < 256 and lineCount <= 5.
func ShouldShortCircuit(totalBytes int64, lineCount int, exitCode int) bool {
	return totalBytes < 256 && lineCount <= 5
}

// formatBytes returns a human-readable byte string such as "420B", "15KB", "2MB".
func formatBytes(b int64) string {
	const unit = 1000
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	units := []string{"KB", "MB", "GB", "TB"}
	val := float64(b) / float64(div)
	if val == float64(int64(val)) {
		return fmt.Sprintf("%.0f%s", val, units[exp])
	}
	return fmt.Sprintf("%.1f%s", val, units[exp])
}

// FormatFooter formats the standard fakebash compressed output footer.
func FormatFooter(origBytes, newBytes int64, cmdID string) string {
	return fmt.Sprintf("[fakebash: output compressed from %s -> %s. To view raw output, run: show-output %s (or 'show-output' for the latest)]",
		formatBytes(origBytes), formatBytes(newBytes), cmdID)
}

// FormatDropOnSuccess formats the summary prompt and footer for commands whose verbose output was dropped on success.
func FormatDropOnSuccess(origBytes int64, cmdID string) string {
	lead := "[fakebash: command succeeded with exit code 0. Verbose output truncated by sandbox]"
	footer := FormatFooter(origBytes, int64(len(lead)), cmdID)
	return lead + "\n" + footer
}
