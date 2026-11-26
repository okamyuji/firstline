// Command firstline runs the ticket sales service and its maintenance subcommands.
package main

import (
	"os"

	"github.com/okamyuji/firstline/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Getenv, os.Stdin, os.Stdout, os.Stderr))
}
