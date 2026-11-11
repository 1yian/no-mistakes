package intent

import "regexp"

var filePathTokens = regexp.MustCompile(`[A-Za-z0-9_./\\\-]+\.[A-Za-z0-9]{1,8}`)
