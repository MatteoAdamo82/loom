// Loom CLI entry point.
package main

import (
	"fmt"
	"os"

	"github.com/MatteoAdamo82/loom/cmd/loom/cli"
)

func main() {
	if err := cli.Root().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "loom:", err)
		os.Exit(1)
	}
}
