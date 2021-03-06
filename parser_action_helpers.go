package ansiterm

import (
	"strconv"
)

func parseParams(bytes []byte) ([]string, error) {
	paramBuff := make([]byte, 0, 0)
	params := []string{}

	for _, v := range bytes {
		if v == ';' {
			if len(paramBuff) > 0 {
				// Completed parameter, append it to the list
				s := string(paramBuff)
				params = append(params, s)
				paramBuff = make([]byte, 0, 0)
			}
		} else {
			paramBuff = append(paramBuff, v)
		}
	}

	// Last parameter may not be terminated with ';'
	if len(paramBuff) > 0 {
		s := string(paramBuff)
		params = append(params, s)
	}

	logger.Infof("Parsed params: %v with length: %d", params, len(params))
	return params, nil
}

func parseCmd(context AnsiContext) (string, error) {
	return string(context.currentChar), nil
}

func getInt(params []string, dflt int) int {
	i := getInts(params, 1, dflt)[0]
	logger.Infof("getInt: %v", i)
	return i
}

func getInts(params []string, minCount int, dflt int) []int {
	ints := []int{}

	for _, v := range params {
		i, _ := strconv.Atoi(v)
		ints = append(ints, i)
	}

	if len(ints) < minCount {
		remaining := minCount - len(ints)
		for i := 0; i < remaining; i++ {
			ints = append(ints, dflt)
		}
	}

	logger.Infof("getInts: %v", ints)

	return ints
}

func (ap *AnsiParser) hDispatch(params []string) error {
	if len(params) == 1 && params[0] == "?25" {
		return ap.eventHandler.DECTCEM(true)
	}

	return nil
}

func (ap *AnsiParser) lDispatch(params []string) error {
	if len(params) == 1 && params[0] == "?25" {
		return ap.eventHandler.DECTCEM(false)
	}

	return nil
}

func getEraseParam(params []string) int {
	param := getInt(params, 0)
	if param < 0 || 3 < param {
		param = 0
	}

	return param
}
