package main

import (
	"fmt"
	"os"
	"syscall"

	"ocq/internal/cli"
)

func main() {
	syscall.Umask(0077)
	if err := cli.Run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "ocq:", err)
		os.Exit(1)
	}
}
