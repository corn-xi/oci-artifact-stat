// Command oci-artifact-stat reports the actual highest-published version of
// every image in a Harbor project.
package main

import (
	"os"

	"github.com/corn-xi/oci-artifact-stat/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
