// Package eitaameow implements Eitaa's native TL-over-HTTPS client. It has no
// dependency on gateway HTTP, SQL, or webhook implementations.
package eitaameow

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

// The pinned MIT schema is a protocol catalog, not a capability claim. See
// schema/LICENSE and docs/providers/sources.md for provenance.
//
//go:embed schema/layer135.json
var schemaJSON []byte

const (
	maxWireBytes   = 16 << 20
	maxDepth       = 48
	maxVectorItems = 10000
	vectorID       = uint32(0x1cb5c415)
	boolTrue       = uint32(0x997275b5)
	boolFalse      = uint32(0xbc799737)
)

type object map[string]any
type parameter struct {
	Name string `json:"name"`
	Type string `json:"type"`
}
type definition struct {
	ID        int64       `json:"id"`
	Predicate string      `json:"predicate"`
	Method    string      `json:"method"`
	Params    []parameter `json:"params"`
	Type      string      `json:"type"`
}
type codec struct {
	methods map[string]definition
	names   map[string]definition
	ids     map[uint32]definition
}

var bundledCodec = sync.OnceValues(func() (*codec, error) {
	var sections map[string]json.RawMessage
	if err := json.Unmarshal(schemaJSON, &sections); err != nil {
		return nil, err
	}
	c := &codec{map[string]definition{}, map[string]definition{}, map[uint32]definition{}}
	for _, section := range []string{"MTProto", "API"} {
		var v struct {
			Constructors []definition `json:"constructors"`
			Methods      []definition `json:"methods"`
		}
		if err := json.Unmarshal(sections[section], &v); err != nil {
			return nil, err
		}
		for _, d := range v.Constructors {
			c.names[d.Predicate] = d
			c.ids[uint32(d.ID)] = d
		}
		for _, d := range v.Methods {
			c.methods[d.Method] = d
		}
	}
	// The inspected official worker also declares this error variant, absent
	// from the pinned MIT catalog. This is a wire layout, not copied client code.
	d := definition{ID: -404, Predicate: "eitta_error", Params: []parameter{{Name: "code", Type: "int"}, {Name: "text", Type: "string"}}, Type: "Error"}
	c.names[d.Predicate] = d
	c.ids[uint32(d.ID)] = d
	// A controlled native auth.signIn response (2026-10-10) carries a
	// terminal TL byte string when authorization flag 10 is set. Its semantics
	// are unreviewed: consume the exact optional layout but never use it as a
	// credential, expose it, or relax the whole-message trailing-byte check.
	auth := c.names["auth.authorization"]
	auth.Params = append(auth.Params, parameter{Name: "private_extension", Type: "flags.10?bytes"})
	c.names[auth.Predicate] = auth
	c.ids[uint32(auth.ID)] = auth
	// The current official Android APK (SHA recorded in sources.md) sends
	// inputGeoPoint#f3b7acc9 with two doubles and no flags. The Web catalog's
	// #48222faf was not consumed by the live server: its flags became an empty
	// caption and latitude bits became random_id. Keep one reviewed wire form;
	// the bundled MIT reference stays byte-identical for provenance.
	geo := definition{ID: -206066487, Predicate: "inputGeoPoint", Params: []parameter{{Name: "lat", Type: "double"}, {Name: "long", Type: "double"}}, Type: "InputGeoPoint"}
	delete(c.ids, uint32(c.names[geo.Predicate].ID))
	c.names[geo.Predicate], c.ids[uint32(geo.ID)] = geo, geo
	return c, nil
})

func optional(t string) (string, uint, string, bool) {
	i := strings.IndexByte(t, '?')
	if i < 0 {
		return "", 0, t, false
	}
	p := strings.LastIndexByte(t[:i], '.')
	if p < 1 {
		return "", 0, t, false
	}
	n, e := strconv.ParseUint(t[p+1:i], 10, 5)
	if e != nil {
		return "", 0, t, false
	}
	return t[:p], uint(n), t[i+1:], true
}
func vectorType(t string) (string, bool) {
	if (strings.HasPrefix(t, "Vector<") || strings.HasPrefix(t, "vector<")) && strings.HasSuffix(t, ">") {
		return t[7 : len(t)-1], true
	}
	return "", false
}
func integer(v any) (int64, error) {
	switch n := v.(type) {
	case int:
		return int64(n), nil
	case int32:
		return int64(n), nil
	case int64:
		return n, nil
	case uint32:
		return int64(n), nil
	case json.Number:
		return n.Int64()
	case string:
		return strconv.ParseInt(n, 10, 64)
	}
	return 0, errors.New("TL integer required")
}
func write32(b *bytes.Buffer, n uint32) {
	var v [4]byte
	binary.LittleEndian.PutUint32(v[:], n)
	b.Write(v[:])
}
func write64(b *bytes.Buffer, n uint64) {
	var v [8]byte
	binary.LittleEndian.PutUint64(v[:], n)
	b.Write(v[:])
}
func writeBytes(b *bytes.Buffer, v []byte) error {
	if len(v) > 0xffffff || len(v) > maxWireBytes {
		return errors.New("TL bytes limit")
	}
	n := len(v)
	header := 1
	if n < 254 {
		b.WriteByte(byte(n))
	} else {
		b.Write([]byte{254, byte(n), byte(n >> 8), byte(n >> 16)})
		header = 4
	}
	b.Write(v)
	for (header+n)%4 != 0 {
		b.WriteByte(0)
		n++
	}
	return nil
}
func (c *codec) encodeMethod(name string, v object) ([]byte, error) {
	d, ok := c.methods[name]
	if !ok {
		return nil, errors.New("unrecognized TL method")
	}
	var b bytes.Buffer
	err := c.encodeDefinition(&b, d, v, 0)
	return b.Bytes(), err
}
func (c *codec) encodeDefinition(b *bytes.Buffer, d definition, v object, depth int) error {
	if depth > maxDepth {
		return errors.New("TL nesting limit")
	}
	flags := map[string]uint32{}
	for _, p := range d.Params {
		if p.Type == "#" {
			if n, ok := v[p.Name]; ok {
				i, e := integer(n)
				if e != nil || i < 0 || i > math.MaxUint32 {
					return errors.New("invalid TL flags")
				}
				flags[p.Name] = uint32(i)
			}
		}
	}
	for _, p := range d.Params {
		f, bit, t, opt := optional(p.Type)
		if !opt {
			continue
		}
		x, present := v[p.Name]
		present = present && x != nil
		if t == "true" {
			present = x == true
		}
		if present {
			flags[f] |= 1 << bit
		} else {
			flags[f] &= ^(1 << bit)
		}
	}
	write32(b, uint32(d.ID))
	for _, p := range d.Params {
		t := p.Type
		if t == "#" {
			write32(b, flags[p.Name])
			continue
		}
		if f, bit, base, opt := optional(t); opt {
			if flags[f]&(1<<bit) == 0 {
				continue
			}
			t = base
			if t == "true" {
				continue
			}
		}
		x, ok := v[p.Name]
		if !ok {
			return fmt.Errorf("missing TL field %s", p.Name)
		}
		if err := c.encodeType(b, t, x, depth+1); err != nil {
			return fmt.Errorf("TL field %s: %w", p.Name, err)
		}
		if b.Len() > maxWireBytes {
			return errors.New("TL message limit")
		}
	}
	return nil
}
func (c *codec) encodeType(b *bytes.Buffer, t string, v any, depth int) error {
	if depth > maxDepth {
		return errors.New("TL nesting limit")
	}
	t = strings.TrimPrefix(t, "!")
	if item, ok := vectorType(t); ok {
		rv := reflect.ValueOf(v)
		if !rv.IsValid() || (rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array) || rv.Len() > maxVectorItems {
			return errors.New("invalid TL vector")
		}
		write32(b, vectorID)
		write32(b, uint32(rv.Len()))
		for i := 0; i < rv.Len(); i++ {
			if e := c.encodeType(b, item, rv.Index(i).Interface(), depth+1); e != nil {
				return e
			}
		}
		return nil
	}
	switch t {
	case "int", "#", "long":
		n, e := integer(v)
		if e != nil {
			return e
		}
		if t == "long" {
			write64(b, uint64(n))
		} else {
			if n < math.MinInt32 || n > math.MaxInt32 {
				return errors.New("TL int overflow")
			}
			write32(b, uint32(n))
		}
		return nil
	case "double":
		n, ok := v.(float64)
		if !ok || math.IsNaN(n) || math.IsInf(n, 0) {
			return errors.New("invalid TL double")
		}
		write64(b, math.Float64bits(n))
		return nil
	case "string":
		s, ok := v.(string)
		if !ok || !utf8.ValidString(s) {
			return errors.New("invalid TL string")
		}
		return writeBytes(b, []byte(s))
	case "bytes", "int128", "int256":
		raw, ok := v.([]byte)
		if !ok {
			return errors.New("TL byte slice required")
		}
		if t != "bytes" {
			size := 16
			if t == "int256" {
				size = 32
			}
			if len(raw) != size {
				return errors.New("invalid fixed TL bytes")
			}
			b.Write(raw)
			return nil
		}
		return writeBytes(b, raw)
	case "Bool":
		value, ok := v.(bool)
		if !ok {
			return errors.New("TL bool required")
		}
		if value {
			write32(b, boolTrue)
		} else {
			write32(b, boolFalse)
		}
		return nil
	case "true":
		return nil
	}
	obj, ok := v.(object)
	if !ok {
		if m, yes := v.(map[string]any); yes {
			obj = object(m)
		} else {
			return errors.New("TL object required")
		}
	}
	name, _ := obj["_"].(string)
	d, ok := c.names[name]
	if !ok {
		return errors.New("unknown TL constructor")
	}
	if t != "Object" && t != "X" && d.Type != t {
		return errors.New("wrong TL constructor type")
	}
	return c.encodeDefinition(b, d, obj, depth+1)
}

type wireReader struct {
	data   []byte
	offset int
	budget *int
}

func (r *wireReader) take(n int) ([]byte, error) {
	if n < 0 || n > len(r.data)-r.offset {
		return nil, io.ErrUnexpectedEOF
	}
	v := r.data[r.offset : r.offset+n]
	r.offset += n
	return v, nil
}
func (r *wireReader) u32() (uint32, error) {
	v, e := r.take(4)
	if e != nil {
		return 0, e
	}
	return binary.LittleEndian.Uint32(v), nil
}
func (r *wireReader) tlBytes() ([]byte, error) {
	h, e := r.take(1)
	if e != nil {
		return nil, e
	}
	n := int(h[0])
	header := 1
	if n == 255 {
		return nil, errors.New("invalid TL byte prefix")
	}
	if n == 254 {
		v, e := r.take(3)
		if e != nil {
			return nil, e
		}
		n = int(v[0]) | int(v[1])<<8 | int(v[2])<<16
		header = 4
		if n < 254 {
			return nil, errors.New("noncanonical TL byte length")
		}
	}
	if n > maxWireBytes {
		return nil, errors.New("TL bytes limit")
	}
	v, e := r.take(n)
	if e != nil {
		return nil, e
	}
	pad, e := r.take((-(n + header)) & 3)
	if e != nil {
		return nil, e
	}
	for _, p := range pad {
		if p != 0 {
			return nil, errors.New("invalid TL padding")
		}
	}
	return v, nil
}
func (c *codec) decodeResponse(method string, raw []byte) (any, error) {
	d, ok := c.methods[method]
	if !ok {
		return nil, errors.New("unknown TL method")
	}
	t := d.Type
	if method == "account.getPassword" {
		// Eitaa's observed password2 constructor has a distinct declared TL
		// result type. Keep this exact union scoped to the password query.
		t = "EitaaPassword"
	}
	if len(raw) >= 4 {
		for _, name := range []string{"error", "eitta_error", "eitaa_updates_expire_token", "eitaa_token_updating"} {
			if e, ok := c.names[name]; ok && binary.LittleEndian.Uint32(raw) == uint32(e.ID) {
				t = "Object"
			}
		}
		if method == "eitaaRefreshToken" {
			// The method's declared result name differs from the observed token
			// constructor. Only the renewal path accepts that explicit variant.
			if e, ok := c.names["eitaa_updates_token"]; ok && binary.LittleEndian.Uint32(raw) == uint32(e.ID) {
				t = "Object"
			}
		}
	}
	return c.decode(raw, t)
}
func (c *codec) decode(raw []byte, t string) (any, error) {
	if len(raw) > maxWireBytes {
		return nil, errors.New("TL message limit")
	}
	budget := maxVectorItems * 4
	r := &wireReader{data: raw, budget: &budget}
	v, e := c.decodeType(r, t, 0)
	if e == nil && r.offset != len(raw) {
		return nil, errors.New("trailing TL bytes")
	}
	return v, e
}
func (c *codec) decodeType(r *wireReader, t string, depth int) (any, error) {
	if depth > maxDepth || *r.budget <= 0 {
		return nil, errors.New("TL complexity limit")
	}
	*r.budget--
	t = strings.TrimPrefix(t, "!")
	if item, ok := vectorType(t); ok {
		id, e := r.u32()
		if e != nil {
			return nil, e
		}
		if id != vectorID {
			return nil, errors.New("invalid TL vector")
		}
		n, e := r.u32()
		if e != nil {
			return nil, e
		}
		if n > maxVectorItems || int(n) > *r.budget {
			return nil, errors.New("TL vector limit")
		}
		items := make([]any, 0, int(n))
		for range n {
			v, e := c.decodeType(r, item, depth+1)
			if e != nil {
				return nil, e
			}
			items = append(items, v)
		}
		return items, nil
	}
	switch t {
	case "int", "#":
		n, e := r.u32()
		if t == "#" {
			return int64(n), e
		}
		return int64(int32(n)), e
	case "long", "double":
		v, e := r.take(8)
		if e != nil {
			return nil, e
		}
		n := binary.LittleEndian.Uint64(v)
		if t == "long" {
			return int64(n), nil
		}
		f := math.Float64frombits(n)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, errors.New("invalid TL double")
		}
		return f, nil
	case "bytes", "string":
		v, e := r.tlBytes()
		if e != nil {
			return nil, e
		}
		if t == "bytes" {
			return append([]byte(nil), v...), nil
		}
		if !utf8.Valid(v) {
			return nil, errors.New("invalid TL UTF-8")
		}
		return string(v), nil
	case "int128", "int256":
		n := 16
		if t == "int256" {
			n = 32
		}
		v, e := r.take(n)
		return append([]byte(nil), v...), e
	case "Bool":
		v, e := r.u32()
		if e != nil {
			return nil, e
		}
		if v == boolTrue {
			return true, nil
		}
		if v == boolFalse {
			return false, nil
		}
		return nil, errors.New("invalid TL bool")
	case "true":
		return true, nil
	}
	id, e := r.u32()
	if e != nil {
		return nil, e
	}
	d, ok := c.ids[id]
	if !ok {
		return nil, errors.New("unknown TL constructor")
	}
	if d.Predicate == "gzip_packed" {
		raw, e := r.tlBytes()
		if e != nil {
			return nil, e
		}
		gz, e := gzip.NewReader(bytes.NewReader(raw))
		if e != nil {
			return nil, errors.New("invalid TL gzip")
		}
		defer gz.Close()
		plain, e := io.ReadAll(io.LimitReader(gz, maxWireBytes+1))
		if e != nil || len(plain) > maxWireBytes {
			return nil, errors.New("TL gzip limit")
		}
		child := &wireReader{data: plain, budget: r.budget}
		v, e := c.decodeType(child, t, depth+1)
		if e == nil && child.offset != len(plain) {
			return nil, errors.New("trailing packed TL bytes")
		}
		return v, e
	}
	passwordVariant := t == "EitaaPassword" && (d.Predicate == "account.password" || d.Predicate == "account.password2")
	if t != "Object" && t != "X" && d.Type != t && !passwordVariant {
		return nil, errors.New("unexpected TL result type")
	}
	obj := object{"_": d.Predicate}
	flags := map[string]uint32{}
	for _, p := range d.Params {
		typ := p.Type
		if typ == "#" {
			n, e := r.u32()
			if e != nil {
				return nil, e
			}
			flags[p.Name] = n
			obj[p.Name] = int64(n)
			continue
		}
		if f, bit, base, opt := optional(typ); opt {
			if flags[f]&(1<<bit) == 0 {
				continue
			}
			typ = base
		}
		v, e := c.decodeType(r, typ, depth+1)
		if e != nil {
			return nil, e
		}
		obj[p.Name] = v
	}
	return obj, nil
}
