// Command csvlocalhost opens delimited text files as a table at
// http://csv.localhost/.
//
// It is what the file association runs (`csvlocalhost open <file>`), and it
// doubles as a small CLI for the background server.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/boyukbas/csv.localhost/internal/server"
)

const usage = `csvlocalhost: CSV and other delimited files as a table at http://csv.localhost/

Usage:
  csvlocalhost open [--delim D] [--no-header] [--host H] <file>
                                  Open a file. Starts the server if needed.
  csvlocalhost home               Open the list of recently viewed files.
  csvlocalhost serve [--port N] [--host H] [--idle MIN]
                                  Run the server in the foreground.
  csvlocalhost status             Where the server is listening, and since when.
  csvlocalhost stop               Shut the server down now.
`

func main() {
	server.AttachParentConsole()
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "csvlocalhost:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		fmt.Print(usage)
		return nil
	}
	cmd, rest := args[0], args[1:]

	// A bare path (e.g. a file dropped onto the .exe) means "open it".
	if !strings.HasPrefix(cmd, "-") && !isCommand(cmd) {
		cmd, rest = "open", args
	}

	switch cmd {
	case "open":
		fs := flag.NewFlagSet("open", flag.ContinueOnError)
		delim := fs.String("delim", "", `delimiter: "," ";" "tab" "|" ":" (default: detect)`)
		noHeader := fs.Bool("no-header", false, "treat the first row as data")
		host := fs.String("host", "", "host name to use in the viewer's address")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return fmt.Errorf("open takes exactly one file")
		}
		abs, err := filepath.Abs(fs.Arg(0))
		if err != nil {
			return err
		}
		opts := server.ParseOptions{Delimiter: *delim}
		if *noHeader {
			opts.Header = "no"
		}
		return server.Open(abs, *host, opts)

	case "home":
		return server.OpenHome()

	case "serve":
		fs := flag.NewFlagSet("serve", flag.ContinueOnError)
		port := fs.Int("port", server.DefaultPort, "preferred port")
		host := fs.String("host", server.DefaultHost, "host name to serve")
		idle := fs.Int("idle", server.DefaultIdleMinutes, "minutes to linger with no open viewers (0 = never stop)")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		return server.Serve(server.Config{PreferredPort: *port, Host: *host, IdleMinutes: *idle})

	case "status":
		return server.Status(os.Stdout)

	case "stop":
		return server.Stop(os.Stdout)

	case "help", "-h", "--help", "/?":
		fmt.Print(usage)
		return nil
	}
	return fmt.Errorf("unknown command %q (run without arguments for help)", cmd)
}

func isCommand(s string) bool {
	switch s {
	case "open", "home", "serve", "status", "stop", "help":
		return true
	}
	return false
}
