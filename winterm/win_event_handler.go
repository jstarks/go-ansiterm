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
	fd          uintptr
	file        *os.File
	infoReset   *CONSOLE_SCREEN_BUFFER_INFO
	sr          scrollRegion
	buffer      bytes.Buffer
	wasInMargin bool
	curInfo     *CONSOLE_SCREEN_BUFFER_INFO
	curPos      COORD
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

// simulateLF simulates a LF or CR+LF by scrolling if necessary to handle the
// current cursor position and scroll region settings, in which case it returns
// true. If no special handling is necessary, then it does nothing and returns
// false.
//
// In the false case, the caller should ensure that a carriage return
// and line feed are inserted or that the text is otherwise wrapped.
func (h *WindowsAnsiEventHandler) simulateLF(includeCR bool) (bool, error) {
	if err := h.cacheInfo(); err != nil {
		return false, err
	}
	if includeCR {
		h.curPos.X = 0
	}
	sr, srActive := h.effectiveSr(h.curInfo.Window)
	if h.curPos.Y == sr.bottom {
		// Scrolling is necessary. Let Windows automatically scroll if the scrolling region
		// is the full window.
		if !srActive {
			return false, nil
		} else {
			// A custom scroll region is active. Scroll the window manually to simulate
			// the LF.
			h.clearWrap()
			newPos := h.curPos
			if err := h.Flush(); err != nil {
				return false, err
			}
			if err := h.scrollUp(1); err != nil {
				return false, err
			}
			if includeCR {
				if err := SetConsoleCursorPosition(h.fd, newPos); err != nil {
					return false, err
				}
			}
			return true, nil
		}
	} else if h.curPos.Y < h.curInfo.Window.Bottom {
		// Let Windows handle the LF.
		h.curPos.Y++
		return false, nil
	} else {
		// The cursor is at the bottom of the screen but outside the scroll
		// region. Skip the LF.
		if includeCR {
			newPos := h.curPos
			h.clearWrap()
			if err := h.Flush(); err != nil {
				return false, err
			}
			if err := SetConsoleCursorPosition(h.fd, newPos); err != nil {
				return false, err
			}
		}
		return true, nil
	}

}

func (h *WindowsAnsiEventHandler) Print(b byte) error {
	if err := h.cacheInfo(); err != nil {
		return err
	}
	// If the column is already in the "wrap" position, then
	// simulate a CRLF (which may flush the buffer or just allow
	// Windows to wrap automatically).
	if h.curPos.X == h.curInfo.Size.X {
		if _, err := h.simulateLF(true); err != nil {
			return err
		}
		// Re-establish the cached position information.
		if err := h.cacheInfo(); err != nil {
			return err
		}
	}
	h.curPos.X++
	return h.buffer.WriteByte(b)
}

func (h *WindowsAnsiEventHandler) Execute(b byte) error {
	switch b {
	case ANSI_TAB:
		if err := h.cacheInfo(); err != nil {
			return err
		}
		// Move to the next tab stop, but stay in the margin if already there.
		if h.curPos.X != h.curInfo.Size.X {
			newPos := h.curPos
			newPos.X = (newPos.X + 8) - newPos.X%8
			if newPos.X >= h.curInfo.Size.X {
				newPos.X = h.curInfo.Size.X - 1
			}
			if err := h.Flush(); err != nil {
				return err
			}
			if err := SetConsoleCursorPosition(h.fd, newPos); err != nil {
				return err
			}
		}
		return nil

	case ANSI_BEL:
		// BEL preserves auto-wrap, which is tricky with our buffering strategy.
		// Just flush and write the byte directly.
		if err := h.Flush(); err != nil {
			return err
		}
		_, err := h.file.Write([]byte{ANSI_BEL})
		return err

	case ANSI_BACKSPACE:
		h.clearWrap()
		if err := h.cacheInfo(); err != nil {
			return err
		}
		if h.curPos.X > 0 {
			h.curPos.X--
		}
		return h.buffer.WriteByte(b)

	case ANSI_VERTICAL_TAB, ANSI_FORM_FEED:
		// Treat as true LF.
		return h.IND()

	case ANSI_LINE_FEED:
		// Simulate a CR and LF for now since there is no way in go-ansiterm
		// to tell if the LF should include CR (and more things break when it's
		// missing than when it's incorrectly added).
		h.clearWrap()
		handled, err := h.simulateLF(true)
		if handled || err != nil {
			return err
		}
		return h.buffer.WriteByte(b)

	case ANSI_CARRIAGE_RETURN:
		h.clearWrap()
		if err := h.cacheInfo(); err != nil {
			return err
		}
		h.curPos.X = 0
		return h.buffer.WriteByte(b)

	default:
		return nil
	}
}

func (h *WindowsAnsiEventHandler) CUU(param int) error {
	if err := h.Flush(); err != nil {
		return err
	}
	logger.Infof("CUU: [%v]", []string{strconv.Itoa(param)})
	h.clearWrap()
	return h.moveCursorVertical(-param)
}

func (h *WindowsAnsiEventHandler) CUD(param int) error {
	if err := h.Flush(); err != nil {
		return err
	}
	logger.Infof("CUD: [%v]", []string{strconv.Itoa(param)})
	h.clearWrap()
	return h.moveCursorVertical(param)
}

func (h *WindowsAnsiEventHandler) CUF(param int) error {
	if err := h.Flush(); err != nil {
		return err
	}
	logger.Infof("CUF: [%v]", []string{strconv.Itoa(param)})
	h.clearWrap()
	return h.moveCursorHorizontal(param)
}

func (h *WindowsAnsiEventHandler) CUB(param int) error {
	if err := h.Flush(); err != nil {
		return err
	}
	logger.Infof("CUB: [%v]", []string{strconv.Itoa(param)})
	h.clearWrap()
	return h.moveCursorHorizontal(-param)
}

func (h *WindowsAnsiEventHandler) CNL(param int) error {
	if err := h.Flush(); err != nil {
		return err
	}
	logger.Infof("CNL: [%v]", []string{strconv.Itoa(param)})
	h.clearWrap()
	return h.moveCursorLine(param)
}

func (h *WindowsAnsiEventHandler) CPL(param int) error {
	if err := h.Flush(); err != nil {
		return err
	}
	logger.Infof("CPL: [%v]", []string{strconv.Itoa(param)})
	h.clearWrap()
	return h.moveCursorLine(-param)
}

func (h *WindowsAnsiEventHandler) CHA(param int) error {
	if err := h.Flush(); err != nil {
		return err
	}
	logger.Infof("CHA: [%v]", []string{strconv.Itoa(param)})
	h.clearWrap()
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
	if err := h.Flush(); err != nil {
		return err
	}
	logger.Infof("CUP: [%d %d]", row, col)
	h.clearWrap()
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
	if err := h.Flush(); err != nil {
		return err
	}
	logger.Infof("HVP: [%d %d]", row, col)
	h.clearWrap()
	return h.CUP(row, col)
}

func (h *WindowsAnsiEventHandler) DECTCEM(visible bool) error {
	if err := h.Flush(); err != nil {
		return err
	}
	logger.Infof("DECTCEM: [%v]", []string{strconv.FormatBool(visible)})
	h.clearWrap()
	return nil
}

func (h *WindowsAnsiEventHandler) ED(param int) error {
	if err := h.Flush(); err != nil {
		return err
	}
	logger.Infof("ED: [%v]", []string{strconv.Itoa(param)})
	h.clearWrap()

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
	if err := h.Flush(); err != nil {
		return err
	}
	logger.Infof("EL: [%v]", strconv.Itoa(param))
	h.clearWrap()

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
	if err := h.Flush(); err != nil {
		return err
	}
	logger.Infof("IL: [%v]", strconv.Itoa(param))
	h.clearWrap()
	if err := h.scrollDown(param); err != nil {
		return err
	}

	return h.EL(2)
}

func (h *WindowsAnsiEventHandler) DL(param int) error {
	if err := h.Flush(); err != nil {
		return err
	}
	logger.Infof("DL: [%v]", strconv.Itoa(param))
	h.clearWrap()
	return h.scrollUp(param)
}

func (h *WindowsAnsiEventHandler) SGR(params []int) error {
	if err := h.Flush(); err != nil {
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
	if err := h.Flush(); err != nil {
		return err
	}
	logger.Infof("SU: [%v]", []string{strconv.Itoa(param)})
	h.clearWrap()
	return h.scrollPageUp()
}

func (h *WindowsAnsiEventHandler) SD(param int) error {
	if err := h.Flush(); err != nil {
		return err
	}
	logger.Infof("SD: [%v]", []string{strconv.Itoa(param)})
	h.clearWrap()
	return h.scrollPageDown()
}

func (h *WindowsAnsiEventHandler) DA(params []string) error {
	// Since this command just affects the buffer, there is no need to call Flush

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
	if err := h.Flush(); err != nil {
		return err
	}
	logger.Infof("DECSTBM: [%d, %d]", top, bottom)

	// Windows is 0 indexed, Linux is 1 indexed
	h.sr.top = SHORT(top - 1)
	h.sr.bottom = SHORT(bottom - 1)

	// This command also moves the cursor to the origin.
	h.clearWrap()
	return SetConsoleCursorPosition(h.fd, COORD{0, 0})
}

func (h *WindowsAnsiEventHandler) effectiveSr(window SMALL_RECT) (scrollRegion, bool) {
	top := AddInRange(window.Top, h.sr.top, window.Top, window.Bottom)
	bottom := AddInRange(window.Top, h.sr.bottom, window.Top, window.Bottom)
	if top >= bottom {
		top = window.Top
		bottom = window.Bottom
	}
	return scrollRegion{top: top, bottom: bottom}, top != window.Top || bottom != window.Bottom
}

func (h *WindowsAnsiEventHandler) RI() error {
	if err := h.Flush(); err != nil {
		return err
	}
	logger.Info("RI: []")
	h.clearWrap()

	info, err := GetConsoleScreenBufferInfo(h.fd)
	if err != nil {
		return err
	}

	sr, _ := h.effectiveSr(info.Window)
	if info.CursorPosition.Y == sr.top {
		return h.scrollDown(1)
	} else {
		return h.moveCursorVertical(-1)
	}
}

func (h *WindowsAnsiEventHandler) IND() error {
	logger.Info("IND: []")

	h.clearWrap()
	handled, err := h.simulateLF(false)
	if err != nil {
		return err
	}
	if !handled {
		// The LF was not simulated, so send it through to Windows.
		if err := h.cacheInfo(); err != nil {
			return err
		}
		h.buffer.WriteByte('\n')
		// Windows LF will reset the cursor column position. Update it
		// to its old position.
		if h.curPos.X != 0 {
			newPos := h.curPos
			if err := h.Flush(); err != nil {
				return err
			}
			h.cacheInfo()
			if err := SetConsoleCursorPosition(h.fd, newPos); err != nil {
				return err
			}
		}
	}
	return nil
}

func (h *WindowsAnsiEventHandler) bufferEmpty() bool {
	return h.buffer.Len() == 0 || h.wasInMargin && h.buffer.Len() == 1
}

func (h *WindowsAnsiEventHandler) Flush() error {
	if h.bufferEmpty() {
		h.curInfo = nil
		return nil
	}

	logger.Infof("Flush: [%s]", h.buffer.Bytes())

	if err := h.cacheInfo(); err != nil {
		return nil
	}
	bytes := h.buffer.Bytes()

	var (
		writeCount  int
		wasInMargin bool
	)
	if h.curPos.X == h.curInfo.Size.X {
		// We are in the margin. Write everything but the last byte (which
		// by construction must be a printable character).
		writeCount = len(bytes) - 1
		wasInMargin = true
	} else {
		writeCount = len(bytes)
	}

	if writeCount > 0 {
		if _, err := h.file.Write(bytes[:writeCount]); err != nil {
			return err
		}
	}

	if wasInMargin {
		// Output the last byte to the screen without adjusting the cursor position.
		wrapByte := bytes[writeCount]
		logger.Infof("output trailing character '%c' to avoid wrap", wrapByte)
		charInfo := []CHAR_INFO{{UnicodeChar: WCHAR(wrapByte), Attributes: h.curInfo.Attributes}}
		size := COORD{1, 1}
		position := COORD{0, 0}
		region := SMALL_RECT{Left: h.curPos.X - 1, Top: h.curPos.Y, Right: h.curPos.X - 1, Bottom: h.curPos.Y}
		if err := WriteConsoleOutput(h.fd, charInfo, size, position, &region); err != nil {
			return err
		}

		// Save the last byte to write again with normal wrapping next time there is
		// a byte is printed.
		h.buffer.Reset()
		h.buffer.WriteByte(wrapByte)
	} else {
		h.buffer.Reset()
	}
	h.wasInMargin = wasInMargin
	h.curInfo = nil
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
		if h.wasInMargin && h.curPos.X == h.curInfo.Size.X-1 {
			h.curPos.X = h.curInfo.Size.X
		}
	}
	return nil
}

func (h *WindowsAnsiEventHandler) clearWrap() {
	if !h.bufferEmpty() {
		if h.curInfo != nil && h.curPos.X == h.curInfo.Size.X {
			h.curPos.X = h.curInfo.Size.X - 1
		}
	} else if h.wasInMargin {
		// Skip the wrap byte -- it has already been written to the screen
		// and should not be rewritten now that auto-wrap is being reset.
		h.buffer.ReadByte()
		h.wasInMargin = false
	}
}
