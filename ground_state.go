package ansiterm

type GroundState struct {
	BaseState
}

func (gs GroundState) Handle(b byte) (s State, e error) {
	gs.parser.context.currentChar = b

	nextState, err := gs.BaseState.Handle(b)
	if nextState != nil || err != nil {
		return nextState, err
	}

	switch {
	case sliceContains(Printables, b):
		return gs, gs.parser.context.printBuffer.WriteByte(b)

	case sliceContains(Executors, b):
		// flush any unwritten printable bytes before executing this byte
		if err = gs.Flush(); err == nil {
			err = gs.parser.execute()
		}
		return gs, err
	}

	return gs, nil
}

func (gs GroundState) Flush() error {
	// flush any printable bytes
	return gs.parser.print()
}
