// Command cursortest is a temporary helper for verifying cursor movement and
// clicking on the host. It is not part of the server build.
package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"kenosm/internal/desktop"
)

func main() {
	d, err := desktop.New(desktop.Config{Timeout: 5 * time.Second})
	if err != nil {
		fmt.Fprintln(os.Stderr, "desktop unavailable:", err)
		os.Exit(2)
	}
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: cursortest move|click X Y [button]")
		os.Exit(2)
	}
	x, _ := strconv.Atoi(os.Args[2])
	y, _ := strconv.Atoi(os.Args[3])
	switch os.Args[1] {
	case "move":
		err = d.MouseMove(x, y)
	case "click":
		btn := "left"
		if len(os.Args) > 4 {
			btn = os.Args[4]
		}
		err = d.Click(x, y, btn)
	default:
		fmt.Fprintln(os.Stderr, "unknown subcommand", os.Args[1])
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Println("ok")
}
