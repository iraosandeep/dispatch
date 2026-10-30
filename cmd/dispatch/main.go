// Command dispatch sends Telegram notifications from the CLI or an HTTP API.
package main

import (
	"os"
	"runtime/debug"

	"github.com/iraosandeep/dispatch/internal/cli"
)

// Set by -ldflags at release build time.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if version == "dev" {
		// `go install module@version` builds carry the module version.
		if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
			version = info.Main.Version
		}
	}
	os.Exit(cli.Main(cli.BuildInfo{Version: version, Commit: commit, Date: date}))
}
