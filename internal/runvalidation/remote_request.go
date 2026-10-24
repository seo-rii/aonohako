package runvalidation

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

	"aonohako/internal/model"
)

// MaxJSONRequestBytes is shared by HTTP admission and remote execution. URL
// payloads can be small on admission yet exceed this limit after expansion.
const MaxJSONRequestBytes = 64 << 20

type RemoteRequestSizeError struct {
	LimitBytes int
}

func (e *RemoteRequestSizeError) Error() string {
	return fmt.Sprintf("remote request too large: max %d JSON bytes after payload expansion", e.LimitBytes)
}

// ValidateRemoteRequestSize checks the exact json.Marshal wire size without
// allocating another copy of the expanded binary or text payloads.
func ValidateRemoteRequestSize(req *model.RunRequest) error {
	return validateRemoteRequestSize(req)
}

// ValidateRemoteCompileRequestSize applies the same wire budget to URL-expanded
// compile sources, including their base64 expansion and request metadata.
func ValidateRemoteCompileRequestSize(req *model.CompileRequest) error {
	return validateRemoteRequestSize(req)
}

func validateRemoteRequestSize(req any) error {
	counter := jsonSizeCounter{limit: MaxJSONRequestBytes}
	if err := counter.value(reflect.ValueOf(req)); err != nil {
		if errors.Is(err, errJSONSizeLimit) {
			return &RemoteRequestSizeError{LimitBytes: MaxJSONRequestBytes}
		}
		return err
	}
	return nil
}

var (
	errJSONSizeLimit  = errors.New("JSON size limit exceeded")
	jsonMarshalerType = reflect.TypeFor[json.Marshaler]()
)

type jsonSizeCounter struct {
	size  int
	limit int
}

func (c *jsonSizeCounter) add(n int) error {
	if n > c.limit-c.size {
		return errJSONSizeLimit
	}
	c.size += n
	return nil
}

func (c *jsonSizeCounter) string(s string) error {
	remaining := c.limit - c.size
	// JSON quoting never shortens these model strings. Reject an oversized
	// string before scanning it, then account for escaping exactly below.
	if remaining < 2 || len(s) > remaining-2 {
		return errJSONSizeLimit
	}
	size := 2
	for i := 0; i < len(s); {
		n := 1
		if b := s[i]; b < utf8.RuneSelf {
			switch b {
			case '"', '\\', '\b', '\f', '\n', '\r', '\t':
				n = 2
			case '<', '>', '&':
				n = 6 // json.Marshal's default HTML escaping.
			default:
				if b < 0x20 {
					n = 6
				}
			}
			i++
		} else {
			r, width := utf8.DecodeRuneInString(s[i:])
			n = width
			if (r == utf8.RuneError && width == 1) || r == '\u2028' || r == '\u2029' {
				n = 6
			}
			i += width
		}
		size += n
		if size > remaining {
			return errJSONSizeLimit
		}
	}
	return c.add(size)
}

func (c *jsonSizeCounter) value(v reflect.Value) error {
	if !v.IsValid() {
		return c.add(4) // null
	}
	// Request models have no custom encoders. Fail closed if one is introduced
	// instead of silently accepting a different wire representation.
	if v.Type().Implements(jsonMarshalerType) || (v.Kind() != reflect.Pointer && reflect.PointerTo(v.Type()).Implements(jsonMarshalerType)) {
		return fmt.Errorf("remote request size check does not support custom JSON encoder %s", v.Type())
	}
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return c.add(4)
		}
		return c.value(v.Elem())
	case reflect.String:
		return c.string(v.String())
	case reflect.Bool:
		if v.Bool() {
			return c.add(4)
		}
		return c.add(5)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		var buf [20]byte
		return c.add(len(strconv.AppendInt(buf[:0], v.Int(), 10)))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		var buf [20]byte
		return c.add(len(strconv.AppendUint(buf[:0], v.Uint(), 10)))
	case reflect.Struct:
		if err := c.add(2); err != nil {
			return err
		}
		fields := 0
		for i := 0; i < v.NumField(); i++ {
			field := v.Type().Field(i)
			if field.PkgPath != "" { // Unexported.
				continue
			}
			name, option, _ := strings.Cut(field.Tag.Get("json"), ",")
			if name == "-" {
				continue
			}
			if field.Anonymous || (option != "" && option != "omitempty") {
				return fmt.Errorf("remote request size check does not support JSON field %s.%s", v.Type(), field.Name)
			}
			value := v.Field(i)
			if option == "omitempty" && jsonSizeEmpty(value) {
				continue
			}
			if name == "" {
				name = field.Name
			}
			if fields > 0 {
				if err := c.add(1); err != nil {
					return err
				}
			}
			fields++
			if err := c.string(name); err != nil {
				return err
			}
			if err := c.add(1); err != nil {
				return err
			}
			if err := c.value(value); err != nil {
				return err
			}
		}
		return nil
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice {
			if v.IsNil() {
				return c.add(4)
			}
			if v.Type().Elem().Kind() == reflect.Uint8 {
				return c.add(2 + base64.StdEncoding.EncodedLen(v.Len()))
			}
		}
		if err := c.add(2); err != nil {
			return err
		}
		for i := 0; i < v.Len(); i++ {
			if i > 0 {
				if err := c.add(1); err != nil {
					return err
				}
			}
			if err := c.value(v.Index(i)); err != nil {
				return err
			}
		}
		return nil
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String {
			return fmt.Errorf("remote request size check does not support map %s", v.Type())
		}
		if v.IsNil() {
			return c.add(4)
		}
		if err := c.add(2); err != nil {
			return err
		}
		iter := v.MapRange()
		fields := 0
		for iter.Next() {
			if fields > 0 {
				if err := c.add(1); err != nil {
					return err
				}
			}
			fields++
			if err := c.string(iter.Key().String()); err != nil {
				return err
			}
			if err := c.add(1); err != nil {
				return err
			}
			if err := c.value(iter.Value()); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("remote request size check does not support type %s", v.Type())
	}
}

func jsonSizeEmpty(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Interface, reflect.Pointer:
		return v.IsZero()
	}
	return false
}
