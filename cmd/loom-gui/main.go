// loom-gui: Wails desktop app for Loom. Reads the same TOML config as the
// CLI; pass --config to override the path (default: ~/.loom/config.toml).
package main

import (
	"embed"
	"fmt"
	"os"
	"strings"

	"github.com/MatteoAdamo82/loom/internal/config"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

//go:embed all:frontend/dist
var assets embed.FS

// Version is stamped at build time via -ldflags "-X main.Version=...".
var Version = "dev"

func main() {
	cfgPath := os.Getenv("LOOM_CONFIG")
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--config" && i+1 < len(args):
			cfgPath = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--config="):
			cfgPath = strings.TrimPrefix(args[i], "--config=")
		}
	}
	if cfgPath == "" {
		cfgPath = config.DefaultPath()
	}

	app := NewApp(cfgPath)

	err := wails.Run(&options.App{
		Title:            "Loom",
		Width:            1280,
		Height:           820,
		MinWidth:         900,
		MinHeight:        600,
		BackgroundColour: &options.RGBA{R: 250, G: 250, B: 248, A: 1},
		AssetServer:      &assetserver.Options{Assets: assets},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		Bind:             []interface{}{app},
		Mac: &mac.Options{
			TitleBar:             mac.TitleBarHiddenInset(),
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			About: &mac.AboutInfo{
				Title:   "Loom",
				Message: "LLM memory: SQLite + BM25 + LLM, no embeddings.\nVersion " + Version,
			},
		},
	})

	if err != nil {
		fmt.Fprintln(os.Stderr, "loom-gui:", err)
		os.Exit(1)
	}
}
