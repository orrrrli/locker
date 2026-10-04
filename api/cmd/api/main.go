package main

import (
	"fmt"
	"os"
)

func main() {
	if _, err := loadConfig(os.Getenv); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
