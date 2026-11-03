package main

import (
	_ "embed"
	"flag"
	"fmt"
	"golang.org/x/term"
	"log"
	"os"
	"runtime"
	"runtime/debug"
	"syscall"

	td2 "github.com/Silk-Nodes/tenderduty/v2/td2"
)

//go:embed example-config.yml
var defaultConfig []byte

// version is set by the Docker build, where no git checkout is present. Builds from a checkout get it from the
// tag that Go stamps into the binary.
var version string

// versionString is what "tenderduty version" prints: the release tag, the commit it was built from, and the Go
// toolchain, so a bug report says exactly what is running.
func versionString() string {
	v, commit, when := version, "", ""
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v == "" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			v = bi.Main.Version
		}
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				commit = s.Value
				if len(commit) > 12 {
					commit = commit[:12]
				}
			case "vcs.time":
				when = s.Value
			}
		}
	}
	if v == "" {
		v = "unknown"
	}
	out := "tenderduty " + v
	if commit != "" {
		out += "\ncommit  " + commit
		if when != "" {
			out += " (" + when + ")"
		}
	}
	return out + "\nbuilt with " + runtime.Version() + " for " + runtime.GOOS + "/" + runtime.GOARCH
}

func main() {
	var configFile, chainConfigDirectory, stateFile, encryptedFile, password string
	var dumpConfig, encryptConfig, decryptConfig, showVersion bool
	flag.StringVar(&configFile, "f", "config.yml", "configuration file to use, can also be set with the ENV var 'CONFIG'")
	flag.StringVar(&encryptedFile, "encrypted-config", "config.yml.asc", "encrypted config file, only valid with -encrypt or -decrypt flag")
	flag.StringVar(&password, "password", "", "password to use for encrypting/decrypting the config, if unset will prompt, also can use ENV var 'PASSWORD'")
	flag.StringVar(&stateFile, "state", ".tenderduty-state.json", "file for storing state between restarts")
	flag.StringVar(&chainConfigDirectory, "cc", "chains.d", "directory containing additional chain specific configurations")
	flag.BoolVar(&dumpConfig, "example-config", false, "print the an example config.yml and exit")
	flag.BoolVar(&encryptConfig, "encrypt", false, "encrypt the file specified by -f to -encrypted-config")
	flag.BoolVar(&decryptConfig, "decrypt", false, "decrypt the file specified by -encrypted-config to -f")
	flag.BoolVar(&showVersion, "version", false, "print the version and exit, same as 'tenderduty version'")
	flag.Parse()

	if showVersion || flag.Arg(0) == "version" {
		fmt.Println(versionString())
		os.Exit(0)
	}

	if dumpConfig {
		fmt.Println(string(defaultConfig))
		os.Exit(0)
	}

	if configFile == "config.yml" && os.Getenv("CONFIG") != "" {
		configFile = os.Getenv("CONFIG")
	}

	if os.Getenv("PASSWORD") != "" {
		password = os.Getenv("PASSWORD")
	}

	if encryptConfig || decryptConfig {
		if password == "" {
			fmt.Print("Please enter the encryption password: ")
			pass, err := term.ReadPassword(int(syscall.Stdin))
			if err != nil {
				log.Fatal(err)
			}
			fmt.Println("")
			password = string(pass)
			pass = nil
		}

		if e := td2.EncryptedConfig(configFile, encryptedFile, password, decryptConfig); e != nil {
			log.Fatalln(e)
		}
		os.Exit(0)
	}

	err := td2.Run(configFile, stateFile, chainConfigDirectory, &password)
	if err != nil {
		log.Println(err.Error(), "... exiting.") // #nosec G706 -- the error text comes from local files and flags, not from a request
	}
}
