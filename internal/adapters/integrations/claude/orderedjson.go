package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// object is a JSON object that keeps its keys in file order and its values
// as raw JSON, so editing one key leaves the rest of the user's settings
// exactly as they were.
type object struct {
	members []member
}

type member struct {
	key string
	val json.RawMessage
}

func decodeObject(b []byte) (*object, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	if err := expectDelim(dec, '{'); err != nil {
		return nil, err
	}
	o := &object{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, errors.New("claude: object key is not a string")
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		o.members = append(o.members, member{key: key, val: raw})
	}
	if err := expectDelim(dec, '}'); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("claude: trailing data after JSON object")
	}
	return o, nil
}

func (o *object) get(key string) (json.RawMessage, bool) {
	for _, m := range o.members {
		if m.key == key {
			return m.val, true
		}
	}
	return nil, false
}

// set replaces key in place, or appends it at the end.
func (o *object) set(key string, val json.RawMessage) {
	for i, m := range o.members {
		if m.key == key {
			o.members[i].val = val
			return
		}
	}
	o.members = append(o.members, member{key: key, val: val})
}

func (o *object) del(key string) {
	out := o.members[:0]
	for _, m := range o.members {
		if m.key != key {
			out = append(out, m)
		}
	}
	o.members = out
}

func (o *object) len() int { return len(o.members) }

// compact encodes the object without insignificant whitespace.
func (o *object) compact() (json.RawMessage, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range o.members {
		if i > 0 {
			b.WriteByte(',')
		}
		k, err := marshal(m.key)
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		if err := json.Compact(&b, m.val); err != nil {
			return nil, fmt.Errorf("claude: value of %q: %w", m.key, err)
		}
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func decodeArray(b json.RawMessage) ([]json.RawMessage, error) {
	var arr []json.RawMessage
	if err := json.Unmarshal(b, &arr); err != nil {
		return nil, err
	}
	return arr, nil
}

func compactArray(arr []json.RawMessage) (json.RawMessage, error) {
	var b bytes.Buffer
	b.WriteByte('[')
	for i, v := range arr {
		if i > 0 {
			b.WriteByte(',')
		}
		if err := json.Compact(&b, v); err != nil {
			return nil, err
		}
	}
	b.WriteByte(']')
	return b.Bytes(), nil
}

// marshal encodes v without HTML escaping, so commands with < > & stay
// readable in the user's settings file.
func marshal(v any) (json.RawMessage, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// pretty formats compact JSON with two-space indentation and a trailing
// newline, the format Claude Code itself writes.
func pretty(compact json.RawMessage) ([]byte, error) {
	var b bytes.Buffer
	if err := json.Indent(&b, compact, "", "  "); err != nil {
		return nil, err
	}
	b.WriteByte('\n')
	return b.Bytes(), nil
}
