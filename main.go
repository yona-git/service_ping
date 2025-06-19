package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"ping-monitor/api"
	"ping-monitor/config"
	"ping-monitor/front"
	"ping-monitor/models"
	"ping-monitor/ping"
	"sync"
	"syscall"
	"time"

	"github.com/go-ini/ini"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

const (
	pingInterval = 5 * time.Second
	pingTimeout  = 10 * time.Second
)

func main() {
	e := echo.New()
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())
	e.Use(middleware.CORSWithConfig(middleware.CORSConfig{
		AllowOrigins: []string{"*"},
		AllowMethods: []string{http.MethodGet, http.MethodHead, http.MethodPut, http.MethodPatch, http.MethodPost, http.MethodDelete},
	}))

	frontPath, err := front.GetFrontPath()
	if err != nil {
		log.Fatalf("Error getting frontend path: %v", err)
	}

	fs := http.FileServer(http.Dir(frontPath))
	e.GET("/*", echo.WrapHandler(http.StripPrefix("/", fs)))

	servers := []models.Server{}
	var mu sync.Mutex

	configFile, err := config.GetConfigPath()
	if err != nil {
		panic(err)
	}

	newServers, err := config.LoadConfig(configFile)
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}
	servers = newServers

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go ping.MonitorServers(&servers, pingInterval, pingTimeout, &mu, ctx)
	api.RegisterHandlers(e, &servers, &mu)

	ctf, err := ini.Load(configFile)
	if err != nil {
		log.Printf("Failed to load ini config file: %v", err)
	}

	webport := ctf.Section("settings").Key("webport").MustString("8888")
	addr := fmt.Sprintf(":%s", webport)
	log.Printf("Server address: %s", addr)

	go func() {
		if err := e.Start(addr); err != nil && err != http.ErrServerClosed {
			e.Logger.Fatalf("Shutting down the server: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down server...")
	e.Close()
	log.Println("Server gracefully stopped")
}