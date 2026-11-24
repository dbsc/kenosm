// Command server runs the kenosm HTTP server (API + gateway + web UI).
package main

import "kenosm/internal/app"

func main() {
	app.Run()
}
