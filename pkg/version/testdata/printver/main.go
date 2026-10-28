package main

import (
	"fmt"

	"github.com/AgentDrasil/asgard/pkg/version"
)

func main() {
	fmt.Print(version.Get())
}
