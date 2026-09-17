package main

import (
	"embed"
	"io/fs"
	"log"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed all:frontend/dist
var embedded embed.FS

func main() {
	assets, err := fs.Sub(embedded, "frontend/dist")
	if err != nil {
		log.Fatal(err)
	}
	if len(os.Args) > 1 && os.Args[1] == "--headless" {
		if err := runHeadless(assets, os.Args[2:]); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "--headless-peer" {
		if err := runPeerHeadless(assets, os.Args[2:]); err != nil {
			log.Fatal(err)
		}
		return
	}
	app := &App{assets: assets, window: wailsWindow{}}
	if err := app.loadPreferences(); err != nil {
		app.loadErr = err.Error()
	}
	err = wails.Run(&options.App{
		Title: "Share Me", Width: 960, Height: 680,
		MinWidth: 800, MinHeight: 560,
		AssetServer: &assetserver.Options{Assets: assets},
		OnStartup:   app.startup, OnShutdown: app.shutdown,
		OnDomReady: app.domReady, OnBeforeClose: app.beforeClose,
		StartHidden: app.prefs.StartMinimized && app.prefsErr == nil,
		Bind:        []interface{}{app},
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId:               "4959a90e-71a6-4b1b-981e-3a244a75cf8d",
			OnSecondInstanceLaunch: app.onSecondInstanceLaunch,
		},
		Windows: &windows.Options{
			Theme:           windows.SystemDefault,
			WindowClassName: "ShareMeWindow",
		},
	})
	if err != nil {
		log.Fatal(err)
	}
}
