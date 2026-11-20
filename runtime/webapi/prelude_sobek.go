//go:build !ekuma

package webapi

import _ "embed"

//go:embed prelude.js
var preludeSource string
