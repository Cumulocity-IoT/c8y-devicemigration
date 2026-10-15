// Command c8y-devicemigration migrates a device hierarchy between Cumulocity
// tenants, using the go-c8y v2 library rather than the go-c8y-cli binary.
//
// docs/behaviour-contract.md records what it does, down to which of the
// library's helpful defaults had to be undone to get there.
package main

import (
	"os"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
}
