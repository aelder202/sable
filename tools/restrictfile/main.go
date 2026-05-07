package main

import (
	"fmt"
	"os"

	"github.com/aelder202/sable/internal/securefile"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: restrictfile <path> [path...]")
		os.Exit(2)
	}
	for _, path := range os.Args[1:] {
		if err := securefile.Restrict(path); err != nil {
			fmt.Fprintf(os.Stderr, "restrict %s: %v\n", path, err)
			os.Exit(1)
		}
	}
}
