// Package display provides drivers for e-paper displays.
package display

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"time"

	"periph.io/x/conn/v3"
	"periph.io/x/conn/v3/display"
	"periph.io/x/conn/v3/gpio"
	"periph.io/x/conn/v3/physic"
	"periph.io/x/conn/v3/spi"
)

var _ display.Drawer = (*SSD1680)(nil)

// ErrBusyTimeout is returned when the panel holds BUSY high for longer than a
// refresh can take, which means it is not answering (unplugged, no power).
var ErrBusyTimeout = errors.New("display: timed out waiting for BUSY to go low")

const (
	// busyTimeout bounds every wait on BUSY. A full refresh takes about 2s.
	busyTimeout = 10 * time.Second

	// powerOffDelay lets deep sleep settle before Halt cuts the panel's power.
	powerOffDelay = 2 * time.Second

	// defaultMaxTx is spidev's default bufsiz: larger writes are split.
	defaultMaxTx = 4096
)

// Rotation is how the panel's native portrait frame maps to the image drawn.
type Rotation int

const (
	// Rotate0 is the native portrait frame: Width x Height.
	Rotate0 Rotation = iota
	// Rotate90 is landscape, the image turned a quarter counterclockwise on
	// the native frame.
	Rotate90
	// Rotate180 is portrait, upside down.
	Rotate180
	// Rotate270 is landscape, the image turned a quarter clockwise on the
	// native frame: right side up on the 2.13inch HAT mounted on a Pi.
	Rotate270
)

// SSD1680 driver for black and white e-paper displays
type SSD1680 struct {
	c    conn.Conn
	dc   gpio.PinOut
	rst  gpio.PinOut
	cs   gpio.PinOut
	pwr  gpio.PinOut
	busy gpio.PinIn

	width  int // native panel width, in pixels
	height int // native panel height, in pixels
	stride int // bytes per native row
	rot    Rotation
	rect   image.Rectangle
	buffer []byte

	fullEvery   int
	partials    int  // partial refreshes since the last full one
	base        bool // RAM holds the image on the glass, so a partial refresh can diff against it
	reinit      bool // a partial refresh reset the registers a full refresh needs
	asleep      bool
	busyTimeout time.Duration
}

// Options defines the configuration options for the SSD1680 device
type Options struct {
	// Width and Height are the panel's native portrait size: 122 x 250 for the
	// Waveshare 2.13inch e-Paper HAT (V4).
	Width  int
	Height int

	// Rotation maps the native frame to the image Draw takes.
	Rotation Rotation

	// FullRefreshEvery makes Draw refresh partially (~0.3s, no flashing) and
	// run a full refresh (~2s, flashing) every N draws to clear the ghosting
	// partial refreshes leave. 0 or 1: every Draw is a full refresh.
	FullRefreshEvery int

	// Power is the panel power pin (GPIO18 on HATs that have one), nil when
	// the panel is powered straight from the header.
	Power gpio.PinOut
}

// NewSSD1680SPI creates a new SSD1680 display driver for SPI communication.
//
// cs may be nil when the SPI controller drives chip select in hardware (the
// spidev CE line); the driver then never touches it. Pass a pin only when chip
// select is wired to a plain GPIO.
func NewSSD1680SPI(p spi.Port, dc, rst, cs gpio.PinOut, busy gpio.PinIn, opts *Options) (*SSD1680, error) {
	if dc == nil || rst == nil || busy == nil {
		return nil, errors.New("display: dc, rst and busy pins are required")
	}

	if opts == nil || opts.Width <= 0 || opts.Height <= 0 {
		return nil, errors.New("display: width and height must be greater than 0")
	}

	speed := 4 * physic.MegaHertz
	c, err := p.Connect(speed, spi.Mode0, 8)
	if err != nil {
		return nil, err
	}

	if err := busy.In(gpio.PullDown, gpio.NoEdge); err != nil {
		return nil, err
	}

	stride := (opts.Width + 7) / 8

	rect := image.Rect(0, 0, opts.Width, opts.Height)
	if opts.Rotation == Rotate90 || opts.Rotation == Rotate270 {
		rect = image.Rect(0, 0, opts.Height, opts.Width)
	}

	ssd1680 := &SSD1680{
		c:           c,
		dc:          dc,
		rst:         rst,
		cs:          cs,
		pwr:         opts.Power,
		busy:        busy,
		width:       opts.Width,
		height:      opts.Height,
		stride:      stride,
		rot:         opts.Rotation,
		rect:        rect,
		buffer:      make([]byte, stride*opts.Height),
		fullEvery:   opts.FullRefreshEvery,
		busyTimeout: busyTimeout,
	}
	ssd1680.Clear()

	// Initialize display
	if err := ssd1680.init(); err != nil {
		return nil, err
	}

	return ssd1680, nil
}

// init powers the panel, resets it and loads the full refresh registers
func (d *SSD1680) init() error {
	if d.pwr != nil {
		if err := d.pwr.Out(gpio.High); err != nil {
			return err
		}
	}

	// Hardware reset sequence
	d.rst.Out(gpio.High)
	time.Sleep(20 * time.Millisecond)
	d.rst.Out(gpio.Low)
	time.Sleep(2 * time.Millisecond)
	d.rst.Out(gpio.High)
	time.Sleep(20 * time.Millisecond)

	if err := d.waitBusy(); err != nil {
		return err
	}

	// Software reset
	if err := d.sendCommand(0x12); err != nil {
		return err
	}

	if err := d.waitBusy(); err != nil {
		return err
	}

	if err := d.setFrame(); err != nil {
		return err
	}

	commands := [][]byte{
		{0x3C, 0x05},       // Border waveform
		{0x21, 0x00, 0x80}, // Display update control: source S8 to S167
		{0x18, 0x80},       // Internal temperature sensor
	}

	for _, cmd := range commands {
		if err := d.sendCommand(cmd[0], cmd[1:]...); err != nil {
			return err
		}
	}

	d.base = false
	d.reinit = false

	return d.waitBusy()
}

// setFrame sets the gate count, the data entry mode and a RAM window covering
// the whole panel, with the address counters at its origin
func (d *SSD1680) setFrame() error {
	gates := d.height - 1
	xEnd := (d.width - 1) >> 3

	commands := [][]byte{
		{0x01, byte(gates), byte(gates >> 8), 0x00}, // Driver output control
		{0x11, 0x03},             // Data entry mode: X then Y increment
		{0x44, 0x00, byte(xEnd)}, // RAM X start/end, in bytes
		{0x45, 0x00, 0x00, byte(gates), byte(gates >> 8)}, // RAM Y start/end
	}

	for _, cmd := range commands {
		if err := d.sendCommand(cmd[0], cmd[1:]...); err != nil {
			return err
		}
	}

	return d.setCursor()
}

// setCursor moves the RAM address counters to the origin
func (d *SSD1680) setCursor() error {
	if err := d.sendCommand(0x4E, 0x00); err != nil {
		return err
	}

	return d.sendCommand(0x4F, 0x00, 0x00)
}

// waitBusy blocks until the panel releases BUSY
func (d *SSD1680) waitBusy() error {
	deadline := time.Now().Add(d.busyTimeout)

	for d.busy.Read() == gpio.High {
		if time.Now().After(deadline) {
			return ErrBusyTimeout
		}
		time.Sleep(10 * time.Millisecond)
	}

	return nil
}

// sendCommand sends a command and its parameters to the display
func (d *SSD1680) sendCommand(cmd byte, data ...byte) error {
	if err := d.tx(gpio.Low, []byte{cmd}); err != nil {
		return err
	}

	if len(data) == 0 {
		return nil
	}

	return d.sendData(data)
}

// sendData sends data to the display
func (d *SSD1680) sendData(data []byte) error {
	return d.tx(gpio.High, data)
}

// tx sets DC to the given level and writes b, in chunks the SPI driver
// accepts, framing it with the software chip select when one was given.
func (d *SSD1680) tx(dc gpio.Level, b []byte) error {
	if err := d.dc.Out(dc); err != nil {
		return err
	}

	if d.cs != nil {
		if err := d.cs.Out(gpio.Low); err != nil {
			return err
		}
		defer d.cs.Out(gpio.High)
	}

	maxTx := defaultMaxTx
	if l, ok := d.c.(conn.Limits); ok && l.MaxTxSize() > 0 {
		maxTx = l.MaxTxSize()
	}

	for len(b) > 0 {
		n := min(len(b), maxTx)
		if err := d.c.Tx(b[:n], nil); err != nil {
			return err
		}
		b = b[n:]
	}

	return nil
}

// writeRAM writes the buffer to one of the panel's two RAMs: 0x24 holds the
// image to show, 0x26 the image on the glass that a partial refresh diffs
// against
func (d *SSD1680) writeRAM(ram byte) error {
	if err := d.setCursor(); err != nil {
		return err
	}

	if err := d.sendCommand(ram); err != nil {
		return err
	}

	return d.sendData(d.buffer)
}

// refresh runs the display update sequence and waits for it to finish
func (d *SSD1680) refresh(mode byte) error {
	if err := d.sendCommand(0x22, mode); err != nil {
		return err
	}

	if err := d.sendCommand(0x20); err != nil {
		return err
	}

	return d.waitBusy()
}

// Update shows the buffer with a full refresh, waking the panel from Halt:
// about 2s, the panel flashes, and no ghosting is left.
func (d *SSD1680) Update() error {
	if err := d.Wake(); err != nil {
		return err
	}

	if d.reinit {
		if err := d.init(); err != nil {
			return err
		}
	}

	if err := d.writeRAM(0x24); err != nil {
		return err
	}

	if err := d.writeRAM(0x26); err != nil {
		return err
	}

	if err := d.refresh(0xF7); err != nil {
		return err
	}

	d.base = true
	d.partials = 0

	return nil
}

// UpdatePartial shows the buffer with a partial refresh: about 0.3s and no
// flashing, but each one leaves some ghosting that only a full refresh clears.
// It runs a full refresh instead when the panel holds no base image yet.
func (d *SSD1680) UpdatePartial() error {
	if err := d.Wake(); err != nil {
		return err
	}

	if !d.base {
		return d.Update()
	}

	// A short reset drops the full refresh waveform before the partial one
	d.rst.Out(gpio.Low)
	time.Sleep(1 * time.Millisecond)
	d.rst.Out(gpio.High)
	d.reinit = true

	if err := d.sendCommand(0x3C, 0x80); err != nil { // Border waveform
		return err
	}

	if err := d.setFrame(); err != nil {
		return err
	}

	if err := d.writeRAM(0x24); err != nil {
		return err
	}

	if err := d.refresh(0xFF); err != nil {
		return err
	}

	// The image now on the glass is the base the next partial diffs against
	if err := d.writeRAM(0x26); err != nil {
		return err
	}

	d.partials++

	return nil
}

// setPixel sets a pixel in the buffer, in rotated coordinates. A set bit is
// white.
func (d *SSD1680) setPixel(x, y int, black bool) {
	if x < 0 || x >= d.rect.Dx() || y < 0 || y >= d.rect.Dy() {
		return
	}

	nx, ny := x, y
	switch d.rot {
	case Rotate90:
		nx, ny = y, d.height-1-x
	case Rotate180:
		nx, ny = d.width-1-x, d.height-1-y
	case Rotate270:
		nx, ny = d.width-1-y, x
	}

	index := ny*d.stride + nx/8
	mask := byte(0x80) >> (nx % 8)

	if black {
		d.buffer[index] &^= mask
	} else {
		d.buffer[index] |= mask
	}
}

// Clear clears the display buffer to white
func (d *SSD1680) Clear() {
	for i := range d.buffer {
		d.buffer[i] = 0xFF
	}
}

// String implements fmt.Stringer
func (d *SSD1680) String() string {
	return fmt.Sprintf("SSD1680{%s, %s}", d.rect.Max, d.c)
}

// Bounds returns the display bounds, rotated
func (d *SSD1680) Bounds() image.Rectangle {
	return d.rect
}

// ColorModel returns the color model (monochrome)
func (d *SSD1680) ColorModel() color.Model {
	return color.GrayModel
}

// Halt puts the panel in deep sleep and, with a power pin, cuts its power.
// E-paper keeps the last image with no power at all. The next refresh wakes
// the panel; Wake does it ahead of time.
func (d *SSD1680) Halt() error {
	if d.asleep {
		return nil
	}

	if err := d.sendCommand(0x10, 0x01); err != nil {
		return err
	}
	d.asleep = true

	if d.pwr != nil {
		time.Sleep(powerOffDelay)

		return d.pwr.Out(gpio.Low)
	}

	return nil
}

// Wake brings the panel back from Halt. The next refresh is a full one.
func (d *SSD1680) Wake() error {
	if !d.asleep {
		return nil
	}

	if err := d.init(); err != nil {
		return err
	}
	d.asleep = false

	return nil
}

// Draw implements display.Drawer: it draws src, from sp, into r of the
// display, then refreshes as Options.FullRefreshEvery says: partially, with a
// full refresh every N draws.
func (d *SSD1680) Draw(r image.Rectangle, src image.Image, sp image.Point) error {
	r = r.Intersect(d.rect)

	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			p := sp.Add(image.Pt(x-r.Min.X, y-r.Min.Y))
			if !p.In(src.Bounds()) {
				continue
			}

			// Pixels darker than 50% are black
			gray := color.GrayModel.Convert(src.At(p.X, p.Y)).(color.Gray)
			d.setPixel(x, y, gray.Y < 128)
		}
	}

	if d.fullEvery > 1 && d.base && d.partials < d.fullEvery-1 {
		return d.UpdatePartial()
	}

	return d.Update()
}

// SetPixel directly sets a pixel on the display buffer
func (d *SSD1680) SetPixel(x, y int, black bool) {
	d.setPixel(x, y, black)
}
