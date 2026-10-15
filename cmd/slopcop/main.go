// Command slopcop checks a working-tree change through the hosted SlopCop
// check API. There is no local analyzer.
package main

import (
	"os"

	"github.com/BrokkAi/slopcop/internal/cli"
)

// version is overridden at build time with
// -ldflags "-X main.version=<version>".
var version = "dev"

func main() {
	os.Exit(cli.Execute(os.Args[1:], version))
}
