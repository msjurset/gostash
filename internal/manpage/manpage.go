package manpage

import _ "embed"

//go:generate cp ../../stash.1 stash.1

//go:embed stash.1
var Content string
