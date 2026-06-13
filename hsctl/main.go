// Command hsctl is the control tool for the homeserver stack. It is the engine the
// web UI (hsctl ui) sits on, and a CLI for the same actions: configure + generate
// .env files, start/stop the stack, fetch the CA cert, and run backups.
package main

import (
	"fmt"
	"os"
)

const version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	args := os.Args[2:]
	var err error
	switch os.Args[1] {
	case "setup":
		err = cmdSetup(args)
	case "up":
		err = cmdUp(args)
	case "down":
		err = cmdDown(args)
	case "status":
		err = cmdStatus(args)
	case "get-ca":
		err = cmdGetCA(args)
	case "backup":
		err = cmdBackup(args)
	case "ui":
		err = cmdUI(args)
	case "install":
		err = cmdInstall(args)
	case "version", "-v", "--version":
		fmt.Println("hsctl", version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintln(os.Stderr, "unknown command:", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `hsctl — homeserver control

usage: hsctl <command> [flags]

  setup      configure + generate .env files (interactive, or --yes for defaults)
  up         start the stack in order (services -> caddy)
  down       stop the stack (--volumes also deletes data)
  status     show container status
  get-ca     write caddy-root-ca.crt for installing on devices
  backup     run or configure backups (restic)
  ui         serve the web dashboard (default :8088)
  install    install the dashboard as a systemd service (auto-start on boot)
  version    print version
`)
}
