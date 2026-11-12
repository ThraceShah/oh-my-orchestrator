package main

import (
	"fmt"
	"os"

	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(cli.ExitCode(err))
	}
}
