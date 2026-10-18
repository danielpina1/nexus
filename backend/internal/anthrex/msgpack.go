package anthrex

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
)

// This file is a small, dependency-free MessagePack codec over generic Go
// values. It covers exactly what rmp_serde (`to_vec_named`) produces and
// accepts: nil, bool, integers, floats, str, bin, array and map (string keys).
//
// Decoded values are: nil, bool, int64 (or uint64 above MaxInt64), float64,
// string, []byte, []any and map[string]any. Ext values decode to Ext.

// Ext is a MessagePack extension value (not used by Anthrex; kept so a
// decode never fails on one).
type Ext struct {
	Type int8
	Data []byte
}

// maxDepth bounds nesting on decode (a hostile frame cannot blow the stack).
const maxDepth = 256

var errShort = errors.New("msgpack: unexpected end of data")

// Marshal encodes a generic value. Supported: nil, bool, all int/uint kinds,
// float32/64, string, []byte, json.Number, json.RawMessage (decoded as JSON
// first), Ext, slices/arrays and maps with string keys (keys are written in
// sorted order, so output is deterministic). Structs are not supported:
// build maps (see wire.go).
func Marshal(v any) ([]byte, error) {
	var e encoder
	if err := e.encode(v, 0); err != nil {
		return nil, err
	}
	return e.buf, nil
}

type encoder struct{ buf []byte }

func (e *encoder) byte1(b byte) { e.buf = append(e.buf, b) }

func (e *encoder) uint(u uint64) {
	switch {
	case u <= 0x7f:
		e.byte1(byte(u))
	case u <= math.MaxUint8:
		e.buf = append(e.buf, 0xcc, byte(u))
	case u <= math.MaxUint16:
		e.buf = append(e.buf, 0xcd)
		e.buf = binary.BigEndian.AppendUint16(e.buf, uint16(u))
	case u <= math.MaxUint32:
		e.buf = append(e.buf, 0xce)
		e.buf = binary.BigEndian.AppendUint32(e.buf, uint32(u))
	default:
		e.buf = append(e.buf, 0xcf)
		e.buf = binary.BigEndian.AppendUint64(e.buf, u)
	}
}

func (e *encoder) int(i int64) {
	if i >= 0 {
		e.uint(uint64(i))
		return
	}
	switch {
	case i >= -32:
		e.byte1(byte(int8(i)))
	case i >= math.MinInt8:
		e.buf = append(e.buf, 0xd0, byte(int8(i)))
	case i >= math.MinInt16:
		e.buf = append(e.buf, 0xd1)
		e.buf = binary.BigEndian.AppendUint16(e.buf, uint16(int16(i)))
	case i >= math.MinInt32:
		e.buf = append(e.buf, 0xd2)
		e.buf = binary.BigEndian.AppendUint32(e.buf, uint32(int32(i)))
	default:
		e.buf = append(e.buf, 0xd3)
		e.buf = binary.BigEndian.AppendUint64(e.buf, uint64(i))
	}
}

func (e *encoder) str(s string) {
	n := len(s)
	switch {
	case n < 32:
		e.byte1(0xa0 | byte(n))
	case n <= math.MaxUint8:
		e.buf = append(e.buf, 0xd9, byte(n))
	case n <= math.MaxUint16:
		e.buf = append(e.buf, 0xda)
		e.buf = binary.BigEndian.AppendUint16(e.buf, uint16(n))
	default:
		e.buf = append(e.buf, 0xdb)
		e.buf = binary.BigEndian.AppendUint32(e.buf, uint32(n))
	}
	e.buf = append(e.buf, s...)
}

func (e *encoder) bin(b []byte) {
	n := len(b)
	switch {
	case n <= math.MaxUint8:
		e.buf = append(e.buf, 0xc4, byte(n))
	case n <= math.MaxUint16:
		e.buf = append(e.buf, 0xc5)
		e.buf = binary.BigEndian.AppendUint16(e.buf, uint16(n))
	default:
		e.buf = append(e.buf, 0xc6)
		e.buf = binary.BigEndian.AppendUint32(e.buf, uint32(n))
	}
	e.buf = append(e.buf, b...)
}

func (e *encoder) arrayHeader(n int) {
	switch {
	case n < 16:
		e.byte1(0x90 | byte(n))
	case n <= math.MaxUint16:
		e.buf = append(e.buf, 0xdc)
		e.buf = binary.BigEndian.AppendUint16(e.buf, uint16(n))
	default:
		e.buf = append(e.buf, 0xdd)
		e.buf = binary.BigEndian.AppendUint32(e.buf, uint32(n))
	}
}

func (e *encoder) mapHeader(n int) {
	switch {
	case n < 16:
		e.byte1(0x80 | byte(n))
	case n <= math.MaxUint16:
		e.buf = append(e.buf, 0xde)
		e.buf = binary.BigEndian.AppendUint16(e.buf, uint16(n))
	default:
		e.buf = append(e.buf, 0xdf)
		e.buf = binary.BigEndian.AppendUint32(e.buf, uint32(n))
	}
}

func (e *encoder) encode(v any, depth int) error {
	if depth > maxDepth {
		return errors.New("msgpack: value nested too deeply")
	}
	switch t := v.(type) {
	case nil:
		e.byte1(0xc0)
	case bool:
		if t {
			e.byte1(0xc3)
		} else {
			e.byte1(0xc2)
		}
	case int:
		e.int(int64(t))
	case int8:
		e.int(int64(t))
	case int16:
		e.int(int64(t))
	case int32:
		e.int(int64(t))
	case int64:
		e.int(t)
	case uint:
		e.uint(uint64(t))
	case uint8:
		e.uint(uint64(t))
	case uint16:
		e.uint(uint64(t))
	case uint32:
		e.uint(uint64(t))
	case uint64:
		e.uint(t)
	case float32:
		e.byte1(0xca)
		e.buf = binary.BigEndian.AppendUint32(e.buf, math.Float32bits(t))
	case float64:
		e.byte1(0xcb)
		e.buf = binary.BigEndian.AppendUint64(e.buf, math.Float64bits(t))
	case json.Number:
		if i, err := strconv.ParseInt(string(t), 10, 64); err == nil {
			e.int(i)
		} else if u, err := strconv.ParseUint(string(t), 10, 64); err == nil {
			e.uint(u)
		} else if f, err := t.Float64(); err == nil {
			return e.encode(f, depth)
		} else {
			return fmt.Errorf("msgpack: bad json number %q", t)
		}
	case json.RawMessage:
		g, err := DecodeJSON(t)
		if err != nil {
			return err
		}
		return e.encode(g, depth)
	case string:
		e.str(t)
	case []byte:
		e.bin(t)
	case Ext:
		return e.ext(t)
	case []any:
		e.arrayHeader(len(t))
		for _, x := range t {
			if err := e.encode(x, depth+1); err != nil {
				return err
			}
		}
	case []string:
		e.arrayHeader(len(t))
		for _, x := range t {
			e.str(x)
		}
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		e.mapHeader(len(keys))
		for _, k := range keys {
			e.str(k)
			if err := e.encode(t[k], depth+1); err != nil {
				return err
			}
		}
	default:
		return e.reflect(reflect.ValueOf(v), depth)
	}
	return nil
}

func (e *encoder) ext(x Ext) error {
	n := len(x.Data)
	switch n {
	case 1:
		e.byte1(0xd4)
	case 2:
		e.byte1(0xd5)
	case 4:
		e.byte1(0xd6)
	case 8:
		e.byte1(0xd7)
	case 16:
		e.byte1(0xd8)
	default:
		switch {
		case n <= math.MaxUint8:
			e.buf = append(e.buf, 0xc7, byte(n))
		case n <= math.MaxUint16:
			e.buf = append(e.buf, 0xc8)
			e.buf = binary.BigEndian.AppendUint16(e.buf, uint16(n))
		default:
			e.buf = append(e.buf, 0xc9)
			e.buf = binary.BigEndian.AppendUint32(e.buf, uint32(n))
		}
	}
	e.byte1(byte(x.Type))
	e.buf = append(e.buf, x.Data...)
	return nil
}

// reflect handles typed slices, arrays, maps with string keys and pointers.
func (e *encoder) reflect(rv reflect.Value, depth int) error {
	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface:
		if rv.IsNil() {
			e.byte1(0xc0)
			return nil
		}
		return e.encode(rv.Elem().Interface(), depth+1)
	case reflect.Slice, reflect.Array:
		if rv.Kind() == reflect.Slice && rv.IsNil() {
			e.arrayHeader(0)
			return nil
		}
		e.arrayHeader(rv.Len())
		for i := 0; i < rv.Len(); i++ {
			if err := e.encode(rv.Index(i).Interface(), depth+1); err != nil {
				return err
			}
		}
		return nil
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			return fmt.Errorf("msgpack: map key type %s not supported", rv.Type().Key())
		}
		keys := rv.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
		e.mapHeader(len(keys))
		for _, k := range keys {
			e.str(k.String())
			if err := e.encode(rv.MapIndex(k).Interface(), depth+1); err != nil {
				return err
			}
		}
		return nil
	case reflect.String:
		e.str(rv.String())
		return nil
	case reflect.Bool:
		return e.encode(rv.Bool(), depth)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		e.int(rv.Int())
		return nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		e.uint(rv.Uint())
		return nil
	case reflect.Float32, reflect.Float64:
		return e.encode(rv.Float(), depth)
	}
	return fmt.Errorf("msgpack: cannot encode %s", rv.Type())
}

// Unmarshal decodes one MessagePack value; trailing bytes are an error.
func Unmarshal(b []byte) (any, error) {
	d := decoder{b: b}
	v, err := d.decode(0)
	if err != nil {
		return nil, err
	}
	if d.off != len(d.b) {
		return nil, fmt.Errorf("msgpack: %d trailing bytes", len(d.b)-d.off)
	}
	return v, nil
}

type decoder struct {
	b   []byte
	off int
}

func (d *decoder) take(n int) ([]byte, error) {
	if n < 0 || d.off+n > len(d.b) {
		return nil, errShort
	}
	s := d.b[d.off : d.off+n]
	d.off += n
	return s, nil
}

func (d *decoder) u8() (byte, error) {
	s, err := d.take(1)
	if err != nil {
		return 0, err
	}
	return s[0], nil
}

func (d *decoder) u16() (uint16, error) {
	s, err := d.take(2)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(s), nil
}

func (d *decoder) u32() (uint32, error) {
	s, err := d.take(4)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(s), nil
}

func (d *decoder) u64() (uint64, error) {
	s, err := d.take(8)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint64(s), nil
}

func (d *decoder) length(marker byte, l8, l16, l32 byte) (int, error) {
	switch marker {
	case l8:
		n, err := d.u8()
		return int(n), err
	case l16:
		n, err := d.u16()
		return int(n), err
	case l32:
		n, err := d.u32()
		return int(n), err
	}
	return 0, fmt.Errorf("msgpack: bad length marker 0x%02x", marker)
}

func uintValue(u uint64) any {
	if u <= math.MaxInt64 {
		return int64(u)
	}
	return u
}

func (d *decoder) decode(depth int) (any, error) {
	if depth > maxDepth {
		return nil, errors.New("msgpack: value nested too deeply")
	}
	m, err := d.u8()
	if err != nil {
		return nil, err
	}
	switch {
	case m <= 0x7f:
		return int64(m), nil
	case m >= 0xe0:
		return int64(int8(m)), nil
	case m&0xe0 == 0xa0:
		s, err := d.take(int(m & 0x1f))
		return string(s), err
	case m&0xf0 == 0x90:
		return d.array(int(m&0x0f), depth)
	case m&0xf0 == 0x80:
		return d.mapN(int(m&0x0f), depth)
	}
	switch m {
	case 0xc0:
		return nil, nil
	case 0xc2:
		return false, nil
	case 0xc3:
		return true, nil
	case 0xcc:
		v, err := d.u8()
		return int64(v), err
	case 0xcd:
		v, err := d.u16()
		return int64(v), err
	case 0xce:
		v, err := d.u32()
		return int64(v), err
	case 0xcf:
		v, err := d.u64()
		return uintValue(v), err
	case 0xd0:
		v, err := d.u8()
		return int64(int8(v)), err
	case 0xd1:
		v, err := d.u16()
		return int64(int16(v)), err
	case 0xd2:
		v, err := d.u32()
		return int64(int32(v)), err
	case 0xd3:
		v, err := d.u64()
		return int64(v), err
	case 0xca:
		v, err := d.u32()
		return float64(math.Float32frombits(v)), err
	case 0xcb:
		v, err := d.u64()
		return math.Float64frombits(v), err
	case 0xd9, 0xda, 0xdb:
		n, err := d.length(m, 0xd9, 0xda, 0xdb)
		if err != nil {
			return nil, err
		}
		s, err := d.take(n)
		return string(s), err
	case 0xc4, 0xc5, 0xc6:
		n, err := d.length(m, 0xc4, 0xc5, 0xc6)
		if err != nil {
			return nil, err
		}
		s, err := d.take(n)
		if err != nil {
			return nil, err
		}
		out := make([]byte, n)
		copy(out, s)
		return out, nil
	case 0xdc, 0xdd:
		n, err := d.length(m, 0, 0xdc, 0xdd)
		if err != nil {
			return nil, err
		}
		return d.array(n, depth)
	case 0xde, 0xdf:
		n, err := d.length(m, 0, 0xde, 0xdf)
		if err != nil {
			return nil, err
		}
		return d.mapN(n, depth)
	case 0xd4, 0xd5, 0xd6, 0xd7, 0xd8:
		n := map[byte]int{0xd4: 1, 0xd5: 2, 0xd6: 4, 0xd7: 8, 0xd8: 16}[m]
		return d.extN(n)
	case 0xc7, 0xc8, 0xc9:
		n, err := d.length(m, 0xc7, 0xc8, 0xc9)
		if err != nil {
			return nil, err
		}
		return d.extN(n)
	}
	return nil, fmt.Errorf("msgpack: unknown marker 0x%02x", m)
}

func (d *decoder) extN(n int) (any, error) {
	t, err := d.u8()
	if err != nil {
		return nil, err
	}
	s, err := d.take(n)
	if err != nil {
		return nil, err
	}
	return Ext{Type: int8(t), Data: append([]byte(nil), s...)}, nil
}

func (d *decoder) array(n, depth int) (any, error) {
	// Every element takes at least one byte: refuse impossible lengths
	// before allocating.
	if n > len(d.b)-d.off {
		return nil, errShort
	}
	out := make([]any, n)
	for i := range out {
		v, err := d.decode(depth + 1)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

func (d *decoder) mapN(n, depth int) (any, error) {
	if 2*n > len(d.b)-d.off {
		return nil, errShort
	}
	out := make(map[string]any, n)
	for i := 0; i < n; i++ {
		k, err := d.decode(depth + 1)
		if err != nil {
			return nil, err
		}
		v, err := d.decode(depth + 1)
		if err != nil {
			return nil, err
		}
		var key string
		switch t := k.(type) {
		case string:
			key = t
		case []byte:
			key = string(t)
		default:
			key = fmt.Sprint(t)
		}
		out[key] = v
	}
	return out, nil
}

// DecodeJSON parses JSON into the same generic shape Unmarshal returns
// (integers as int64/uint64, other numbers as float64).
func DecodeJSON(b []byte) (any, error) {
	var v any
	dec := json.NewDecoder(bytesReader(b))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return normalizeJSON(v), nil
}

func normalizeJSON(v any) any {
	switch t := v.(type) {
	case json.Number:
		if i, err := strconv.ParseInt(string(t), 10, 64); err == nil {
			return i
		}
		if u, err := strconv.ParseUint(string(t), 10, 64); err == nil {
			return u
		}
		f, _ := t.Float64()
		return f
	case []any:
		for i := range t {
			t[i] = normalizeJSON(t[i])
		}
		return t
	case map[string]any:
		for k := range t {
			t[k] = normalizeJSON(t[k])
		}
		return t
	}
	return v
}
