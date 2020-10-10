package main

import (
	"os"

	"github.com/ziyan/gatewaysshd/cli"
)

func main() {
	cli.Run(Version, Commit, os.Args)
}
