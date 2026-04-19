# Config
[![License](https://img.shields.io/github/license/mashape/apistatus.svg)](https://github.com/catamat/config/blob/master/LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/catamat/config)](https://goreportcard.com/report/github.com/catamat/config)
[![Go Reference](https://pkg.go.dev/badge/github.com/catamat/config.svg)](https://pkg.go.dev/github.com/catamat/config)
[![Version](https://img.shields.io/github/tag/catamat/config.svg?color=blue&label=version)](https://github.com/catamat/config/releases)

Config is a package to read configurations from JSON file, environment and flags.

`FromEnv` and `FromFlags` support exported scalar fields, slices of supported scalar values or `encoding.TextUnmarshaler` values via `vsep`, and nested exported structs recursively. Map fields are not supported.

If an `env` or `flag` tag is omitted, the exported Go field name is used as fallback. Use `env:"-"` or `flag:"-"` to ignore a field.

`FromFlags` uses the standard library `flag` parser. Flag tags can be written with or without a leading `-`.
Repeated flags do not append to slices: the last provided flag value wins.

## Installation:
```
go get github.com/catamat/config@latest
```
## Example:
```golang
package main

import (
	"log"

	"github.com/catamat/config"
)

type configJSON struct {
	Word   string `json:"word"`
	Number int    `json:"number"`
	Check  bool   `json:"check"`
	Slice  []int  `json:"myslice"`
}

type configEnv struct {
	TmpDir      string `env:"TMPDIR"`
	HOME        string
	Shell       string `env:"SHELL"`
	User        string `env:"USER"`
	GoRoot      string `env:"GOROOT"`
	CgoCflags   string `env:"CGO_CFLAGS"`
	VscodePid   int    `env:"VSCODE_PID"`
	PipeLogging bool   `env:"PIPE_LOGGING"`
	Slice       []int  `env:"MY_SLICE" vsep:":"`
}

type configFlags struct {
	Word   string `flag:"word"`
	Number int    `flag:"number"`
	Check  bool   `flag:"check"`
	Slice  []int  `flag:"myslice" vsep:","`
}

func main() {
	/*
	    {
	        "word": "TestWord",
	        "number": 123456,
	        "check": true,
	        "myslice": [1,1,2,3,5,8]
	    }
	*/
	log.Println("JSON:")
	cfg1 := configJSON{}
	if err := config.FromJSON(&cfg1, "config.json"); err != nil {
		log.Fatal(err)
	}
	log.Println(cfg1)

	/*
	    os.Setenv("MY_SLICE", "111:222:333")
	*/
	log.Println("Env:")
	cfg2 := configEnv{}
	if err := config.FromEnv(&cfg2); err != nil {
		log.Fatal(err)
	}
	log.Println(cfg2)

	/*
	    ./example -word TestWord -number 123456 -check -myslice 1,1,2,3,5,8
	*/
	log.Println("Flags:")
	cfg3 := configFlags{}
	if err := config.FromFlags(&cfg3); err != nil {
		log.Fatal(err)
	}
	log.Println(cfg3)
}
```
