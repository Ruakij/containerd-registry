// Command containerd-registry serves the OCI distribution API from the
// containerd of its node.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path"
)

var (
	// Set by the build process
	version = "dev"
)

func main() {
	configPath := flag.String("config", "/etc/containerd-registry/config.yml", "path to the config file")
	showVersion := flag.Bool("version", false, "show version")
	flag.Parse()

	if *showVersion {
		fmt.Println(path.Base(os.Args[0]), version)
		return
	}
	slog.Error("serving is not implemented", "config", *configPath)
	os.Exit(1)
}
