package main

import (
	"debug/pe"
	"fmt"
	"iceyquicksave/internal/peproxy"
	"os"
)

func main() {
	for _, path := range os.Args[1:] {
		f, err := pe.Open(path)
		if err != nil {
			panic(err)
		}
		fmt.Printf("%s machine=%x\n", path, f.Machine)
		imports, err := f.ImportedSymbols()
		if err != nil {
			panic(err)
		}
		for _, s := range imports {
			fmt.Println(s)
		}
		f.Close()
		exports, e := peproxy.Exports(path)
		if e == nil {
			for _, x := range exports {
				fmt.Printf("EXPORT %d %s\n", x.Ordinal, x.Name)
			}
		}
	}
}
