package platformio

import "errors"

var (
	errInvalidTTL        = errors.New("ttl 必须大于 0")
	errInvalidMaxEntries = errors.New("maxEntries 必须大于 0")
)
