# Config
[![License](https://img.shields.io/github/license/mashape/apistatus.svg)](https://github.com/catamat/config/blob/master/LICENSE)
[![Build Status](https://travis-ci.org/catamat/config.svg?branch=master)](https://travis-ci.org/catamat/config)
[![Go Report Card](https://goreportcard.com/badge/github.com/catamat/config)](https://goreportcard.com/report/github.com/catamat/config)
[![Go Reference](https://pkg.go.dev/badge/github.com/catamat/config.svg)](https://pkg.go.dev/github.com/catamat/config)
[![Version](https://img.shields.io/github/tag/catamat/config.svg?color=blue&label=version)](https://github.com/catamat/config/releases)

Config is a simple package to read configurations from JSON file, environment and flags.

## Installation:
```
go get -u github.com/catamat/config
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
	Word   string `flag:"-word"`
	Number int    `flag:"-number"`
	Check  bool   `flag:"-check"`
	Slice  []int  `flag:"-myslice" vsep:","`
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
	config.FromJSON(&cfg1, "config.json")
	log.Println(cfg1)

	/*
	    os.Setenv("MY_SLICE", "111:222:333")
	*/
	log.Println("Env:")
	cfg2 := configEnv{}
	config.FromEnv(&cfg2)
	log.Println(cfg2)

	/*
	    ./example -word=TestWord -number=123456 -check=true -myslice=1,1,2,3,5,8
	*/
	log.Println("Flags:")
	cfg3 := configFlags{}
	config.FromFlags(&cfg3)
	log.Println(cfg3)
}
```