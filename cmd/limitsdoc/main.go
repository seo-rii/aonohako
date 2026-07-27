package main

import (
	"flag"
	"fmt"
	"os"

	"aonohako/internal/limitdoc"
)

func main() {
	writePath := flag.String("write", "", "write generated Markdown to this path instead of stdout")
	flag.Parse()

	body := []byte(limitdoc.Markdown())
	if *writePath == "" {
		if _, err := os.Stdout.Write(body); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := os.WriteFile(*writePath, body, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
