package main

import (
	"image"
	"log"
	"os"

	"github.com/danielgatis/go-ssd1680/pkg/display"
	"github.com/danielgatis/go-ssd1680/pkg/text"

	"periph.io/x/conn/v3/gpio"
	"periph.io/x/conn/v3/gpio/gpioreg"
	"periph.io/x/conn/v3/spi/spireg"
	"periph.io/x/host/v3"
)

func main() {
	// Get message from command line argument
	message := "Hello, e-paper!"
	if len(os.Args) > 1 {
		message = os.Args[1]
	}

	// Load all the drivers
	if _, err := host.Init(); err != nil {
		log.Fatal(err)
	}

	// Open a handle to the first available SPI bus
	bus, err := spireg.Open("")
	if err != nil {
		log.Fatal(err)
	}

	// Pins of the Waveshare 2.13inch e-Paper HAT
	dc := mustPin("GPIO25")
	rst := mustPin("GPIO17")
	busy := mustPin("GPIO24")
	pwr := gpioreg.ByName("GPIO18") // only on HATs that have it; harmless otherwise

	// Create SSD1680 display driver
	// Chip select is CE0 (GPIO8), driven by the SPI controller: pass nil.
	dev, err := display.NewSSD1680SPI(bus, dc, rst, nil, busy, &display.Options{
		Width:    122,
		Height:   250,
		Rotation: display.Rotate270,
		Power:    pwr,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer dev.Halt()

	// Create text renderer with embedded font
	textRenderer, err := text.NewRendererWithEmbeddedFont(&text.Config{
		Width:     250,
		Height:    122,
		LineCount: 4,
		Scale:     3, // the embedded font is 3x5 px
	})
	if err != nil {
		log.Fatal(err)
	}

	textRenderer.SetTexts([]string{
		message,
		"",
		"SSD1680 e-paper",
		"250 x 122",
	})

	// Draw the text: a full refresh, then the image stays with no power
	if err := dev.Draw(textRenderer.Bounds(), textRenderer.Image(), image.Point{}); err != nil {
		log.Fatal(err)
	}
}

func mustPin(name string) gpio.PinIO {
	p := gpioreg.ByName(name)
	if p == nil {
		log.Fatalf("%s not available", name)
	}

	return p
}
