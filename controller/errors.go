package controller

import "errors"

// errBadJSON 请求体不是合法 JSON。
var errBadJSON = errors.New("request body must be a valid JSON object")
