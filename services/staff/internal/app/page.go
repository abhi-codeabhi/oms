package app

import "strconv"

// encodeToken / decodeToken implement a trivial numeric-offset page token. The
// roster is small per outlet; offset paging is sufficient and keeps the wire
// token opaque to the caller.
func encodeToken(offset int) string {
	if offset <= 0 {
		return ""
	}
	return strconv.Itoa(offset)
}

func decodeToken(token string) int {
	if token == "" {
		return 0
	}
	n, err := strconv.Atoi(token)
	if err != nil || n < 0 {
		return 0
	}
	return n
}
