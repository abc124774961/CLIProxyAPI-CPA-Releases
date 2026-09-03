package licensing

import (
	"encoding/json"
	"io"
)

func marshalJSON(v any) ([]byte, error)   { return json.Marshal(v) }
func decodeJSON(data []byte, v any) error { return json.Unmarshal(data, v) }
func decodeJSONReader(r io.Reader, v any) error {
	return json.NewDecoder(r).Decode(v)
}

type rawMessage = json.RawMessage
