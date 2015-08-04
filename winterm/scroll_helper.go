// +build windows

package winterm

func (h *WindowsAnsiEventHandler) scrollPageUp() error {
	return h.scrollPage(1)
}

func (h *WindowsAnsiEventHandler) scrollPageDown() error {
	return h.scrollPage(-1)
}

func (h *WindowsAnsiEventHandler) scrollPage(param int) error {
	info, err := GetConsoleScreenBufferInfo(h.fd)
	if err != nil {
		return err
	}

	return h.scroll(param, scrollRegion{0, info.Size.Y - 1})
}

func (h *WindowsAnsiEventHandler) scrollUp(param int) error {
	return h.scroll(param, h.sr)
}

func (h *WindowsAnsiEventHandler) scrollDown(param int) error {
	return h.scroll(-param, h.sr)
}

func (h *WindowsAnsiEventHandler) scroll(param int, sr scrollRegion) error {

	info, err := GetConsoleScreenBufferInfo(h.fd)
	if err != nil {
		return err
	}
    
    if sr.top >= sr.bottom {
        sr.top = 0
        sr.bottom = info.Window.Bottom - info.Window.Top + 1
    }

	logger.Infof("scroll: scrollTop: %d, scrollBottom: %d", sr.top, sr.bottom)
	logger.Infof("scroll: windowTop: %d, windowBottom: %d", info.Window.Top, info.Window.Bottom)

	rect := info.Window

	// Current scroll region in Windows backing buffer coordinates
	top := rect.Top + SHORT(sr.top)
	bottom := rect.Top + SHORT(sr.bottom)

	// Area from backing buffer to be copied
	scrollRect := SMALL_RECT{
		Top:    top + SHORT(param),
		Bottom: bottom + SHORT(param),
		Left:   rect.Left,
		Right:  rect.Right,
	}

	// Clipping region should be the original scroll region
	clipRegion := SMALL_RECT{
		Top:    top,
		Bottom: bottom,
		Left:   rect.Left,
		Right:  rect.Right,
	}

	// Origin to which area should be copied
	destOrigin := COORD{
		X: rect.Left,
		Y: top,
	}

	char := CHAR_INFO{
		UnicodeChar: ' ',
		Attributes:  h.attributes,
	}

	if err := ScrollConsoleScreenBuffer(h.fd, scrollRect, clipRegion, destOrigin, char); err != nil {
		return err
	}
    logger.Infof("scroll success")
	return nil
}
