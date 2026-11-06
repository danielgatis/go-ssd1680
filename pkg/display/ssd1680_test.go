package display

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"testing"
	"time"

	"periph.io/x/conn/v3/gpio"
	"periph.io/x/conn/v3/gpio/gpiotest"
	"periph.io/x/conn/v3/spi/spitest"
)

type levelPin struct {
	gpiotest.Pin
	levels []gpio.Level
}

func (p *levelPin) Out(l gpio.Level) error {
	p.levels = append(p.levels, l)

	return p.Pin.Out(l)
}

// command is one command byte and the data sent after it.
type command struct {
	cmd  byte
	data []byte
}

type rig struct {
	dev  *SSD1680
	port *spitest.Record
	dc   *levelPin
}

func newRig(t *testing.T, cs gpio.PinOut, opts *Options) *rig {
	t.Helper()

	port := &spitest.Record{}
	dc := &levelPin{Pin: gpiotest.Pin{N: "DC"}}
	rst := &gpiotest.Pin{N: "RST"}
	busy := &gpiotest.Pin{N: "BUSY", L: gpio.Low}

	dev, err := NewSSD1680SPI(port, dc, rst, cs, busy, opts)
	if err != nil {
		t.Fatalf("NewSSD1680SPI: %v", err)
	}

	return &rig{dev: dev, port: port, dc: dc}
}

func newV4(t *testing.T, opts Options) *rig {
	t.Helper()

	opts.Width, opts.Height = 122, 250

	return newRig(t, nil, &opts)
}

// commands pairs every SPI write with the DC level set before it and folds
// data writes into the command they follow.
func (r *rig) commands(t *testing.T) []command {
	t.Helper()

	if len(r.dc.levels) != len(r.port.Ops) {
		t.Fatalf("%d dc levels for %d writes", len(r.dc.levels), len(r.port.Ops))
	}

	var out []command
	for i, op := range r.port.Ops {
		if r.dc.levels[i] == gpio.Low {
			out = append(out, command{cmd: op.W[0]})
			continue
		}

		if len(out) == 0 {
			t.Fatal("data written before any command")
		}
		last := &out[len(out)-1]
		last.data = append(last.data, op.W...)
	}

	return out
}

func (r *rig) reset() {
	r.port.Ops = nil
	r.dc.levels = nil
}

func find(cmds []command, cmd byte) []command {
	var out []command
	for _, c := range cmds {
		if c.cmd == cmd {
			out = append(out, c)
		}
	}

	return out
}

func refreshModes(cmds []command) []byte {
	var out []byte
	for _, c := range find(cmds, 0x22) {
		out = append(out, c.data[0])
	}

	return out
}

func TestNewSSD1680SPI_RequiresPins(t *testing.T) {
	pin := &gpiotest.Pin{}
	opts := &Options{Width: 122, Height: 250}

	cases := map[string]func() error{
		"dc": func() error {
			_, err := NewSSD1680SPI(&spitest.Record{}, nil, pin, nil, pin, opts)
			return err
		},
		"rst": func() error {
			_, err := NewSSD1680SPI(&spitest.Record{}, pin, nil, nil, pin, opts)
			return err
		},
		"busy": func() error {
			_, err := NewSSD1680SPI(&spitest.Record{}, pin, pin, nil, nil, opts)
			return err
		},
		"size": func() error {
			_, err := NewSSD1680SPI(&spitest.Record{}, pin, pin, nil, pin, &Options{})
			return err
		},
	}

	for name, fn := range cases {
		if fn() == nil {
			t.Errorf("expected an error without %s", name)
		}
	}
}

func TestInit_Sequence(t *testing.T) {
	r := newV4(t, Options{})
	cmds := r.commands(t)

	if cmds[0].cmd != 0x12 {
		t.Fatalf("first command %#x, want software reset 0x12", cmds[0].cmd)
	}

	want := map[byte][]byte{
		0x01: {0xF9, 0x00, 0x00},       // 250 gates
		0x11: {0x03},                   // X then Y increment
		0x44: {0x00, 0x0F},             // 16 bytes per row
		0x45: {0x00, 0x00, 0xF9, 0x00}, // rows 0 to 249
		0x3C: {0x05},                   // border
		0x21: {0x00, 0x80},             // source range
		0x18: {0x80},                   // internal temperature sensor
	}

	for cmd, data := range want {
		got := find(cmds, cmd)
		if len(got) != 1 || !bytes.Equal(got[0].data, data) {
			t.Errorf("command %#x: got %v, want one with %v", cmd, got, data)
		}
	}
}

func TestBounds_FollowRotation(t *testing.T) {
	cases := map[Rotation]image.Rectangle{
		Rotate0:   image.Rect(0, 0, 122, 250),
		Rotate90:  image.Rect(0, 0, 250, 122),
		Rotate180: image.Rect(0, 0, 122, 250),
		Rotate270: image.Rect(0, 0, 250, 122),
	}

	for rot, want := range cases {
		if got := newV4(t, Options{Rotation: rot}).dev.Bounds(); got != want {
			t.Errorf("rotation %d: got %v, want %v", rot, got, want)
		}
	}
}

func TestSetPixel_MapsRotationToNativeFrame(t *testing.T) {
	cases := []struct {
		rot   Rotation
		x, y  int
		index int
		mask  byte
	}{
		{Rotate0, 0, 0, 0, 0x80},
		{Rotate0, 121, 0, 15, 0x40},
		{Rotate0, 0, 249, 249 * 16, 0x80},
		{Rotate90, 0, 0, 249 * 16, 0x80},
		{Rotate90, 249, 121, 15, 0x40},
		{Rotate180, 0, 0, 249*16 + 15, 0x40},
		{Rotate270, 0, 0, 15, 0x40},
	}

	for _, c := range cases {
		dev := newV4(t, Options{Rotation: c.rot}).dev
		dev.SetPixel(c.x, c.y, true)

		for i, b := range dev.buffer {
			want := byte(0xFF)
			if i == c.index {
				want = 0xFF &^ c.mask
			}
			if b != want {
				t.Fatalf("rotation %d (%d,%d): byte %d is %#x, want %#x", c.rot, c.x, c.y, i, b, want)
			}
		}
	}
}

func TestSetPixel_OutOfBoundsIsIgnored(t *testing.T) {
	dev := newV4(t, Options{}).dev
	dev.SetPixel(-1, 0, true)
	dev.SetPixel(122, 0, true)
	dev.SetPixel(0, 250, true)

	for i, b := range dev.buffer {
		if b != 0xFF {
			t.Fatalf("byte %d changed to %#x", i, b)
		}
	}
}

func TestClear_WhitensBuffer(t *testing.T) {
	dev := newV4(t, Options{}).dev
	dev.SetPixel(3, 3, true)
	dev.Clear()

	for i, b := range dev.buffer {
		if b != 0xFF {
			t.Fatalf("byte %d is %#x after Clear", i, b)
		}
	}
}

func TestUpdate_WritesBothRAMsAndRefreshesFully(t *testing.T) {
	r := newV4(t, Options{})
	r.reset()

	if err := r.dev.Update(); err != nil {
		t.Fatalf("Update: %v", err)
	}

	cmds := r.commands(t)
	for _, ram := range []byte{0x24, 0x26} {
		got := find(cmds, ram)
		if len(got) != 1 || len(got[0].data) != 16*250 {
			t.Errorf("RAM %#x: got %d writes, want one of %d bytes", ram, len(got), 16*250)
		}
	}

	if modes := refreshModes(cmds); !bytes.Equal(modes, []byte{0xF7}) {
		t.Errorf("refresh modes %#v, want full 0xF7", modes)
	}
}

func TestUpdatePartial_WithoutBaseRunsFullRefresh(t *testing.T) {
	r := newV4(t, Options{})
	r.reset()

	if err := r.dev.UpdatePartial(); err != nil {
		t.Fatalf("UpdatePartial: %v", err)
	}

	if modes := refreshModes(r.commands(t)); !bytes.Equal(modes, []byte{0xF7}) {
		t.Errorf("refresh modes %#v, want full 0xF7", modes)
	}
}

func TestDraw_FullRefreshEvery(t *testing.T) {
	r := newV4(t, Options{FullRefreshEvery: 3})
	r.reset()

	img := image.NewGray(r.dev.Bounds())
	for range 5 {
		if err := r.dev.Draw(img.Bounds(), img, image.Point{}); err != nil {
			t.Fatalf("Draw: %v", err)
		}
	}

	cmds := r.commands(t)
	want := []byte{0xF7, 0xFF, 0xFF, 0xF7, 0xFF}
	if modes := refreshModes(cmds); !bytes.Equal(modes, want) {
		t.Fatalf("refresh modes %#v, want %#v", modes, want)
	}

	// The full refresh after partials re-runs init: one software reset
	if got := len(find(cmds, 0x12)); got != 1 {
		t.Errorf("%d software resets, want 1", got)
	}
}

func TestDraw_EveryDrawFullByDefault(t *testing.T) {
	r := newV4(t, Options{})
	r.reset()

	img := image.NewGray(r.dev.Bounds())
	for range 2 {
		if err := r.dev.Draw(img.Bounds(), img, image.Point{}); err != nil {
			t.Fatalf("Draw: %v", err)
		}
	}

	if modes := refreshModes(r.commands(t)); !bytes.Equal(modes, []byte{0xF7, 0xF7}) {
		t.Fatalf("refresh modes %#v, want two full refreshes", modes)
	}
}

func TestDraw_ThresholdsAndRotates(t *testing.T) {
	r := newV4(t, Options{Rotation: Rotate90})

	img := image.NewGray(image.Rect(0, 0, 250, 122))
	for i := range img.Pix {
		img.Pix[i] = 0xFF
	}
	img.SetGray(0, 0, color.Gray{Y: 0x7F}) // dark enough: black
	img.SetGray(1, 0, color.Gray{Y: 0x80}) // light: white

	if err := r.dev.Draw(img.Bounds(), img, image.Point{}); err != nil {
		t.Fatalf("Draw: %v", err)
	}

	// (0,0) landscape is native (0,249); (1,0) is native (0,248)
	if got := r.dev.buffer[249*16]; got != 0x7F {
		t.Errorf("row 249 byte 0 is %#x, want 0x7f", got)
	}
	if got := r.dev.buffer[248*16]; got != 0xFF {
		t.Errorf("row 248 byte 0 is %#x, want 0xff", got)
	}
}

func TestDraw_HonoursRectangleAndSourcePoint(t *testing.T) {
	r := newV4(t, Options{})

	src := image.NewGray(image.Rect(0, 0, 20, 20))
	for i := range src.Pix {
		src.Pix[i] = 0xFF
	}
	src.SetGray(10, 10, color.Gray{})

	// Draw src from (10,10) into (8,0)-(16,8): the black pixel lands at (8,0)
	if err := r.dev.Draw(image.Rect(8, 0, 16, 8), src, image.Pt(10, 10)); err != nil {
		t.Fatalf("Draw: %v", err)
	}

	if got := r.dev.buffer[1]; got != 0x7F {
		t.Errorf("byte 1 is %#x, want 0x7f", got)
	}
}

func TestHaltAndWake(t *testing.T) {
	r := newV4(t, Options{})
	r.reset()

	if err := r.dev.Halt(); err != nil {
		t.Fatalf("Halt: %v", err)
	}
	if err := r.dev.Halt(); err != nil {
		t.Fatalf("second Halt: %v", err)
	}

	cmds := r.commands(t)
	if len(cmds) != 1 || cmds[0].cmd != 0x10 || !bytes.Equal(cmds[0].data, []byte{0x01}) {
		t.Fatalf("got %v, want a single deep sleep [0x10 0x01]", cmds)
	}

	r.reset()
	if err := r.dev.Wake(); err != nil {
		t.Fatalf("Wake: %v", err)
	}

	if got := len(find(r.commands(t), 0x12)); got != 1 {
		t.Fatalf("Wake sent %d software resets, want 1", got)
	}
}

func TestDraw_WakesFromHalt(t *testing.T) {
	r := newV4(t, Options{FullRefreshEvery: 10})

	img := image.NewGray(r.dev.Bounds())
	if err := r.dev.Draw(img.Bounds(), img, image.Point{}); err != nil {
		t.Fatalf("Draw: %v", err)
	}
	if err := r.dev.Halt(); err != nil {
		t.Fatalf("Halt: %v", err)
	}

	r.reset()
	if err := r.dev.Draw(img.Bounds(), img, image.Point{}); err != nil {
		t.Fatalf("Draw: %v", err)
	}

	cmds := r.commands(t)
	if len(find(cmds, 0x12)) != 1 {
		t.Error("Draw after Halt did not re-initialize the panel")
	}
	if modes := refreshModes(cmds); !bytes.Equal(modes, []byte{0xF7}) {
		t.Errorf("refresh modes %#v, want a full refresh after waking", modes)
	}
}

func TestPowerPin_OnAtInit(t *testing.T) {
	pwr := &levelPin{Pin: gpiotest.Pin{N: "PWR"}}
	newV4(t, Options{Power: pwr})

	if len(pwr.levels) == 0 || pwr.levels[0] != gpio.High {
		t.Fatalf("power pin levels %v, want High first", pwr.levels)
	}
}

func TestWaitBusy_TimesOut(t *testing.T) {
	r := newV4(t, Options{})
	r.dev.busy = &gpiotest.Pin{N: "BUSY", L: gpio.High}
	r.dev.busyTimeout = 30 * time.Millisecond

	if err := r.dev.Update(); !errors.Is(err, ErrBusyTimeout) {
		t.Fatalf("got %v, want ErrBusyTimeout", err)
	}
}

func TestSoftwareCSFramesEveryTransfer(t *testing.T) {
	cs := &levelPin{Pin: gpiotest.Pin{N: "CS"}}
	r := newRig(t, cs, &Options{Width: 122, Height: 250})

	if got, want := len(cs.levels), 2*len(r.port.Ops); got != want {
		t.Fatalf("cs toggled %d times, want %d", got, want)
	}

	for i := 0; i < len(cs.levels); i += 2 {
		if cs.levels[i] != gpio.Low || cs.levels[i+1] != gpio.High {
			t.Fatalf("transfer %d not framed low/high: %v", i/2, cs.levels[i:i+2])
		}
	}
}

func TestTx_SplitsLargeWrites(t *testing.T) {
	// A 128 x 296 panel holds 4736 bytes, more than spidev takes at once
	r := newRig(t, nil, &Options{Width: 128, Height: 296})
	r.reset()

	if err := r.dev.sendData(r.dev.buffer); err != nil {
		t.Fatalf("sendData: %v", err)
	}

	if len(r.port.Ops) != 2 || len(r.port.Ops[0].W) != defaultMaxTx || len(r.port.Ops[1].W) != 4736-defaultMaxTx {
		t.Fatalf("got %d writes, want %d + %d bytes", len(r.port.Ops), defaultMaxTx, 4736-defaultMaxTx)
	}
}

func TestString(t *testing.T) {
	if newV4(t, Options{Rotation: Rotate90}).dev.String() == "" {
		t.Error("String() should not return empty string")
	}
}
