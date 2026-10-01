package main

import (
	"fmt"
	"os"
)

// Version is set at build time with -ldflags "-X main.Version=...".
var Version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "version":
		fmt.Println(Version)
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: depotd <command> [flags]

commands:
  version          print the version
  hub              run the gate hub (web app + API + reading) on this computer
  export-dataset   write verified photos and labels for model training
  bench            re-read verified moves with an engine and report accuracy`)
}
