// Command migrate runs database migrations and exits: "migrate up" applies all pending ones,
// "migrate down" rolls back one.
package main

import (
	"os"

	"github.com/Prokud1n/ai-agents/internal"
)

func main() {
	application := internal.NewMigrationApplication(os.Args[1:])
	application.Run()
}
