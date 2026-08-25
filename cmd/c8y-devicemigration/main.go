// Command c8y-devicemigration migrates a device hierarchy between Cumulocity
// tenants, using the go-c8y v2 library rather than the go-c8y-cli binary.
//
// It mirrors the behaviour of the bash extension in commands/ 1:1. See
// docs/go-port/BEHAVIOUR-CONTRACT.md for what that means concretely, and
// docs/go-port/PLAN.md for how the two relate.
package main

import (
	"os"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
}
