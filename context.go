package ansiterm

import (
	"bytes"
)

type AnsiContext struct {
	currentChar byte
	paramBuffer []byte
	interBuffer []byte
	printBuffer bytes.Buffer
}
