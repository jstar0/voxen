package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/jstar0/voxen/internal/voxen"
)

func main() {
	if err := voxen.NewCommand().Execute(); err != nil {
		var exit *voxen.ExitError
		if errors.As(err, &exit) {
			os.Exit(exit.Code)
		}
		fmt.Fprintln(os.Stderr, "voxen:", err)
		os.Exit(1)
	}
}
