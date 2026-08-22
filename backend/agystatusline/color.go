package agystatusline

// ANSI colour helpers.
const (
	ansiReset      = "\033[0m"
	ansiRed        = "\033[31m"
	ansiGreen      = "\033[32m"
	ansiYellow     = "\033[33m"
	ansiBlue       = "\033[34m"
	ansiMagenta    = "\033[35m"
	ansiCyan       = "\033[36m"
	ansiGray       = "\033[90m"
	ansiBrightGray = "\033[37m"
)

// colorPrint wraps the text with the specified ANSI color escape sequence
// and resets it at the end. If no color is provided, it returns the text as-is.
func colorPrint(color, text string) string {
	if color == "" {
		return text
	}
	return color + text + ansiReset
}
