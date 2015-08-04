// +build windows

package winterm

import (
	"bytes"
	"io/ioutil"
	"os"
	"strconv"

	. "github.com/Azure/go-ansiterm"
	"github.com/Sirupsen/logrus"
)

var logger *logrus.Logger

type WindowsAnsiEventHandler struct {
	attributes WORD
	inverted   bool
	fd        uintptr
	file      *os.File
	infoReset *CONSOLE_SCREEN_BUFFER_INFO
	sr        scrollRegion
	buffer    bytes.Buffer
	wrapNext  bool
    curInfo   *CONSOLE_SCREEN_BUFFER_INFO
    curPos    COORD
}

func CreateWinEventHandler(fd uintptr, file *os.File) AnsiEventHandler {
	logFile := ioutil.Discard

	if isDebugEnv := os.Getenv(LogEnv); isDebugEnv == "1" {
		logFile, _ = os.Create("winEventHandler.log")
	}

	logger = &logrus.Logger{
		Out:       logFile,
		Formatter: new(logrus.TextFormatter),
		Level:     logrus.DebugLevel,
	}

	infoReset, err := GetConsoleScreenBufferInfo(fd)
	if err != nil {
		return nil
	}

	return &WindowsAnsiEventHandler{
		attributes: infoReset.Attributes,
		fd:        fd,
		file:      file,
		infoReset: infoReset,
	}
}

type scrollRegion struct {
	top    SHORT
	bottom SHORT
}

func (h *WindowsAnsiEventHandler) Print(b byte) error {
    if err := h.cacheInfo(); err != nil {
        return err
    }    
    // Update the current calculated position and scroll if this caused wrapping
    // off the screen.
    h.curPos.X++
    if h.curPos.X > h.curInfo.Size.X {
        sr := h.effectiveSr(h.curInfo.Window)
        if h.curPos.Y < sr.bottom {
            // nothing to scroll
            h.curPos.X = 1
            h.curPos.Y++
        } else if sr.top == h.curInfo.Window.Top && sr.bottom == h.curInfo.Window.Bottom && false {
            // scrolling will happen normally
            h.curPos.X = 1
        } else {
            // A custom scroll region is active. Scroll the window manually.
            if err := h.prepareForCommand(true); err != nil {
                return err
            }
            if err := h.scrollUp(1); err != nil {
                return err
            }
            pos := COORD{0, h.curPos.Y}
            if err := SetConsoleCursorPosition(h.fd, pos); err != nil {
                return err
            }
            // Retry printing the character now that the cursor is at the beginning of the line.
            return h.Print(b)
        }
    }
    return h.buffer.WriteByte(b)
}

func (h *WindowsAnsiEventHandler) Execute(b byte) error {
    if err := h.cacheInfo(); err != nil {
        return err
    }
    if h.curPos.X == h.curInfo.Size.X {
        if err := h.prepareForCommand(true); err != nil {
            return err
        }
        if err := h.cacheInfo(); err != nil {
            return err
        }        
    }
    switch (b) {
        case ANSI_BEL:
            // nothing
        case ANSI_BACKSPACE:
            if h.curPos.X > 0 {
                h.curPos.X--
            }
        //case ANSI_TAB:
            // TODO
        //case ANSI_VERTICAL_TAB:
            // ???
        //case ANSI_FORM_FEED:
            // ???
        case ANSI_LINE_FEED:
            // this is going to wrap. let's assume non-VT100 behavior for now
            sr := h.effectiveSr(h.curInfo.Window)
            if h.curPos.Y < sr.bottom {
                h.curPos.X = 0
                h.curPos.Y++
            } else if sr.top == h.curInfo.Window.Top && sr.bottom == h.curInfo.Window.Bottom && false {
                // scrolling will happen normally, no cursor Y position change
                h.curPos.X = 0
            } else {
                // A custom scroll region is active. Scroll the window manually.
                if err := h.Flush(); err != nil {
                    return err
                }
                if err := h.scrollUp(1); err != nil {
                    return err
                }
                h.curPos.X = 0
                return SetConsoleCursorPosition(h.fd, h.curPos)
            }
        case ANSI_CARRIAGE_RETURN:
            h.curPos.X = 0
        default:
            return nil
    }
    return h.buffer.WriteByte(b)
}

func (h *WindowsAnsiEventHandler) CUU(param int) error {
	if err := h.prepareForCommand(true); err != nil {
		return err
	}
	logger.Infof("CUU: [%v]", []string{strconv.Itoa(param)})
	return h.moveCursorVertical(-param)
}

func (h *WindowsAnsiEventHandler) CUD(param int) error {
	if err := h.prepareForCommand(true); err != nil {
		return err
	}
	logger.Infof("CUD: [%v]", []string{strconv.Itoa(param)})
	return h.moveCursorVertical(param)
}

func (h *WindowsAnsiEventHandler) CUF(param int) error {
	if err := h.prepareForCommand(true); err != nil {
		return err
	}
	logger.Infof("CUF: [%v]", []string{strconv.Itoa(param)})
	return h.moveCursorHorizontal(param)
}

func (h *WindowsAnsiEventHandler) CUB(param int) error {
	if err := h.prepareForCommand(true); err != nil {
		return err
	}
	logger.Infof("CUB: [%v]", []string{strconv.Itoa(param)})
	return h.moveCursorHorizontal(-param)
}

func (h *WindowsAnsiEventHandler) CNL(param int) error {
	if err := h.prepareForCommand(true); err != nil {
		return err
	}
	logger.Infof("CNL: [%v]", []string{strconv.Itoa(param)})
	return h.moveCursorLine(param)
}

func (h *WindowsAnsiEventHandler) CPL(param int) error {
	if err := h.prepareForCommand(true); err != nil {
		return err
	}
	logger.Infof("CPL: [%v]", []string{strconv.Itoa(param)})
	return h.moveCursorLine(-param)
}

func (h *WindowsAnsiEventHandler) CHA(param int) error {
	if err := h.prepareForCommand(true); err != nil {
		return err
	}
	logger.Infof("CHA: [%v]", []string{strconv.Itoa(param)})
	return h.moveCursorColumn(param)
}

func (h *WindowsAnsiEventHandler) VPA(param int) error {
	if err := h.Flush(); err != nil {
		return err
	}
	logger.Infof("VPA: [[%d]]", param)
	info, err := GetConsoleScreenBufferInfo(h.fd)
	if err != nil {
		return err
	}
	pos := info.CursorPosition
	pos.Y = ensureInRange(SHORT(param-1), info.Window.Top, info.Window.Bottom)
	return SetConsoleCursorPosition(h.fd, pos)
}

func (h *WindowsAnsiEventHandler) CUP(row int, col int) error {
	if err := h.prepareForCommand(true); err != nil {
		return err
	}
	rowStr, colStr := strconv.Itoa(row), strconv.Itoa(col)
	logger.Infof("CUP: [%v]", []string{rowStr, colStr})
	info, err := GetConsoleScreenBufferInfo(h.fd)
	if err != nil {
		return err
	}

	rect := info.Window
	rowS := AddInRange(SHORT(row-1), rect.Top, rect.Top, rect.Bottom)
	colS := AddInRange(SHORT(col-1), rect.Left, rect.Left, rect.Right)
	position := COORD{colS, rowS}

	return h.setCursorPosition(position, info.Size)
}

func (h *WindowsAnsiEventHandler) HVP(row int, col int) error {
	if err := h.prepareForCommand(true); err != nil {
		return err
	}
	rowS, colS := strconv.Itoa(row), strconv.Itoa(row)
	logger.Infof("HVP: [%v]", []string{rowS, colS})
	return h.CUP(row, col)
}

func (h *WindowsAnsiEventHandler) DECTCEM(visible bool) error {
	if err := h.prepareForCommand(true); err != nil {
		return err
	}
	logger.Infof("DECTCEM: [%v]", []string{strconv.FormatBool(visible)})
	return nil
}

func (h *WindowsAnsiEventHandler) ED(param int) error {
	if err := h.prepareForCommand(true); err != nil {
		return err
	}
	logger.Infof("ED: [%v]", []string{strconv.Itoa(param)})

	// [J  -- Erases from the cursor to the end of the screen, including the cursor position.
	// [1J -- Erases from the beginning of the screen to the cursor, including the cursor position.
	// [2J -- Erases the complete display. The cursor does not move.
	// [3J -- Erases the complete display and backing buffer, cursor moves to (0,0)
	// Notes:
	// -- ANSI.SYS always moved the cursor to (0,0) for both [2J and [3J
	// -- Clearing the entire buffer, versus just the Window, works best for Windows Consoles

	info, err := GetConsoleScreenBufferInfo(h.fd)
	if err != nil {
		return err
	}

	var start COORD
	var end COORD

	switch param {
	case 0:
		start = info.CursorPosition
		end = COORD{info.Size.X - 1, info.Size.Y - 1}

	case 1:
		start = COORD{0, 0}
		end = info.CursorPosition

	case 2:
		start = COORD{0, 0}
		end = COORD{info.Size.X - 1, info.Size.Y - 1}

	case 3:
		start = COORD{0, 0}
		end = COORD{info.Size.X - 1, info.Size.Y - 1}
	}

	err = h.clearRange(h.attributes, start, end)
	if err != nil {
		return err
	}

	if param == 2 || param == 3 {
		err = h.setCursorPosition(COORD{0, 0}, info.Size)
		if err != nil {
			return err
		}
	}

	return nil
}

func (h *WindowsAnsiEventHandler) EL(param int) error {
	if err := h.prepareForCommand(true); err != nil {
		return err
	}
	logger.Infof("EL: [%v]", strconv.Itoa(param))

	// [K  -- Erases from the cursor to the end of the line, including the cursor position.
	// [1K -- Erases from the beginning of the line to the cursor, including the cursor position.
	// [2K -- Erases the complete line.

	info, err := GetConsoleScreenBufferInfo(h.fd)
	if err != nil {
		return err
	}

	var start COORD
	var end COORD

	switch param {
	case 0:
		start = info.CursorPosition
		end = COORD{info.Size.X, info.CursorPosition.Y}

	case 1:
		start = COORD{0, info.CursorPosition.Y}
		end = info.CursorPosition

	case 2:
		start = COORD{0, info.CursorPosition.Y}
		end = COORD{info.Size.X, info.CursorPosition.Y}
	}

	err = h.clearRange(h.attributes, start, end)
	if err != nil {
		return err
	}

	return nil
}

func (h *WindowsAnsiEventHandler) IL(param int) error {
	if err := h.prepareForCommand(true); err != nil {
		return err
	}
	logger.Infof("IL: [%v]", strconv.Itoa(param))
	if err := h.scrollDown(param); err != nil {
		return err
	}

	return h.EL(2)
}

func (h *WindowsAnsiEventHandler) DL(param int) error {
	if err := h.prepareForCommand(true); err != nil {
		return err
	}
	logger.Infof("DL: [%v]", strconv.Itoa(param))
	return h.scrollUp(param)
}

func (h *WindowsAnsiEventHandler) SGR(params []int) error {
	// SGR does not cancel a current wrap operation
	if err := h.prepareForCommand(false); err != nil {
		return err
	}
	strings := []string{}
	for _, v := range params {
		logger.Infof("SGR: [%v]", strings)
		strings = append(strings, strconv.Itoa(v))
	}

	logger.Infof("SGR: [%v]", strings)

	if len(params) <= 0 {
		h.attributes = h.infoReset.Attributes
		h.inverted = false
	} else {
		for _, attr := range params {

			if attr == ANSI_SGR_RESET {
				h.attributes = h.infoReset.Attributes
				continue
			}

			h.attributes, h.inverted = collectAnsiIntoWindowsAttributes(h.attributes, h.inverted, h.infoReset.Attributes, SHORT(attr))
		}
	}

	attributes := h.attributes
	if h.inverted {
		attributes = invertAttributes(attributes)
	}
	err := SetConsoleTextAttribute(h.fd, attributes)
	if err != nil {
		return err
	}

	return nil
}

func (h *WindowsAnsiEventHandler) SU(param int) error {
	if err := h.prepareForCommand(true); err != nil {
		return err
	}
	logger.Infof("SU: [%v]", []string{strconv.Itoa(param)})
	return h.scrollPageUp()
}

func (h *WindowsAnsiEventHandler) SD(param int) error {
	if err := h.prepareForCommand(true); err != nil {
		return err
	}
	logger.Infof("SD: [%v]", []string{strconv.Itoa(param)})
	return h.scrollPageDown()
}

func (h *WindowsAnsiEventHandler) DA(params []string) error {
	// Since this command just affects the buffer, there is no need to call prepareForCommand

	logger.Infof("DA: [%v]", params)

	// See the site below for details of the device attributes command
	// http://vt100.net/docs/vt220-rm/chapter4.html

	// First character of first parameter string is '>'
	if params[0][0] == '>' {
		// Secondary device attribute request:
		// Respond with:
		// "I am a VT220 version 1.0, no options.
		//                    CSI     >     1     ;     1     0     ;     0     c    CR    LF
		h.buffer.Write([]byte{CSI_ENTRY, 0x3E, 0x31, 0x3B, 0x31, 0x30, 0x3B, 0x30, 0x63, 0x0D, 0x0A})

	} else {
		// Primary device attribute request:
		// Respond with:
		// "I am a service class 2 terminal (62) with 132 columns (1),
		// printer port (2), selective erase (6), DRCS (7), UDK (8),
		// and I support 7-bit national replacement character sets (9)."
		//                    CSI     ?     6     2     ;     1     ;     2     ;     6     ;     7     ;     8     ;     9     c    CR    LF
		h.buffer.Write([]byte{CSI_ENTRY, 0x3F, 0x36, 0x32, 0x3B, 0x31, 0x3B, 0x32, 0x3B, 0x36, 0x3B, 0x37, 0x3B, 0x38, 0x3B, 0x39, 0x63, 0x0D, 0x0A})
	}

	return nil
}

func (h *WindowsAnsiEventHandler) DECSTBM(top int, bottom int) error {
	if err := h.prepareForCommand(true); err != nil {
		return err
	}
	logger.Infof("DECSTBM: [%d, %d]", top, bottom)

	// Windows is 0 indexed, Linux is 1 indexed
	h.sr.top = SHORT(top - 1)
	h.sr.bottom = SHORT(bottom - 1)

	return nil
}

func (h *WindowsAnsiEventHandler) effectiveSr(window SMALL_RECT) scrollRegion {
    top := AddInRange(window.Top, h.sr.top, window.Top, window.Bottom)
    bottom := AddInRange(window.Top, h.sr.bottom, window.Top, window.Bottom)
    if top >= bottom {
        top = window.Top
        bottom = window.Bottom
    }
    return scrollRegion{top: top, bottom: bottom}
}

func (h *WindowsAnsiEventHandler) RI() error {
	if err := h.prepareForCommand(true); err != nil {
		return err
	}
	logger.Info("RI: []")

	info, err := GetConsoleScreenBufferInfo(h.fd)
	if err != nil {
		return err
	}

	if info.Window.Top == info.CursorPosition.Y {
		if err := h.scrollPageDown(); err != nil {
			return err
		}

		return h.EL(2)
	} else {
		return h.CUU(1)
	}
}

func (h *WindowsAnsiEventHandler) Flush() error {
	if h.buffer.Len() == 0 || (h.wrapNext && h.buffer.Len() == 1) {
        h.curInfo = nil
		return nil
	}
    
    if err := h.cacheInfo(); err != nil {
        return nil
    }

	logger.Infof("Flush: [%s]", h.buffer.Bytes())

	// Write everything but the last byte.
	bytes := h.buffer.Bytes()
	wrapIndex := len(bytes) - 1
	if len(bytes) > 1 {
		if _, err := h.file.Write(bytes[:wrapIndex]); err != nil {
			return err
		}
	}

    info, err := GetConsoleScreenBufferInfo(h.fd)
    if err != nil {
        return err
    }

    if info.CursorPosition.X == info.Size.X-1 {
		// Output the last byte to the screen without adjusting the cursor position.
		wrapByte := bytes[wrapIndex]
        logger.Infof("output trailing character '%c' to avoid wrap", wrapByte)
		charInfo := []CHAR_INFO{{UnicodeChar: WCHAR(wrapByte), Attributes: info.Attributes}}
		size := COORD{1, 1}
		position := COORD{0, 0}
		region := SMALL_RECT{Left: info.CursorPosition.X, Top: info.CursorPosition.Y, Right: info.CursorPosition.X, Bottom: info.CursorPosition.Y}
		if err := WriteConsoleOutput(h.fd, charInfo, size, position, &region); err != nil {
			return err
		}

		// Save the last byte to write again with normal wrapping next time there is
		// a byte is printed.
		h.buffer.Reset()
		h.buffer.WriteByte(wrapByte)
		h.wrapNext = true
	} else {
		if _, err := h.file.Write(bytes[wrapIndex:]); err != nil {
			return err
		}
		h.buffer.Reset()
		h.wrapNext = false
	}
	return nil
}

func (h *WindowsAnsiEventHandler) prepareForCommand(resetWrap bool) error {
	if err := h.Flush(); err != nil {
		return err
	}
	if resetWrap && h.wrapNext {
		// Skip the wrap byte -- it has already been written to the screen
		// and should not be rewritten now that auto-wrap is being reset.
		h.buffer.ReadByte()
		h.wrapNext = false
	}
	return nil
}

func (h *WindowsAnsiEventHandler) cacheInfo() error {
    if h.curInfo == nil {
        info, err := GetConsoleScreenBufferInfo(h.fd)
        if err != nil {
            return err
        }
        h.curInfo = info
        h.curPos = info.CursorPosition
    }    
    return nil
}
