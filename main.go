package main

import (
	"log"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/AnalyseDeCircuit/kyandb/internal/config"
	"github.com/AnalyseDeCircuit/kyandb/internal/engine/mysql"
	"github.com/AnalyseDeCircuit/kyandb/internal/engine/postgres"
	"github.com/AnalyseDeCircuit/kyandb/internal/engine/sqlite"
	"github.com/AnalyseDeCircuit/kyandb/internal/runtime"
	"github.com/AnalyseDeCircuit/kyandb/internal/secret"
	"github.com/AnalyseDeCircuit/kyandb/internal/ui/app"
)

func main() {
	cfgPath, err := config.DefaultPath()
	if err != nil {
		log.Fatal(err)
	}
	rt, err := runtime.New(
		config.NewStore(cfgPath),
		secret.NewKeychainStore("com.analysedecircuit.kyandb"),
		sqlite.NewDriver(),
		postgres.NewDriver(),
		mysql.NewDriver(),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = rt.Close() }()

	if snap := rt.Snapshot(); snap.Active >= 0 {
		rt.SelectConnection(snap.Active)
	}

	shell := app.NewFromRuntime(rt)
	mygo.App.WhenReady(func() {
		mygo.NewWindow(mygo.WindowOptions{
			Title:         "KyanDB",
			Width:         1200,
			Height:        760,
			MinWidth:      720,
			MinHeight:     480,
			StateKey:      "main",
			TitleBarStyle: mygo.TitleBarHidden,
			Content:       ui.View(shell.View),
		})
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
