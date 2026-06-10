// agystatusline reads the JSON payload that the antigravity-cli (agy) pipes to
// a custom status line command via stdin, extracts the fields we care about,
// and prints a compact one-line status string to stdout.
//
// Usage – ~/.gemini/antigravity-cli/settings.json:
//
// {
//   "statusLine": {
//     "type":    "command",
//     "command": "/path/to/agystatusline",
//     "enabled": true
//   }
// }

package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/AgentDrasil/asgard/lib/agystatusline"
)

func main() {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agystatusline: reading stdin: %v\n", err)
		os.Exit(1)
	}

	line, _, err := agystatusline.Run(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agystatusline: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(line)

	if sessionID := os.Getenv("AW_SESSION_ID"); sessionID != "" {
		if err := os.MkdirAll("/tmp/agystatusline", 0755); err != nil {
			fmt.Fprintf(os.Stderr, "agystatusline: creating directory: %v\n", err)
		} else {
			filePath := filepath.Join("/tmp/agystatusline", sessionID+".json")
			if err := os.WriteFile(filePath, data, 0644); err != nil {
				fmt.Fprintf(os.Stderr, "agystatusline: writing statusline JSON: %v\n", err)
			}
		}
	}
}
