package main

import (
    "fmt"
    "os"

    "github.com/rafalmasiarek/porta/internal/cli"
)

var version = "dev"

func main() {
    if err := cli.Run(os.Args[1:], version); err != nil {
        fmt.Fprintln(os.Stderr, "porta error:", err)
        os.Exit(1)
    }
}
