// Command kb loads brand materials into the knowledge base and searches it:
//
//	kb ingest <file or folder> [-kind brand]
//	kb search "<query>" [-limit 5]
//	kb delete <source>
package main

import (
	"os"

	"github.com/Prokud1n/ai-agents/internal"
)

func main() {
	os.Exit(internal.RunKB(os.Args[1:]))
}
