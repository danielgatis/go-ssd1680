# SSD1680 Go Driver

[![Go Report Card](https://goreportcard.com/badge/github.com/danielgatis/go-ssd1680?style=flat-square)](https://goreportcard.com/report/github.com/danielgatis/go-ssd1680)
[![License MIT](https://img.shields.io/badge/license-MIT-blue.svg)](https://raw.githubusercontent.com/danielgatis/go-ssd1680/master/LICENSE)
[![Go Doc](https://img.shields.io/badge/godoc-reference-blue.svg?style=flat-square)](https://godoc.org/github.com/danielgatis/go-ssd1680)
[![Release](https://img.shields.io/github/release/danielgatis/go-ssd1680.svg?style=flat-square)](https://github.com/danielgatis/go-ssd1680/releases/latest)

A Go library for controlling SSD1680 black and white e-paper displays with text rendering capabilities.

https://www.waveshare.com/2.13inch-e-paper-hat.htm

## Features

- **SSD1680 Display Driver**: Full support for SSD1680 e-paper displays via SPI
- **Full and Partial Refresh**: ~2s full refresh, ~0.3s partial refresh with no flashing
- **Rotation**: Native portrait or landscape, either way up
- **Low Power**: Deep sleep and optional power pin; the image stays with no power at all
- **Text Rendering**: BDF font support with embedded font option
- **Examples**: Complete examples showing different usage patterns

Tested on the Waveshare 2.13inch e-Paper HAT **V4** (250x122). Older revisions
(V2, V3) use other controllers or waveforms and are not supported.

## Installation

```bash
go get github.com/danielgatis/go-ssd1680
```

## Quick Start

```go
package main

import (
    "image"
    "log"

    "github.com/danielgatis/go-ssd1680/pkg/display"
    "github.com/danielgatis/go-ssd1680/pkg/text"

    "periph.io/x/conn/v3/gpio/gpioreg"
    "periph.io/x/conn/v3/spi/spireg"
    "periph.io/x/host/v3"
)

func main() {
    // Initialize periph.io
    host.Init()

    // Open SPI bus
    bus, _ := spireg.Open("")

    // Configure GPIO pins
    dc := gpioreg.ByName("GPIO25")
    rst := gpioreg.ByName("GPIO17")
    busy := gpioreg.ByName("GPIO24")

    // Create display. Chip select is CE0, driven by the SPI controller, so cs
    // is nil; pass a gpio.PinOut only when CS is wired to a plain GPIO.
    dev, _ := display.NewSSD1680SPI(bus, dc, rst, nil, busy, &display.Options{
        Width:    122,
        Height:   250,
        Rotation: display.Rotate270, // landscape: 250x122
        Power:    gpioreg.ByName("GPIO18"),
    })
    defer dev.Halt()

    // Create text renderer with embedded font
    textRenderer, _ := text.NewRendererWithEmbeddedFont(&text.Config{
        Width:     250,
        Height:    122,
        LineCount: 4,
        Scale:     3, // the embedded font is 3x5 px
    })

    // Set text and display
    textRenderer.SetTexts([]string{
        "Hello World!",
        "SSD1680 Display",
        "Go Library",
    })

    if err := dev.Draw(textRenderer.Bounds(), textRenderer.Image(), image.Point{}); err != nil {
        log.Fatal(err)
    }
}
```

### Wiring

The HAT plugs straight onto the 40-pin header:

| HAT  | Raspberry Pi      |
|------|-------------------|
| DIN  | GPIO10 (MOSI)     |
| CLK  | GPIO11 (SCLK)     |
| CS   | GPIO8 (CE0)       |
| DC   | GPIO25            |
| RST  | GPIO17            |
| BUSY | GPIO24            |
| PWR  | GPIO18 (newer HATs only) |

### Enable SPI on Raspberry Pi

```bash
sudo raspi-config
# Navigate to: Interfacing Options > SPI > Enable
```

## Packages

### Display Package (`pkg/display`)
SSD1680 e-paper display driver with SPI support.

```go
// Draw draws src, from sp, into r of the display, then refreshes
dev.Draw(r, src, image.Point{})

// Or set pixels in the buffer and show it: true is black
dev.SetPixel(10, 10, true)

// Full refresh: ~2s, the panel flashes, no ghosting
dev.Update()

// Partial refresh: ~0.3s, no flashing, leaves some ghosting
dev.UpdatePartial()

// Draw refreshes as Options.FullRefreshEvery says: partially, with a full
// refresh every N draws. 0 or 1: every Draw is a full refresh.
dev, _ := display.NewSSD1680SPI(bus, dc, rst, nil, busy, &display.Options{
    Width:            122,
    Height:           250,
    FullRefreshEvery: 60,
})

// Deep sleep (and power off, with a power pin). The next update wakes it.
dev.Halt()
```

Keep the panel in deep sleep between updates: e-paper only draws power while it
refreshes, and leaving it powered for long can damage it.

### Text Package (`pkg/text`)
Text rendering with BDF font support and embedded font option. Text is black
on white.

```go
// With embedded font (no external file needed)
renderer, _ := text.NewRendererWithEmbeddedFont(&text.Config{
    Width:     250,
    Height:    122,
    LineCount: 6,
    Scale:     3, // each font pixel becomes a sharp 3x3 block
})

// Or with custom BDF font file
renderer, _ := text.NewRenderer("path/to/font.bdf", &text.Config{
    Width:     250,
    Height:    122,
    LineCount: 6,
})

// Or with any font.Face, such as a TrueType font: the examples use the Go font
f, _ := opentype.Parse(gomedium.TTF)
face, _ := opentype.NewFace(f, &opentype.FaceOptions{Size: 22, DPI: 72})
renderer, _ := text.NewRendererWithFace(face, &text.Config{
    Width:     250,
    Height:    122,
    LineCount: 4,
})
```

## Examples

### Basic Display Example
```bash
cd examples/basic
go run main.go "YOUR MESSAGE"
```

### Clock Example
Partial refresh every second, full refresh every minute:
```bash
cd examples/clock
go run main.go
```

## Building for Raspberry Pi

```bash
# Build for Raspberry Pi
GOOS=linux GOARCH=arm go build -o basic examples/basic/main.go

# Copy to Raspberry Pi
scp basic pi@raspberrypi.local:.
```

### License

Copyright (c) 2026-present [Daniel Gatis](https://github.com/danielgatis)

Licensed under [MIT License](./LICENSE)

### Buy me a coffee
Liked some of my work? Buy me a coffee (or more likely a beer)

<a href="https://www.buymeacoffee.com/danielgatis" target="_blank"><img src="https://bmc-cdn.nyc3.digitaloceanspaces.com/BMC-button-images/custom_images/orange_img.png" alt="Buy Me A Coffee" style="height: auto !important;width: auto !important;"></a>
