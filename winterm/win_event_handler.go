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
	fd             uintptr
	file           *os.File
	infoReset      *CONSOLE_SCREEN_BUFFER_INFO
	sr             scrollRegion
	buffer         bytes.Buffer
	wrapNext       bool
	drewMarginByte bool
	inverted       bool
	attributes     WORD
	marginByte     byte
	curInfo        *CONSOLE_SCREEN_BUFFER_INFO
	curPos         COORD
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
		fd:         fd,
		file:       file,
		infoReset:  infoReset,
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
	if h.wrapNext {
		if err := h.Flush(); err != nil {
			return false, err
		}
		h.clearWrap()
	}
	pos, info, err := h.getCurrentInfo()
	if err != nil {
		return false, err
	}
	sr := h.effectiveSr(info.Window)
	if pos.Y == sr.bottom {
		// Scrolling is necessary. Let Windows automatically scroll if the scrolling region
		// is the full window.
		if sr.top == info.Window.Top && sr.bottom == info.Window.Bottom {
			if includeCR {
				pos.X = 0
				h.updatePos(pos)
			}
			return false, nil
		} else {
			// A custom scroll region is active. Scroll the window manually to simulate
			// the LF.
			if err := h.Flush(); err != nil {
				return false, err
			}
			if err := h.scrollUp(1); err != nil {
				return false, err
			}
			if includeCR {
				pos.X = 0
				if err := SetConsoleCursorPosition(h.fd, pos); err != nil {
					return false, err
				}
			}
			return true, nil
		}
	} else if pos.Y < info.Window.Bottom {
		// Let Windows handle the LF.
		pos.Y++
		if includeCR {
			pos.X = 0
		}
		h.updatePos(pos)
		return false, nil
	} else {
		// The cursor is at the bottom of the screen but outside the scroll
		// region. Skip the LF.
		if includeCR {
			if err := h.Flush(); err != nil {
				return false, err
			}
			pos.X = 0
			if err := SetConsoleCursorPosition(h.fd, pos); err != nil {
				return false, err
			}
		}
		return true, nil
	}
}

func (h *WindowsAnsiEventHandler) Print(b byte) error {
	if h.wrapNext {
		h.buffer.WriteByte(h.marginByte)
		h.clearWrap()
		if _, err := h.simulateLF(true); err != nil {
			return err
		}
	}
	pos, info, err := h.getCurrentInfo()
	if err != nil {
		return err
	}
	if pos.X == info.Size.X-1 {
		h.wrapNext = true
		h.marginByte = b
	} else {
		pos.X++
		h.updatePos(pos)
		h.buffer.WriteByte(b)
	}
	return nil
}

func (h *WindowsAnsiEventHandler) Execute(b byte) error {
	switch b {
	case ANSI_TAB:
		// Move to the next tab stop, but preserve auto-wrap if already set.
		if !h.wrapNext {
			pos, info, err := h.getCurrentInfo()
			if err != nil {
				return err
			}
			pos.X = (pos.X + 8) - pos.X%8
			if pos.X >= info.Size.X {
				pos.X = info.Size.X - 1
			}
			if err := h.Flush(); err != nil {
				return err
			}
			if err := SetConsoleCursorPosition(h.fd, pos); err != nil {
				return err
			}
		}
		return nil

	case ANSI_BEL:
		h.buffer.WriteByte(ANSI_BEL)
		return nil

	case ANSI_BACKSPACE:
		if h.wrapNext {
			if err := h.Flush(); err != nil {
				return err
			}
			h.clearWrap()
		}
		pos, _, err := h.getCurrentInfo()
		if err != nil {
			return err
		}
		if pos.X > 0 {
			pos.X--
			h.updatePos(pos)
			h.buffer.WriteByte(ANSI_BACKSPACE)
		}
		return nil

	case ANSI_VERTICAL_TAB, ANSI_FORM_FEED:
		// Treat as true LF.
		return h.IND()

	case ANSI_LINE_FEED:
		// Simulate a CR and LF for now since there is no way in go-ansiterm
		// to tell if the LF should include CR (and more things break when it's
		// missing than when it's incorrectly added).
		handled, err := h.simulateLF(true)
		if handled || err != nil {
			return err
		}
		return h.buffer.WriteByte(ANSI_LINE_FEED)

	case ANSI_CARRIAGE_RETURN:
		if h.wrapNext {
			if err := h.Flush(); err != nil {
				return err
			}
			h.clearWrap()
		}
		pos, _, err := h.getCurrentInfo()
		if err != nil {
			return err
		}
		if pos.X != 0 {
			pos.X = 0
			h.updatePos(pos)
			h.buffer.WriteByte(ANSI_CARRIAGE_RETURN)
		}
		return nil

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
	h.clearWrap()
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
	logger.Infof("CUP: [[%d %d]]", row, col)
	h.clearWrap()
	info, err := GetConsoleScreenBufferInfo(h.fd)
	if err != nil {
		return err
	}

	rect := info.Window
	rowS := AddInRange(SHORT(row-1), rect.Top, rect.Top, rect.Bottom)
	colS := ensureInRange(SHORT(col-1), 0, info.Size.X-1)
	position := COORD{colS, rowS}

	return h.setCursorPosition(position, info.Size)
}

func (h *WindowsAnsiEventHandler) HVP(row int, col int) error {
	if err := h.Flush(); err != nil {
		return err
	}
	logger.Infof("HVP: [[%d %d]]", row, col)
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
	logger.Infof("DA: [%v]", params)
	// DA cannot be implemented because it must send data on the VT100 input stream,
	// which is not available to go-ansiterm.
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

	sr := h.effectiveSr(info.Window)
	if info.CursorPosition.Y == sr.top {
		return h.scrollDown(1)
	} else {
		return h.moveCursorVertical(-1)
	}
}

func (h *WindowsAnsiEventHandler) IND() error {
	logger.Info("IND: []")

	handled, err := h.simulateLF(false)
	if err != nil {
		return err
	}
	if !handled {
		// Windows LF will reset the cursor column position. Write the LF
		// and restore the cursor position.
		pos, _, err := h.getCurrentInfo()
		if err != nil {
			return err
		}
		h.buffer.WriteByte(ANSI_LINE_FEED)
		if pos.X != 0 {
			if err := h.Flush(); err != nil {
				return err
			}
			if err := SetConsoleCursorPosition(h.fd, pos); err != nil {
				return err
			}
		}
	}
	return nil
}

func (h *WindowsAnsiEventHandler) Flush() error {
	h.curInfo = nil
	if h.buffer.Len() > 0 {
		logger.Infof("Flush: [%s]", h.buffer.Bytes())
		_, err := h.buffer.WriteTo(h.file)
		if err != nil {
			return err
		}
	}

	if h.wrapNext && !h.drewMarginByte {
		logger.Infof("Flush: drawing margin byte '%c'", h.marginByte)

		info, err := GetConsoleScreenBufferInfo(h.fd)
		if err != nil {
			return err
		}

		charInfo := []CHAR_INFO{{UnicodeChar: WCHAR(h.marginByte), Attributes: info.Attributes}}
		size := COORD{1, 1}
		position := COORD{0, 0}
		region := SMALL_RECT{Left: info.CursorPosition.X, Top: info.CursorPosition.Y, Right: info.CursorPosition.X, Bottom: info.CursorPosition.Y}
		if err := WriteConsoleOutput(h.fd, charInfo, size, position, &region); err != nil {
			return err
		}
		h.drewMarginByte = true
	}
	return nil
}

// cacheConsoleInfo ensures that the current console screen information has been queried
// since the last call to Flush(). It must be called before accessing h.curInfo or h.curPos.
func (h *WindowsAnsiEventHandler) getCurrentInfo() (COORD, *CONSOLE_SCREEN_BUFFER_INFO, error) {
	if h.curInfo == nil {
		info, err := GetConsoleScreenBufferInfo(h.fd)
		if err != nil {
			return COORD{}, nil, err
		}
		h.curInfo = info
		h.curPos = info.CursorPosition
	}
	return h.curPos, h.curInfo, nil
}

func (h *WindowsAnsiEventHandler) updatePos(pos COORD) {
	if h.curInfo == nil {
		panic("failed to call getCurrentInfo before calling updatePos")
	}
	h.curPos = pos
}

// clearWrap clears the state where the cursor is in the margin
// waiting for the next character before wrapping the line. This must
// be done before most operations that act on the cursor.
func (h *WindowsAnsiEventHandler) clearWrap() {
	h.wrapNext = false
	h.drewMarginByte = false
}
