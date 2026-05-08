package main

import (
	"encoding/json"
	"io"
)

func jsonDecodeStream(r io.Reader, v any) error {
	return json.NewDecoder(r).Decode(v)
}
