package common

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
)

// JSONTokenDecoder walks JSON structure with a bounded buffer. Skip validates
// strings and nested values without materializing them, so Base64 payloads can
// be inspected for routing without allocating a second request-sized string.
// Scalar decoding remains in the host codec; errors never include payload data.
type JSONTokenDecoder struct {
	reader   *bufio.Reader
	stack    []jsonStreamState
	rootRead bool
	err      error
}
type jsonStreamState uint8

const (
	jsonObjectFirst jsonStreamState = iota
	jsonObjectKey
	jsonObjectValue
	jsonObjectAfter
	jsonArrayFirst
	jsonArrayValue
	jsonArrayAfter
)

func NewJSONTokenDecoder(reader io.Reader) *JSONTokenDecoder {
	return &JSONTokenDecoder{reader: bufio.NewReaderSize(reader, 4096)}
}
func (d *JSONTokenDecoder) peek() (byte, error) {
	for {
		b, err := d.reader.Peek(1)
		if err != nil {
			return 0, err
		}
		switch b[0] {
		case ' ', '\t', '\r', '\n':
			_, _ = d.reader.ReadByte()
		default:
			return b[0], nil
		}
	}
}
func (d *JSONTokenDecoder) More() bool {
	b, err := d.peek()
	if err != nil {
		d.err = err
		return false
	}
	return b != '}' && b != ']'
}
func (d *JSONTokenDecoder) Token() (json.Token, error) { return d.token(true) }

// StructuralToken consumes one token without retaining string/number payloads.
// Delimiters, booleans and null are preserved for structural inspection.
func (d *JSONTokenDecoder) StructuralToken() (json.Token, error) { return d.token(false) }
func (d *JSONTokenDecoder) invalid() (json.Token, error) {
	d.err = errors.New("invalid JSON structure")
	return nil, d.err
}
func (d *JSONTokenDecoder) token(capture bool) (json.Token, error) {
	if d.err != nil {
		return nil, d.err
	}
	b, err := d.peek()
	if err != nil {
		return nil, err
	}
	if len(d.stack) == 0 {
		if d.rootRead {
			return d.invalid()
		}
		d.rootRead = true
	} else {
		index := len(d.stack) - 1
		state := d.stack[index]
		switch state {
		case jsonObjectAfter, jsonArrayAfter:
			end := byte('}')
			if state == jsonArrayAfter {
				end = ']'
			}
			if b == end {
				_, _ = d.reader.ReadByte()
				d.stack = d.stack[:index]
				return json.Delim(end), nil
			}
			if b != ',' {
				return d.invalid()
			}
			_, _ = d.reader.ReadByte()
			b, err = d.peek()
			if err != nil {
				return nil, err
			}
			if state == jsonObjectAfter {
				state = jsonObjectKey
			} else {
				state = jsonArrayValue
			}
		case jsonObjectValue:
			if b != ':' {
				return d.invalid()
			}
			_, _ = d.reader.ReadByte()
			b, err = d.peek()
			if err != nil {
				return nil, err
			}
		}
		switch state {
		case jsonObjectFirst:
			if b == '}' {
				_, _ = d.reader.ReadByte()
				d.stack = d.stack[:index]
				return json.Delim('}'), nil
			}
			fallthrough
		case jsonObjectKey:
			if b != '"' {
				return d.invalid()
			}
			value, err := d.stringToken(capture)
			d.stack[index] = jsonObjectValue
			return value, err
		case jsonArrayFirst:
			if b == ']' {
				_, _ = d.reader.ReadByte()
				d.stack = d.stack[:index]
				return json.Delim(']'), nil
			}
			fallthrough
		case jsonArrayValue:
			d.stack[index] = jsonArrayAfter
		case jsonObjectValue:
			d.stack[index] = jsonObjectAfter
		}
	}
	switch b {
	case '{', '[':
		_, _ = d.reader.ReadByte()
		if len(d.stack) >= 10000 {
			return d.invalid()
		}
		state := jsonObjectFirst
		if b == '[' {
			state = jsonArrayFirst
		}
		d.stack = append(d.stack, state)
		return json.Delim(b), nil
	case '"':
		return d.stringToken(capture)
	case 't', 'f', 'n':
		literal := "true"
		var value any = true
		if b == 'f' {
			literal = "false"
			value = false
		}
		if b == 'n' {
			literal = "null"
			value = nil
		}
		for i := range len(literal) {
			actual, err := d.reader.ReadByte()
			if err != nil {
				return nil, err
			}
			if actual != literal[i] {
				return d.invalid()
			}
		}
		return value, nil
	default:
		return d.numberToken(capture)
	}
}
func (d *JSONTokenDecoder) stringToken(capture bool) (string, error) {
	_, _ = d.reader.ReadByte()
	var raw []byte
	if capture {
		raw = append(raw, '"')
	}
	for {
		b, err := d.reader.ReadByte()
		if err != nil {
			return "", err
		}
		if capture {
			raw = append(raw, b)
		}
		if b == '"' {
			break
		}
		if b < 0x20 {
			_, err := d.invalid()
			return "", err
		}
		if b != '\\' {
			continue
		}
		escaped, err := d.reader.ReadByte()
		if err != nil {
			return "", err
		}
		if capture {
			raw = append(raw, escaped)
		}
		switch escaped {
		case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
		case 'u':
			for range 4 {
				digit, err := d.reader.ReadByte()
				if err != nil {
					return "", err
				}
				if !(digit >= '0' && digit <= '9' || digit >= 'a' && digit <= 'f' || digit >= 'A' && digit <= 'F') {
					_, err := d.invalid()
					return "", err
				}
				if capture {
					raw = append(raw, digit)
				}
			}
		default:
			_, err := d.invalid()
			return "", err
		}
	}
	if !capture {
		return "", nil
	}
	var value string
	if err := Unmarshal(raw, &value); err != nil {
		return "", errors.New("invalid JSON string")
	}
	return value, nil
}
func (d *JSONTokenDecoder) numberToken(capture bool) (json.Token, error) {
	// Grammar states: sign, integer zero/digits, dot/fraction, exponent/sign/digits.
	state := 0
	var raw []byte
	for {
		next, err := d.reader.Peek(1)
		if err != nil && err != io.EOF {
			return nil, err
		}
		if err == io.EOF {
			break
		}
		b := next[0]
		if b == ',' || b == ']' || b == '}' || b == ' ' || b == '\t' || b == '\r' || b == '\n' {
			break
		}
		digit := b >= '0' && b <= '9'
		switch state {
		case 0:
			if b == '-' {
				state = 1
			} else if b == '0' {
				state = 2
			} else if digit {
				state = 3
			} else {
				return d.invalid()
			}
		case 1:
			if b == '0' {
				state = 2
			} else if digit {
				state = 3
			} else {
				return d.invalid()
			}
		case 2, 3:
			if b == '.' {
				state = 4
			} else if b == 'e' || b == 'E' {
				state = 6
			} else if state == 3 && digit {
			} else {
				return d.invalid()
			}
		case 4:
			if digit {
				state = 5
			} else {
				return d.invalid()
			}
		case 5:
			if b == 'e' || b == 'E' {
				state = 6
			} else if !digit {
				return d.invalid()
			}
		case 6:
			if b == '+' || b == '-' {
				state = 7
			} else if digit {
				state = 8
			} else {
				return d.invalid()
			}
		case 7:
			if digit {
				state = 8
			} else {
				return d.invalid()
			}
		case 8:
			if !digit {
				return d.invalid()
			}
		}
		_, _ = d.reader.ReadByte()
		if capture {
			raw = append(raw, b)
		}
	}
	if state != 2 && state != 3 && state != 5 && state != 8 {
		return d.invalid()
	}
	return json.Number(raw), nil
}
func (d *JSONTokenDecoder) Skip() error {
	token, err := d.token(false)
	if err != nil {
		return err
	}
	opening, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	if opening != '{' && opening != '[' {
		_, err := d.invalid()
		return err
	}
	depth := 1
	for depth > 0 {
		token, err = d.token(false)
		if err != nil {
			return err
		}
		if delimiter, ok := token.(json.Delim); ok {
			if delimiter == '{' || delimiter == '[' {
				depth++
			} else {
				depth--
			}
		}
	}
	return nil
}
