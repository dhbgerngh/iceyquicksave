package main

import "C" // Go's c-shared build mode; no handwritten C or C# payload.
import "iceyquicksave/internal/agent"

func init() { go agent.Run() }
func main() {}
