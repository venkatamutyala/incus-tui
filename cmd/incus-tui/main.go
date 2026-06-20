// Command incus-tui is a keyboard-driven terminal cockpit for local Incus VMs.
package main

import (
	"flag"
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	xincus "github.com/venkatamutyala/incus-tui/internal/incus"
	"github.com/venkatamutyala/incus-tui/internal/tui"
)

// version is overridden at build time via -ldflags "-X main.version=…".
var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.BoolVar(showVersion, "v", false, "print version and exit (shorthand)")
	flag.Parse()
	if *showVersion {
		fmt.Println("incus-tui", version)
		return
	}

	client, err := xincus.Connect()
	if err != nil {
		fmt.Fprintln(os.Stderr, "incus-tui: cannot connect to the local Incus daemon:")
		fmt.Fprintln(os.Stderr, "  "+err.Error())
		fmt.Fprintln(os.Stderr, "Is Incus running? Start it (e.g. sudo systemctl start incus), and make sure your")
		fmt.Fprintln(os.Stderr, "user can reach its socket — join the 'incus-admin' group, or set INCUS_SOCKET.")
		os.Exit(1)
	}
	defer client.Disconnect()

	p := tea.NewProgram(tui.New(client))
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "incus-tui:", err)
		os.Exit(1)
	}
}
