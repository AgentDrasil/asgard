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
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/AgentDrasil/asgard/agystatusline"
)

func main() {
	icon := flag.String("icon", "", "style of icons: nf (nerdfont) or emoji")
	flag.Parse()

	if *icon != "" && *icon != "nf" && *icon != "emoji" {
		fmt.Fprintf(os.Stderr, "agystatusline: invalid --icon value %q. Must be 'nf', 'emoji', or empty.\n", *icon)
		os.Exit(1)
	}

	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agystatusline: reading stdin: %v\n", err)
		os.Exit(1)
	}

	line, _, err := agystatusline.Run(data, *icon)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agystatusline: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(line)
}
