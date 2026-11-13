// Command migrate applies database migrations and exits.
package main

import (
	"github.com/Prokud1n/ai-agents/internal"
)

func main() {
	application := internal.NewMigrationApplication()
	application.Run()
}
