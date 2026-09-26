package msg

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
)

// The generated structs carry the template verbatim in `ll` tags, so
// this file is the only place that knows what a template type means on
// the wire.  A message struct's fields are its blocks:
//
//	type ChatFromViewer struct {
//	        AgentData      ChatFromViewer_AgentData      `ll:"Single"`
//	        ChatData       ChatFromViewer_ChatData       `ll:"Single"`
//	}
//
// and a block struct's fields are its fields:
//
//	type ChatFromViewer_ChatData struct {
//	        Message []byte `ll:"Variable,2"`
//	        Type    uint8  `ll:"U8"`
//	        Channel int32  `ll:"S32"`
//	}
//
// Block quantifiers map to Go shapes: Single is a struct, Multiple N is
// an [N]struct and Variable is a []struct with a one byte count.

type quant uint8

const (
	qSingle quant = iota
	qMultiple
	qVariable
)

type kind uint8

const (
	kU8 kind = iota
	kU16
	kU32
	kU64
	kS8
	kS16
	kS32
	kS64
	kF32
	kF64
	kBool
	kUUID
	kVec3
	kVec3d
	kVec4
	kQuat
	kIPAddr
	kIPPort
	kVariable
	kFixed
)

var kindByName = map[string]kind{
	"U8": kU8, "U16": kU16, "U32": kU32, "U64": kU64,
	"S8": kS8, "S16": kS16, "S32": kS32, "S64": kS64,
	"F32": kF32, "F64": kF64,
	"BOOL":         kBool,
	"LLUUID":       kUUID,
	"LLVector3":    kVec3,
	"LLVector3d":   kVec3d,
	"LLVector4":    kVec4,
	"LLQuaternion": kQuat,
	"IPADDR":       kIPAddr,
	"IPPORT":       kIPPort,
	"Variable":     kVariable,
	"Fixed":        kFixed,
}

type fieldPlan struct {
	name  string
	index int
	kind  kind
	size  int // Variable: width of the length prefix.  Fixed: byte count.
}

type blockPlan struct {
	name   string
	index  int
	quant  quant
	count  int
	fields []fieldPlan
}

type plan struct {
	name   string
	blocks []blockPlan
}

// Tags are parsed once per type, not once per packet.
var planCache sync.Map // reflect.Type -> *plan

func planFor(t reflect.Type, name string) (*plan, error) {
	if p, ok := planCache.Load(t); ok {
		return p.(*plan), nil
	}
	p, err := compile(t, name)
	if err != nil {
		return nil, err
	}
	planCache.Store(t, p)
	return p, nil
}

func compile(t reflect.Type, name string) (*plan, error) {
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("msg: %s: message must be a struct, got %s", name, t.Kind())
	}
	p := &plan{name: name}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag, ok := f.Tag.Lookup("ll")
		if !ok {
			continue
		}
		bp := blockPlan{name: f.Name, index: i}

		head, rest, _ := strings.Cut(tag, ",")
		switch head {
		case "Single":
			bp.quant = qSingle
		case "Multiple":
			bp.quant = qMultiple
			n, err := strconv.Atoi(rest)
			if err != nil {
				return nil, fmt.Errorf("msg: %s.%s: bad Multiple count %q", name, f.Name, rest)
			}
			bp.count = n
		case "Variable":
			bp.quant = qVariable
		default:
			return nil, fmt.Errorf("msg: %s.%s: unknown block quantifier %q", name, f.Name, head)
		}

		bt := f.Type
		switch bp.quant {
		case qMultiple:
			if bt.Kind() != reflect.Array {
				return nil, fmt.Errorf("msg: %s.%s: Multiple block must be an array", name, f.Name)
			}
			if bt.Len() != bp.count {
				return nil, fmt.Errorf("msg: %s.%s: array is [%d] but template says %d",
					name, f.Name, bt.Len(), bp.count)
			}
			bt = bt.Elem()
		case qVariable:
			if bt.Kind() != reflect.Slice {
				return nil, fmt.Errorf("msg: %s.%s: Variable block must be a slice", name, f.Name)
			}
			bt = bt.Elem()
		}
		if bt.Kind() != reflect.Struct {
			return nil, fmt.Errorf("msg: %s.%s: block must be a struct", name, f.Name)
		}

		fields, err := compileBlock(bt, name, f.Name)
		if err != nil {
			return nil, err
		}
		bp.fields = fields
		p.blocks = append(p.blocks, bp)
	}
	return p, nil
}

func compileBlock(t reflect.Type, msgName, blockName string) ([]fieldPlan, error) {
	var out []fieldPlan
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag, ok := f.Tag.Lookup("ll")
		if !ok {
			continue
		}
		head, rest, _ := strings.Cut(tag, ",")
		k, ok := kindByName[head]
		if !ok {
			return nil, fmt.Errorf("msg: %s.%s.%s: unknown field type %q",
				msgName, blockName, f.Name, head)
		}
		fp := fieldPlan{name: f.Name, index: i, kind: k}
		if k == kVariable || k == kFixed {
			n, err := strconv.Atoi(rest)
			if err != nil {
				return nil, fmt.Errorf("msg: %s.%s.%s: %s needs a size, got %q",
					msgName, blockName, f.Name, head, rest)
			}
			fp.size = n
			if k == kVariable && n != 1 && n != 2 && n != 4 {
				return nil, fmt.Errorf("msg: %s.%s.%s: Variable prefix must be 1, 2 or 4, got %d",
					msgName, blockName, f.Name, n)
			}
		}
		out = append(out, fp)
	}
	return out, nil
}

// Marshal encodes a message body.  The result does not include the
// packet header or the message number; see AppendHeader and AppendID.
func Marshal(m Message) ([]byte, error) {
	return MarshalAppend(nil, m)
}

// MarshalAppend encodes onto dst, which lets a caller build a whole
// packet in one buffer.
func MarshalAppend(dst []byte, m Message) ([]byte, error) {
	v := reflect.ValueOf(m)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return nil, fmt.Errorf("msg: Marshal needs a non-nil pointer, got %T", m)
	}
	v = v.Elem()
	p, err := planFor(v.Type(), m.MsgInfo().Name)
	if err != nil {
		return nil, err
	}

	w := &buf{b: dst}
	for bi := range p.blocks {
		b := &p.blocks[bi]
		bv := v.Field(b.index)
		switch b.quant {
		case qSingle:
			if err := encodeBlock(w, bv, p, b); err != nil {
				return nil, err
			}
		case qMultiple:
			for j := 0; j < b.count; j++ {
				if err := encodeBlock(w, bv.Index(j), p, b); err != nil {
					return nil, err
				}
			}
		case qVariable:
			n := bv.Len()
			if n > 255 {
				return nil, fmt.Errorf("msg: %s.%s: %d instances, a Variable block holds at most 255",
					p.name, b.name, n)
			}
			w.u8(uint8(n))
			for j := 0; j < n; j++ {
				if err := encodeBlock(w, bv.Index(j), p, b); err != nil {
					return nil, err
				}
			}
		}
	}
	return w.b, nil
}

func encodeBlock(w *buf, bv reflect.Value, p *plan, b *blockPlan) error {
	for fi := range b.fields {
		f := &b.fields[fi]
		if err := encodeField(w, bv.Field(f.index), f); err != nil {
			return &fieldError{msg: p.name, block: b.name, field: f.name, err: err}
		}
	}
	return nil
}

func encodeField(w *buf, v reflect.Value, f *fieldPlan) error {
	switch f.kind {
	case kU8:
		w.u8(uint8(v.Uint()))
	case kU16:
		w.u16(uint16(v.Uint()))
	case kU32:
		w.u32(uint32(v.Uint()))
	case kU64:
		w.u64(v.Uint())
	case kS8:
		w.u8(uint8(int8(v.Int())))
	case kS16:
		w.u16(uint16(int16(v.Int())))
	case kS32:
		w.u32(uint32(int32(v.Int())))
	case kS64:
		w.u64(uint64(v.Int()))
	case kF32:
		w.f32(float32(v.Float()))
	case kF64:
		w.f64(v.Float())
	case kBool:
		if v.Bool() {
			w.u8(1)
		} else {
			w.u8(0)
		}
	case kUUID, kIPAddr:
		w.bytes(arrayBytes(v))
	case kIPPort:
		// Network order, unlike every other integer here.
		p := uint16(v.Uint())
		w.u8(uint8(p >> 8))
		w.u8(uint8(p))
	case kVec3, kQuat:
		for i := 0; i < 3; i++ {
			w.f32(float32(v.Field(i).Float()))
		}
	case kVec4:
		for i := 0; i < 4; i++ {
			w.f32(float32(v.Field(i).Float()))
		}
	case kVec3d:
		for i := 0; i < 3; i++ {
			w.f64(v.Field(i).Float())
		}
	case kFixed:
		b := arrayBytes(v)
		if len(b) != f.size {
			return fmt.Errorf("Fixed field is [%d]byte but template says %d", len(b), f.size)
		}
		w.bytes(b)
	case kVariable:
		b := v.Bytes()
		max := 1<<(8*f.size) - 1
		if len(b) > max {
			return fmt.Errorf("%d bytes exceeds the %d byte length prefix", len(b), f.size)
		}
		switch f.size {
		case 1:
			w.u8(uint8(len(b)))
		case 2:
			w.u16(uint16(len(b)))
		case 4:
			w.u32(uint32(len(b)))
		}
		w.bytes(b)
	default:
		return fmt.Errorf("unhandled kind %d", f.kind)
	}
	return nil
}

// Unmarshal decodes a message body into m.
//
// Trailing bytes are ignored, and a Variable block whose count byte is
// missing decodes as zero instances rather than an error.  Both are
// deliberate: Linden Lab extends messages by appending blocks, and a
// client that treats "there is more here than I know about" or "the
// block I was told about is absent" as fatal stops working the next
// time the protocol grows.
//
// A zerocoded message that runs off its end is read as though the rest
// were zeros, because on the grid that is what the rest is.  The
// simulator's zero coder leaves off the tail of a trailing run of
// zeros now and then, and the viewer has always read past the end of a
// packet as zeros (LLTemplateMessageReader::decodeData, "default to
// 0s").  Measured on Agni: every failure over two and a half minutes
// on three avatars was a zerocoded ObjectUpdate short by exactly 5 or
// exactly 37 bytes, all of them decoding once given that many zeros.
// Refusing them threw away whole packets of object descriptions -- one
// was a seat, which the rest of the store then could not place.
//
// Zeros stand in only for a width the template fixes.  A Variable
// field whose length prefix runs off the end is empty, as in the
// viewer, and one whose payload runs past the end is cut at the last
// byte there is, never padded: a length of 65,535 with three bytes
// behind it decodes as those three.  The viewer does not check the
// payload at all.  Why: doc/wire.md#past-the-end
//
// Any other short read is an error.  A message that is not zerocoded
// has no zeros to have lost, so running off its end is a real fault,
// and one worth seeing: it is what a wrong template looks like.
func Unmarshal(b []byte, m Message) error {
	_, err := unmarshal(b, m)
	return err
}

// unmarshal is Unmarshal, also saying whether it read past the end,
// padding a width or cutting a payload, so that the receiver can count
// how often.
func unmarshal(b []byte, m Message) (padded bool, err error) {
	v := reflect.ValueOf(m)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return false, fmt.Errorf("msg: Unmarshal needs a non-nil pointer, got %T", m)
	}
	v = v.Elem()
	info := m.MsgInfo()
	p, err := planFor(v.Type(), info.Name)
	if err != nil {
		return false, err
	}

	r := &cur{b: b, lenient: info.Zerocoded}
	err = decodeBlocks(r, v, p)
	return r.padded, err
}

func decodeBlocks(r *cur, v reflect.Value, p *plan) error {
	for bi := range p.blocks {
		blk := &p.blocks[bi]
		bv := v.Field(blk.index)
		switch blk.quant {
		case qSingle:
			if err := decodeBlock(r, bv, p, blk); err != nil {
				return err
			}
		case qMultiple:
			for j := 0; j < blk.count; j++ {
				if err := decodeBlock(r, bv.Index(j), p, blk); err != nil {
					return err
				}
			}
		case qVariable:
			if r.atEnd() {
				bv.Set(reflect.MakeSlice(bv.Type(), 0, 0))
				continue
			}
			n, err := r.u8()
			if err != nil {
				return &fieldError{msg: p.name, block: blk.name, field: "(count)", err: err}
			}
			s := reflect.MakeSlice(bv.Type(), int(n), int(n))
			for j := 0; j < int(n); j++ {
				if err := decodeBlock(r, s.Index(j), p, blk); err != nil {
					return err
				}
			}
			bv.Set(s)
		}
	}
	return nil
}

func decodeBlock(r *cur, bv reflect.Value, p *plan, b *blockPlan) error {
	for fi := range b.fields {
		f := &b.fields[fi]
		if err := decodeField(r, bv.Field(f.index), f); err != nil {
			return &fieldError{msg: p.name, block: b.name, field: f.name, err: err}
		}
	}
	return nil
}

func decodeField(r *cur, v reflect.Value, f *fieldPlan) error {
	switch f.kind {
	case kU8:
		x, err := r.u8()
		if err != nil {
			return err
		}
		v.SetUint(uint64(x))
	case kU16:
		x, err := r.u16()
		if err != nil {
			return err
		}
		v.SetUint(uint64(x))
	case kU32:
		x, err := r.u32()
		if err != nil {
			return err
		}
		v.SetUint(uint64(x))
	case kU64:
		x, err := r.u64()
		if err != nil {
			return err
		}
		v.SetUint(x)
	case kS8:
		x, err := r.u8()
		if err != nil {
			return err
		}
		v.SetInt(int64(int8(x)))
	case kS16:
		x, err := r.u16()
		if err != nil {
			return err
		}
		v.SetInt(int64(int16(x)))
	case kS32:
		x, err := r.u32()
		if err != nil {
			return err
		}
		v.SetInt(int64(int32(x)))
	case kS64:
		x, err := r.u64()
		if err != nil {
			return err
		}
		v.SetInt(int64(x))
	case kF32:
		x, err := r.f32()
		if err != nil {
			return err
		}
		v.SetFloat(float64(x))
	case kF64:
		x, err := r.f64()
		if err != nil {
			return err
		}
		v.SetFloat(x)
	case kBool:
		x, err := r.u8()
		if err != nil {
			return err
		}
		v.SetBool(x != 0)
	case kUUID, kIPAddr:
		dst := arrayBytes(v)
		s, err := r.take(len(dst))
		if err != nil {
			return err
		}
		copy(dst, s)
	case kIPPort:
		s, err := r.take(2)
		if err != nil {
			return err
		}
		v.SetUint(uint64(s[0])<<8 | uint64(s[1]))
	case kVec3, kQuat:
		for i := 0; i < 3; i++ {
			x, err := r.f32()
			if err != nil {
				return err
			}
			v.Field(i).SetFloat(float64(x))
		}
	case kVec4:
		for i := 0; i < 4; i++ {
			x, err := r.f32()
			if err != nil {
				return err
			}
			v.Field(i).SetFloat(float64(x))
		}
	case kVec3d:
		for i := 0; i < 3; i++ {
			x, err := r.f64()
			if err != nil {
				return err
			}
			v.Field(i).SetFloat(x)
		}
	case kFixed:
		dst := arrayBytes(v)
		if len(dst) != f.size {
			return fmt.Errorf("Fixed field is [%d]byte but template says %d", len(dst), f.size)
		}
		s, err := r.take(f.size)
		if err != nil {
			return err
		}
		copy(dst, s)
	case kVariable:
		var n int
		switch f.size {
		case 1:
			x, err := r.u8()
			if err != nil {
				return err
			}
			n = int(x)
		case 2:
			x, err := r.u16()
			if err != nil {
				return err
			}
			n = int(x)
		case 4:
			x, err := r.u32()
			if err != nil {
				return err
			}
			n = int(x)
		}
		// A prefix that ran off the end leaves nothing to cut, so
		// the field is empty: the viewer's length 0.
		s, err := r.cut(n)
		if err != nil {
			return err
		}
		// Copy: the caller should not end up aliasing the
		// receive buffer.
		out := make([]byte, len(s))
		copy(out, s)
		v.SetBytes(out)
	default:
		return fmt.Errorf("unhandled kind %d", f.kind)
	}
	return nil
}

// arrayBytes views an addressable byte array as a slice.
func arrayBytes(v reflect.Value) []byte {
	return v.Slice(0, v.Len()).Bytes()
}
