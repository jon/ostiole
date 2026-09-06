package main

import "github.com/jon/ostiole/armdebug"

func main() {
	var c armdebug.Conn
	_ = c.Close()
}
