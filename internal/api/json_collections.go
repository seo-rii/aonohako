package api

import (
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"

	"aonohako/internal/model"
	"aonohako/internal/runvalidation"
)

type jsonCollectionField struct {
	typ  reflect.Type
	name string
}

// Every request collection must have an admission bound. Keep these tied to
// the semantic validators; the schema inventory test rejects unreviewed additions.
var jsonCollectionLimits = map[jsonCollectionField]int{
	{reflect.TypeFor[model.CompileRequest](), "Sources"}:                  maxCompileSourceFiles,
	{reflect.TypeFor[model.RunRequest](), "Binaries"}:                     runvalidation.MaxBinaryFiles,
	{reflect.TypeFor[model.RunRequest](), "Programs"}:                     runvalidation.MaxPrograms,
	{reflect.TypeFor[model.RunRequest](), "Steps"}:                        runvalidation.MaxSteps,
	{reflect.TypeFor[model.RunRequest](), "FileOutputs"}:                  1,
	{reflect.TypeFor[model.RunRequest](), "SidecarOutputs"}:               runvalidation.MaxSidecarOutputs,
	{reflect.TypeFor[model.RunProgram](), "Binaries"}:                     runvalidation.MaxBinaryFiles,
	{reflect.TypeFor[model.RunStep](), "StdinParts"}:                      runvalidation.MaxStdinParts,
	{reflect.TypeFor[model.InteractorSpec](), "Binaries"}:                 runvalidation.MaxBinaryFiles,
	{reflect.TypeFor[model.CommunicationSpec](), "ParticipantProgramIDs"}: 2,
	{reflect.TypeFor[model.SPJSpec](), "SidecarOutputs"}:                  runvalidation.MaxSidecarOutputs,
	{reflect.TypeFor[model.PipelineV1](), "Resources"}:                    runvalidation.MaxPipelineResources,
	{reflect.TypeFor[model.PipelineV1](), "Programs"}:                     runvalidation.MaxPrograms,
	{reflect.TypeFor[model.PipelineV1](), "Steps"}:                        runvalidation.MaxPipelineSteps,
	{reflect.TypeFor[model.PipelineStep](), "Stdin"}:                      runvalidation.MaxStdinParts,
	{reflect.TypeFor[model.PipelineStep](), "Outputs"}:                    runvalidation.MaxPipelineOutputs,
}

type jsonCollectionLimitError struct {
	field string
	limit int
}

func (e *jsonCollectionLimitError) Error() string {
	return fmt.Sprintf("too many %s: max %d", e.field, e.limit)
}

type jsonDecodeField struct {
	name   string
	index  int
	schema *jsonDecodeSchema
}

type jsonDecodeSchema struct {
	stream bool
	limit  int
	fields []jsonDecodeField
	elem   *jsonDecodeSchema
}

var requestJSONSchemas = func() map[reflect.Type]*jsonDecodeSchema {
	schemas := make(map[reflect.Type]*jsonDecodeSchema)
	for _, typ := range []reflect.Type{reflect.TypeFor[model.CompileRequest](), reflect.TypeFor[model.RunRequest]()} {
		schema, err := buildJSONDecodeSchema(typ, 0)
		if err != nil {
			panic(err) // A new model collection requires an explicit admission policy.
		}
		schemas[typ] = schema
	}
	return schemas
}()

func buildJSONDecodeSchema(typ reflect.Type, limit int) (*jsonDecodeSchema, error) {
	if typ.Kind() == reflect.Pointer {
		return buildJSONDecodeSchema(typ.Elem(), limit)
	}
	if reflect.PointerTo(typ).Implements(reflect.TypeFor[json.Unmarshaler]()) || reflect.PointerTo(typ).Implements(reflect.TypeFor[encoding.TextUnmarshaler]()) {
		return nil, fmt.Errorf("request JSON type %s has an unreviewed custom decoder", typ)
	}
	schema := &jsonDecodeSchema{limit: limit}
	switch typ.Kind() {
	case reflect.Slice, reflect.Map:
		if limit <= 0 || (typ.Kind() == reflect.Map && typ.Key().Kind() != reflect.String) {
			return nil, fmt.Errorf("JSON collection %s needs an explicit admission limit", typ)
		}
		elem, err := buildJSONDecodeSchema(typ.Elem(), 0)
		if err != nil {
			return nil, err
		}
		schema.stream, schema.elem = true, elem
	case reflect.Struct:
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if !field.IsExported() || field.Tag.Get("json") == "-" {
				continue
			}
			name, option, _ := strings.Cut(field.Tag.Get("json"), ",")
			if field.Anonymous || (option != "" && option != "omitempty") {
				return nil, fmt.Errorf("unsupported request JSON field %s.%s", typ, field.Name)
			}
			if name == "" {
				name = field.Name
			}
			child, err := buildJSONDecodeSchema(field.Type, jsonCollectionLimits[jsonCollectionField{typ, field.Name}])
			if err != nil {
				return nil, fmt.Errorf("%s.%s: %w", typ, field.Name, err)
			}
			schema.stream = schema.stream || child.stream
			schema.fields = append(schema.fields, jsonDecodeField{name: name, index: i, schema: child})
		}
	case reflect.Interface, reflect.Array:
		return nil, fmt.Errorf("unsupported request JSON type %s", typ)
	}
	return schema, nil
}

type boundedJSONDecoder struct {
	dec      *json.Decoder
	firstErr error
}

// Decode still checks the skipped value's complete JSON syntax, but unlike
// RawMessage this does not copy its bytes into a second potentially large buffer.
type discardedJSONValue struct{}

func (*discardedJSONValue) UnmarshalJSON([]byte) error { return nil }

// Only objects containing collections and their collection containers are
// decoded a token at a time. Ordinary fields retain encoding/json's decoding,
// type checking and unknown-field behavior without a second whole-body buffer.
func decodeBoundedJSON(dec *json.Decoder, dst any) error {
	value := reflect.ValueOf(dst).Elem()
	schema := requestJSONSchemas[value.Type()]
	if schema == nil {
		return fmt.Errorf("unsupported request JSON destination %s", value.Type())
	}
	dec.UseNumber()
	decoder := boundedJSONDecoder{dec: dec}
	if err := decoder.value(value, schema, "", ""); err != nil {
		var limitErr *jsonCollectionLimitError
		if errors.As(err, &limitErr) && decoder.firstErr != nil {
			return decoder.firstErr
		}
		return err
	}
	return decoder.firstErr
}

func (d *boundedJSONDecoder) remember(err error, path, parent string) error {
	if err == nil {
		return nil
	}
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		if typeErr.Field == "" {
			typeErr.Struct, typeErr.Field = parent, path
		} else if path != "" {
			typeErr.Field = path + "." + typeErr.Field
		}
	} else if !strings.HasPrefix(err.Error(), "json: unknown field ") {
		return err
	}
	if d.firstErr == nil {
		d.firstErr = err
	}
	return nil
}

func (d *boundedJSONDecoder) value(value reflect.Value, schema *jsonDecodeSchema, path, parent string) error {
	if !schema.stream {
		return d.remember(d.dec.Decode(value.Addr().Interface()), path, parent)
	}
	token, err := d.dec.Token()
	if err != nil {
		return err
	}
	if token == nil {
		switch value.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Map:
			value.SetZero()
		}
		return nil
	}
	for value.Kind() == reflect.Pointer {
		if value.IsNil() {
			value.Set(reflect.New(value.Type().Elem()))
		}
		value = value.Elem()
	}
	want := json.Delim('{')
	if value.Kind() == reflect.Slice {
		want = '['
	}
	if token != want {
		typeErr := &json.UnmarshalTypeError{Value: jsonTokenType(token), Type: value.Type(), Offset: d.dec.InputOffset()}
		_ = d.remember(typeErr, path, parent)
		return d.discardOpened(token)
	}
	switch value.Kind() {
	case reflect.Struct:
		for d.dec.More() {
			key, err := d.dec.Token()
			if err != nil {
				return err
			}
			field := schema.field(key.(string))
			if field == nil {
				_ = d.remember(fmt.Errorf("json: unknown field %q", key), "", "")
				var discard discardedJSONValue
				if err := d.dec.Decode(&discard); err != nil {
					return err
				}
				continue
			}
			fieldPath := field.name
			if path != "" {
				fieldPath = path + "." + fieldPath
			}
			if err := d.value(value.Field(field.index), field.schema, fieldPath, value.Type().Name()); err != nil {
				return err
			}
		}
	case reflect.Slice:
		n := 0
		for d.dec.More() {
			if n >= schema.limit {
				return &jsonCollectionLimitError{field: path, limit: schema.limit}
			}
			// Like encoding/json, reuse existing backing elements when duplicate
			// array fields shrink and later grow again within the same request.
			if n >= value.Cap() {
				value.Grow(1)
			}
			if n >= value.Len() {
				value.SetLen(n + 1)
			}
			if err := d.value(value.Index(n), schema.elem, path, parent); err != nil {
				return err
			}
			n++
		}
		if n == 0 {
			value.Set(reflect.MakeSlice(value.Type(), 0, 0))
		} else {
			value.SetLen(n)
		}
	case reflect.Map:
		if value.IsNil() {
			value.Set(reflect.MakeMap(value.Type()))
		}
		for d.dec.More() {
			key, err := d.dec.Token()
			if err != nil {
				return err
			}
			mapKey := reflect.ValueOf(key.(string))
			if !value.MapIndex(mapKey).IsValid() && value.Len() >= schema.limit {
				return &jsonCollectionLimitError{field: path, limit: schema.limit}
			}
			elem := reflect.New(value.Type().Elem()).Elem()
			if err := d.value(elem, schema.elem, path, parent); err != nil {
				return err
			}
			value.SetMapIndex(mapKey, elem)
		}
	}
	_, err = d.dec.Token() // encoding/json validates the matching closing delimiter.
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

func (s *jsonDecodeSchema) field(key string) *jsonDecodeField {
	for i := range s.fields {
		if s.fields[i].name == key {
			return &s.fields[i]
		}
	}
	for i := range s.fields {
		if strings.EqualFold(s.fields[i].name, key) {
			return &s.fields[i]
		}
	}
	return nil
}

func jsonTokenType(token json.Token) string {
	switch token.(type) {
	case json.Number:
		return "number"
	case string:
		return "string"
	case bool:
		return "bool"
	default:
		if token == json.Delim('[') {
			return "array"
		}
		return "object"
	}
}

// The opening delimiter was already consumed for a wrongly typed field. Skip
// complete child values with the standard decoder, preserving syntax errors
// without constructing an unbounded []any/map[string]any or recursing ourselves.
func (d *boundedJSONDecoder) discardOpened(token json.Token) error {
	if token != json.Delim('[') && token != json.Delim('{') {
		return nil
	}
	for d.dec.More() {
		if token == json.Delim('{') {
			if _, err := d.dec.Token(); err != nil {
				return err
			}
		}
		var discard discardedJSONValue
		if err := d.dec.Decode(&discard); err != nil {
			return err
		}
	}
	_, err := d.dec.Token()
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}
