// Build/install tooling is also Go. No PowerShell, C#, or hand-written C is shipped.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"iceyquicksave/internal/peproxy"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

type Manifest struct {
	Version            string
	Files              map[string]string
	GameAssemblySHA256 string
}

func must(e error) {
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func digest(path string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		return "", e
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func cp(src, dst string) error {
	b, e := os.ReadFile(src)
	if e != nil {
		return e
	}
	tmp := dst + ".tmp"
	if e = os.WriteFile(tmp, b, 0755); e != nil {
		return e
	}
	return os.Rename(tmp, dst)
}
func run(env []string, args ...string) error {
	cmd := exec.Command("go", args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
func main() {
	game := flag.String("game", `G:\SteamLibrary\steamapps\common\ICEY`, "ICEY root")
	action := flag.String("action", "build", "build, install, uninstall, verify")
	flag.Parse()
	root, e := os.Getwd()
	must(e)
	if *action == "build" {
		must(os.MkdirAll("dist", 0755))
		cc := filepath.Join(root, "tools", "winlibs", "mingw64", "bin", "gcc.exe")
		if _, e = os.Stat(cc); e != nil {
			must(fmt.Errorf("compiler missing: %s (see tools/README.md)", cc))
		}
		env := []string{"CGO_ENABLED=1", "GOOS=windows", "GOARCH=amd64", "CC=" + cc, "PATH=" + filepath.Dir(cc) + string(os.PathListSeparator) + os.Getenv("PATH")}
		must(run(env, "build", "-trimpath", "-buildmode=c-shared", "-ldflags=-s -w", "-o", "dist/version.dll", "./cmd/payload"))
		must(run([]string{"CGO_ENABLED=0", "GOOS=windows", "GOARCH=amd64"}, "build", "-trimpath", "-ldflags=-s -w -H=windowsgui", "-o", "dist/ICEYQuickSave.exe", "./cmd/iceyqs"))
		system := filepath.Join(os.Getenv("SystemRoot"), "System32", "version.dll")
		must(cp(system, filepath.Join("dist", "iceyqs_system_version.dll")))
		must(peproxy.Forward(filepath.Join("dist", "version.dll"), system, "iceyqs_system_version"))
		exports, e := peproxy.Exports(filepath.Join("dist", "version.dll"))
		must(e)
		fmt.Printf("Built Go proxy and native picker; %d system exports forwarded.\n", len(exports))
		return
	}
	manifestPath := filepath.Join(*game, "iceyqs-install.json")
	var existing Manifest
	b, readErr := os.ReadFile(manifestPath)
	if readErr == nil {
		must(json.Unmarshal(b, &existing))
	}
	names := []string{"version.dll", "iceyqs_system_version.dll", "ICEYQuickSave.exe"}
	if *action == "install" {
		if _, e = os.Stat(filepath.Join(*game, "ICEY.exe")); e != nil {
			must(e)
		}
		// First check every destination, so an unrelated proxy is never overwritten.
		for _, n := range names {
			p := filepath.Join(*game, n)
			if _, e = os.Stat(p); e == nil {
				h, e := digest(p)
				must(e)
				if existing.Files[n] != h {
					must(fmt.Errorf("refusing to replace unowned/modified file: %s", p))
				}
			} else if !os.IsNotExist(e) {
				must(e)
			}
		}
		next := Manifest{Version: "0.1.0-experimental", Files: map[string]string{}}
		next.GameAssemblySHA256, e = digest(filepath.Join(*game, "ICEY_Data", "Managed", "Assembly-CSharp.dll"))
		must(e)
		for _, n := range names {
			h, e := digest(filepath.Join("dist", n))
			must(e)
			next.Files[n] = h
		}
		for _, n := range names {
			must(cp(filepath.Join("dist", n), filepath.Join(*game, n)))
		}
		must(os.MkdirAll(filepath.Join(*game, "savedata"), 0755))
		b, e = json.MarshalIndent(next, "", "  ")
		must(e)
		must(os.WriteFile(manifestPath, b, 0644))
		fmt.Println("Installed experimental version. Original ICEY.exe and game assemblies untouched.")
		return
	}
	if *action == "uninstall" || *action == "verify" {
		must(readErr)
		for _, n := range names {
			expected := existing.Files[n]
			if expected == "" {
				must(fmt.Errorf("invalid install manifest: %s", n))
			}
			h, e := digest(filepath.Join(*game, n))
			must(e)
			if h != expected {
				must(fmt.Errorf("modified installed file: %s", n))
			}
		}
		if *action == "verify" {
			fmt.Println("Installed files verified.")
			return
		}
		for _, n := range names {
			must(os.Remove(filepath.Join(*game, n)))
		}
		must(os.Remove(manifestPath))
		fmt.Println("Removed owned binaries. Savedata preserved.")
		return
	}
	must(fmt.Errorf("unknown action %q", *action))
}
