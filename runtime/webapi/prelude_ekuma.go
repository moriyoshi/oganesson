//go:build ekuma

package webapi

import _ "embed"

//go:embed prelude-ekuma.js
var preludeSource string
