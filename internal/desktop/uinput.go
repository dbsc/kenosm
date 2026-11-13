package desktop

// uinput provides a virtual absolute pointer (tablet) device via /dev/uinput.
// GNOME Wayland ignores synthetic X11 (XTEST) and relative (REL) pointer events,
// so the cursor can only be positioned by sending absolute EV_ABS events through
// the kernel's uinput subsystem. This device reports itself as a tablet, which
// the compositor accepts for absolute cursor placement (and hover).

import (
	"fmt"
	"strings"
	"syscall"
	"unsafe"
)

// Linux uinput ioctl request numbers (from <linux/uinput.h>).
const (
	uiDevCreate  = 0x5501
	uiDevDestroy = 0x5502
	uiDevSetup   = 0x405C5503 // _IOW('U', 3, struct uinput_setup)
	uiAbsSetup   = 0x401C5504 // _IOW('U', 4, struct uinput_abs_setup)
	uiSetEvbit   = 0x40045564 // _IOW('U', 100, int)
	uiSetKeybit  = 0x40045565 // _IOW('U', 101, int)
	uiSetAbsbit  = 0x40045567 // _IOW('U', 103, int)
)

// Input event types and codes (from <linux/input-event-codes.h>).
const (
	evSyn     = 0x00
	evKey     = 0x01
	evAbs     = 0x03
	synReport = 0

	btnLeft    = 0x110
	btnRight   = 0x111
	btnMiddle  = 0x112
	btnToolPen = 0x140
	btnTouch   = 0x14a
	btnStylus  = 0x14b
	btnStylus2 = 0x14c

	absX = 0x00
	absY = 0x01
)

type inputEvent struct {
	Time  syscall.Timeval
	Type  uint16
	Code  uint16
	Value int32
}

type uinputSetup struct {
	ID struct {
		Bustype uint16
		Vendor  uint16
		Product uint16
		Version uint16
	}
	Name         [80]byte
	FfEffectsMax uint32
}

type absInfo struct {
	Value, Minimum, Maximum, Fuzz, Flat, Resolution int32
}

type uinputAbsSetup struct {
	Code uint16
	_    uint16
	Abs  absInfo
}

// uinputPointer is a virtual absolute pointer device. It is a long-lived handle
// to /dev/uinput; Close must be called to release the kernel device.
type uinputPointer struct {
	fd   int
	maxX int32
	maxY int32
}

// newUinputPointer creates the virtual pointer device covering an absolute
// coordinate space of width x height pixels. It returns an error when /dev/uinput
// is unavailable (e.g. no permission), in which case the caller should fall back
// to xdotool.
func newUinputPointer(width, height int) (*uinputPointer, error) {
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("invalid pointer bounds %dx%d", width, height)
	}
	fd, err := syscall.Open("/dev/uinput", syscall.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open /dev/uinput: %w", err)
	}
	f := uintptr(fd)
	p := &uinputPointer{fd: fd, maxX: int32(width), maxY: int32(height)}

	ioctl := func(req, arg uintptr) error {
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f, req, arg)
		if errno != 0 {
			return errno
		}
		return nil
	}
	fail := func(err error) (*uinputPointer, error) {
		syscall.Close(fd)
		return nil, err
	}

	if err := ioctl(uiSetEvbit, evKey); err != nil {
		return fail(fmt.Errorf("enable EV_KEY: %w", err))
	}
	if err := ioctl(uiSetEvbit, evAbs); err != nil {
		return fail(fmt.Errorf("enable EV_ABS: %w", err))
	}
	for _, b := range []uintptr{btnLeft, btnRight, btnMiddle, btnToolPen, btnTouch, btnStylus, btnStylus2} {
		if err := ioctl(uiSetKeybit, b); err != nil {
			return fail(fmt.Errorf("enable key bit: %w", err))
		}
	}
	if err := ioctl(uiSetAbsbit, absX); err != nil {
		return fail(fmt.Errorf("enable ABS_X: %w", err))
	}
	if err := ioctl(uiSetAbsbit, absY); err != nil {
		return fail(fmt.Errorf("enable ABS_Y: %w", err))
	}

	setAbs := func(code uint16, max int32) error {
		s := uinputAbsSetup{Code: code, Abs: absInfo{Minimum: 0, Maximum: max, Resolution: 100}}
		return ioctl(uiAbsSetup, uintptr(unsafe.Pointer(&s)))
	}
	if err := setAbs(absX, p.maxX); err != nil {
		return fail(fmt.Errorf("configure ABS_X: %w", err))
	}
	if err := setAbs(absY, p.maxY); err != nil {
		return fail(fmt.Errorf("configure ABS_Y: %w", err))
	}

	var setup uinputSetup
	setup.ID.Bustype = 0x06 // BUS_VIRTUAL
	setup.ID.Vendor = 0x1234
	setup.ID.Product = 0x5678
	setup.ID.Version = 1
	copy(setup.Name[:], "kenosm-virtual-pointer")
	if err := ioctl(uiDevSetup, uintptr(unsafe.Pointer(&setup))); err != nil {
		return fail(fmt.Errorf("device setup: %w", err))
	}
	if err := ioctl(uiDevCreate, 0); err != nil {
		return fail(fmt.Errorf("device create: %w", err))
	}
	return p, nil
}

func (p *uinputPointer) emit(typ, code uint16, value int32) error {
	ev := inputEvent{Type: typ, Code: code, Value: value}
	b := unsafe.Slice((*byte)(unsafe.Pointer(&ev)), int(unsafe.Sizeof(ev)))
	_, err := syscall.Write(p.fd, b)
	return err
}

func (p *uinputPointer) sync() error { return p.emit(evSyn, 0, 0) }

// move positions the cursor at the absolute (x, y), pen in proximity (hover).
func (p *uinputPointer) move(x, y int) error {
	p.emit(evKey, btnToolPen, 1)
	p.emit(evAbs, absX, int32(x))
	p.emit(evAbs, absY, int32(y))
	return p.sync()
}

// button presses (down=true) or releases (down=false) a mouse button.
func (p *uinputPointer) button(code uint16, down bool) error {
	v := int32(0)
	if down {
		v = 1
	}
	if err := p.emit(evKey, code, v); err != nil {
		return err
	}
	return p.sync()
}

// uinputButton maps a desktop button name ("left", "middle", "right") to the
// Linux input key code that mutter/libinput understands on a tablet device:
// the pen tip (BTN_TOUCH) is a left/primary click, while the two stylus barrel
// buttons are mapped to middle and right. Unknown names default to left.
func uinputButton(button string) uint16 {
	switch strings.ToLower(strings.TrimSpace(button)) {
	case "right", "3":
		return btnStylus2
	case "middle", "2":
		return btnStylus
	default:
		return btnTouch
	}
}

// close destroys the virtual device and releases the file descriptor.
func (p *uinputPointer) close() {
	if p == nil || p.fd <= 0 {
		return
	}
	_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, uintptr(p.fd), uiDevDestroy, 0)
	syscall.Close(p.fd)
	p.fd = 0
}
