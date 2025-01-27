// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package slog

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"time"
	"unicode/utf8"

	"golang.org/x/exp/slog/internal/buffer"
)

// JSONHandler is a Handler that writes Records to an io.Writer as
// line-delimited JSON objects.
type JSONHandler struct {
	*commonHandler
}

// NewJSONHandler creates a JSONHandler that writes to w,
// using the default options.
func NewJSONHandler(w io.Writer) *JSONHandler {
	return (HandlerOptions{}).NewJSONHandler(w)
}

// NewJSONHandler creates a JSONHandler with the given options that writes to w.
func (opts HandlerOptions) NewJSONHandler(w io.Writer) *JSONHandler {
	return &JSONHandler{
		&commonHandler{
			app:     jsonAppender{},
			attrSep: ',',
			w:       w,
			opts:    opts,
		},
	}
}

// With returns a new JSONHandler whose attributes consists
// of h's attributes followed by attrs.
func (h *JSONHandler) With(attrs []Attr) Handler {
	return &JSONHandler{commonHandler: h.commonHandler.with(attrs)}
}

// Handle formats its argument Record as a JSON object on a single line.
//
// If the Record's time is zero, the time is omitted.
// Otherwise, the key is "time"
// and the value is output as with json.Marshal.
//
// If the Record's level is zero, the level is omitted.
// Otherwise, the key is "level"
// and the value of [Level.String] is output.
//
// If the AddSource option is set and source information is available,
// the key is "source"
// and the value is output as "FILE:LINE".
//
// The message's key is "msg".
//
// To modify these or other attributes, or remove them from the output, use
// [HandlerOptions.ReplaceAttr].
//
// Values are formatted as with encoding/json.Marshal, with the following
// exceptions:
//   - Floating-point NaNs and infinities are formatted as one of the strings
//     "NaN", "+Inf" or "-Inf".
//   - Levels are formatted as with Level.String.
//
// Each call to Handle results in a single serialized call to io.Writer.Write.
func (h *JSONHandler) Handle(r Record) error {
	return h.commonHandler.handle(r)
}

type jsonAppender struct{}

func (jsonAppender) appendStart(buf *buffer.Buffer) { buf.WriteByte('{') }
func (jsonAppender) appendEnd(buf *buffer.Buffer)   { buf.WriteByte('}') }

func (a jsonAppender) appendKey(buf *buffer.Buffer, key string) {
	a.appendString(buf, key)
	buf.WriteByte(':')
}

func (jsonAppender) appendString(buf *buffer.Buffer, s string) {
	*buf = appendQuotedJSONString(*buf, s)
}

func (jsonAppender) appendSource(buf *buffer.Buffer, file string, line int) {
	buf.WriteByte('"')
	*buf = appendJSONString(*buf, file)
	buf.WriteByte(':')
	itoa((*[]byte)(buf), line, -1)
	buf.WriteByte('"')
}

func (jsonAppender) appendTime(buf *buffer.Buffer, t time.Time) error {
	b, err := t.MarshalJSON()
	if err != nil {
		return err
	}
	buf.Write(b)
	return nil
}

func (app jsonAppender) appendAttrValue(buf *buffer.Buffer, a Attr) error {
	switch a.Kind() {
	case StringKind:
		app.appendString(buf, a.str())
	case Int64Kind:
		*buf = strconv.AppendInt(*buf, a.Int64(), 10)
	case Uint64Kind:
		*buf = strconv.AppendUint(*buf, a.Uint64(), 10)
	case Float64Kind:
		f := a.Float64()
		// json.Marshal fails on special floats, so handle them here.
		switch {
		case math.IsInf(f, 1):
			buf.WriteString(`"+Inf"`)
		case math.IsInf(f, -1):
			buf.WriteString(`"-Inf"`)
		case math.IsNaN(f):
			buf.WriteString(`"NaN"`)
		default:
			// json.Marshal is funny about floats; it doesn't
			// always match strconv.AppendFloat. So just call it.
			// That's expensive, but floats are rare.
			if err := appendJSONMarshal(buf, f); err != nil {
				return err
			}
		}
	case BoolKind:
		*buf = strconv.AppendBool(*buf, a.Bool())
	case DurationKind:
		// Do what json.Marshal does.
		*buf = strconv.AppendInt(*buf, int64(a.Duration()), 10)
	case TimeKind:
		if err := app.appendTime(buf, a.Time()); err != nil {
			return err
		}
	case AnyKind:
		if err := appendJSONMarshal(buf, a.Value()); err != nil {
			return err
		}
	default:
		panic(fmt.Sprintf("bad kind: %d", a.Kind()))
	}
	return nil
}

func appendJSONMarshal(buf *buffer.Buffer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	buf.Write(b)
	return nil
}

func appendQuotedJSONString(buf []byte, s string) []byte {
	buf = append(buf, '"')
	buf = appendJSONString(buf, s)
	return append(buf, '"')
}

// appendJSONString escapes s for JSON and appends it to buf.
// It does not surround the string in quotation marks.
//
// Modified from encoding/json/encode.go:encodeState.string,
// with escapeHTML set to true.
//
// TODO: review whether HTML escaping is necessary.
func appendJSONString(buf []byte, s string) []byte {
	char := func(b byte) { buf = append(buf, b) }
	str := func(s string) { buf = append(buf, s...) }

	start := 0
	for i := 0; i < len(s); {
		if b := s[i]; b < utf8.RuneSelf {
			if htmlSafeSet[b] {
				i++
				continue
			}
			if start < i {
				str(s[start:i])
			}
			char('\\')
			switch b {
			case '\\', '"':
				char(b)
			case '\n':
				char('n')
			case '\r':
				char('r')
			case '\t':
				char('t')
			default:
				// This encodes bytes < 0x20 except for \t, \n and \r.
				// It also escapes <, >, and &
				// because they can lead to security holes when
				// user-controlled strings are rendered into JSON
				// and served to some browsers.
				str(`u00`)
				char(hex[b>>4])
				char(hex[b&0xF])
			}
			i++
			start = i
			continue
		}
		c, size := utf8.DecodeRuneInString(s[i:])
		if c == utf8.RuneError && size == 1 {
			if start < i {
				str(s[start:i])
			}
			str(`\ufffd`)
			i += size
			start = i
			continue
		}
		// U+2028 is LINE SEPARATOR.
		// U+2029 is PARAGRAPH SEPARATOR.
		// They are both technically valid characters in JSON strings,
		// but don't work in JSONP, which has to be evaluated as JavaScript,
		// and can lead to security holes there. It is valid JSON to
		// escape them, so we do so unconditionally.
		// See http://timelessrepo.com/json-isnt-a-javascript-subset for discussion.
		if c == '\u2028' || c == '\u2029' {
			if start < i {
				str(s[start:i])
			}
			str(`\u202`)
			char(hex[c&0xF])
			i += size
			start = i
			continue
		}
		i += size
	}
	if start < len(s) {
		str(s[start:])
	}
	return buf
}

var hex = "0123456789abcdef"

// Copied from encoding/json/encode.go:encodeState.string.
//
// htmlSafeSet holds the value true if the ASCII character with the given
// array position can be safely represented inside a JSON string, embedded
// inside of HTML <script> tags, without any additional escaping.
//
// All values are true except for the ASCII control characters (0-31), the
// double quote ("), the backslash character ("\"), HTML opening and closing
// tags ("<" and ">"), and the ampersand ("&").
var htmlSafeSet = [utf8.RuneSelf]bool{
	' ':      true,
	'!':      true,
	'"':      false,
	'#':      true,
	'$':      true,
	'%':      true,
	'&':      false,
	'\'':     true,
	'(':      true,
	')':      true,
	'*':      true,
	'+':      true,
	',':      true,
	'-':      true,
	'.':      true,
	'/':      true,
	'0':      true,
	'1':      true,
	'2':      true,
	'3':      true,
	'4':      true,
	'5':      true,
	'6':      true,
	'7':      true,
	'8':      true,
	'9':      true,
	':':      true,
	';':      true,
	'<':      false,
	'=':      true,
	'>':      false,
	'?':      true,
	'@':      true,
	'A':      true,
	'B':      true,
	'C':      true,
	'D':      true,
	'E':      true,
	'F':      true,
	'G':      true,
	'H':      true,
	'I':      true,
	'J':      true,
	'K':      true,
	'L':      true,
	'M':      true,
	'N':      true,
	'O':      true,
	'P':      true,
	'Q':      true,
	'R':      true,
	'S':      true,
	'T':      true,
	'U':      true,
	'V':      true,
	'W':      true,
	'X':      true,
	'Y':      true,
	'Z':      true,
	'[':      true,
	'\\':     false,
	']':      true,
	'^':      true,
	'_':      true,
	'`':      true,
	'a':      true,
	'b':      true,
	'c':      true,
	'd':      true,
	'e':      true,
	'f':      true,
	'g':      true,
	'h':      true,
	'i':      true,
	'j':      true,
	'k':      true,
	'l':      true,
	'm':      true,
	'n':      true,
	'o':      true,
	'p':      true,
	'q':      true,
	'r':      true,
	's':      true,
	't':      true,
	'u':      true,
	'v':      true,
	'w':      true,
	'x':      true,
	'y':      true,
	'z':      true,
	'{':      true,
	'|':      true,
	'}':      true,
	'~':      true,
	'\u007f': true,
}
