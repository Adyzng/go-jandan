package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/adyzng/jandan/core"
	log "gopkg.in/clog.v1"
)

var (
	saveDir   = flag.String("destination", "pic", "destination folder to save the picture")
	maxPages  = flag.Int("max", 0, "the max pages to crawl")
	startPage = flag.Int("page", 1, "start page to crawl")
)

func init() {
	if err := log.New(log.CONSOLE, log.ConsoleConfig{
		Level:      log.INFO,
		BufferSize: 100,
	}); err != nil {
		fmt.Printf("Fail to create new console logger, error %+v.\n", err)
		os.Exit(1)
	}

	if err := log.New(log.FILE, log.FileConfig{
		Level:      log.TRACE,
		Filename:   "log\\jandan.log",
		BufferSize: 100,
	}); err != nil {
		fmt.Printf("Failed to create file logger, error %+v.\n", err)
		os.Exit(1)
	}
}

func main() {

	flag.Parse()
	log.Info("Crawl start")
	defer log.Shutdown()

	dest := *saveDir
	if abs := filepath.IsAbs(dest); !abs {
		dest, _ = filepath.Abs(dest)
	}
	// make destination folder if not exists
	if err := os.MkdirAll(dest, os.ModePerm); err != nil && !os.IsExist(err) {
		log.Fatal(2, "Failed to create destination folder %+v.", *saveDir)
	}

	log.Info("Destination %s, StartPage: %d, MaxPages: %d.", dest, *startPage, *maxPages)
	jd := core.NewJanDan(core.Config{
		Destination: dest,
		StartPage:   int32(*startPage),
		MaxPages:    int32(*maxPages),
		BaseURL:     "http://jandan.net",
	})

	jd.Run()
}
