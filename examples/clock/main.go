package main

import (
	"image"
	"log"
	"os"
	"os/signal"
	"time"

	"github.com/danielgatis/go-ssd1680/pkg/display"
	"github.com/danielgatis/go-ssd1680/pkg/text"

	"periph.io/x/conn/v3/gpio"
	"periph.io/x/conn/v3/gpio/gpioreg"
	"periph.io/x/conn/v3/spi/spireg"
	"periph.io/x/host/v3"
)

func main() {
	// Load all the drivers
	if _, err := host.Init(); err != nil {
		log.Fatal(err)
	}

	// Open a handle to the first available SPI bus
	bus, err := spireg.Open("")
	if err != nil {
		log.Fatal(err)
	}

	// Partial refreshes every second, a full one every 60 to clear ghosting
	dev, err := display.NewSSD1680SPI(bus, mustPin("GPIO25"), mustPin("GPIO17"), nil, mustPin("GPIO24"), &display.Options{
		Width:            122,
		Height:           250,
		Rotation:         display.Rotate90,
		FullRefreshEvery: 60,
		Power:            gpioreg.ByName("GPIO18"),
	})
	if err != nil {
		log.Fatal(err)
	}
	defer dev.Halt()

	textRenderer, err := text.NewRendererWithEmbeddedFont(&text.Config{
		Width:     250,
		Height:    122,
		LineCount: 4,
	})
	if err != nil {
		log.Fatal(err)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)

	tick := time.NewTicker(time.Second)
	defer tick.Stop()

	for {
		now := time.Now()
		textRenderer.SetTexts([]string{
			"",
			now.Format("15:04:05"),
			now.Format("Mon, 02 Jan 2006"),
		})

		if err := dev.Draw(textRenderer.Bounds(), textRenderer.Image(), image.Point{}); err != nil {
			log.Fatal(err)
		}

		select {
		case <-stop:
			return
		case <-tick.C:
		}
	}
}

func mustPin(name string) gpio.PinIO {
	p := gpioreg.ByName(name)
	if p == nil {
		log.Fatalf("%s not available", name)
	}

	return p
}
