package main

import (
	"encoding/json"
	"fmt"
	"iceyquicksave/internal/archive"
	"os"
)

func main() {
	var v map[string]any
	if e := archive.Read(os.Args[1], &v); e != nil {
		panic(e)
	}
	if len(os.Args) > 2 {
		b, e := json.MarshalIndent(v, "", " ")
		if e != nil {
			panic(e)
		}
		if e = os.WriteFile(os.Args[2], b, 0600); e != nil {
			panic(e)
		}
		return
	}
	w := v["World"].(map[string]any)
	for _, o := range w["Objects"].([]any) {
		x := o.(map[string]any)
		fmt.Printf("%s %v\n", x["Key"], x["Components"])
	}
}
