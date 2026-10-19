package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

var errUnexpectedTrailingJSON = errors.New("unexpected trailing JSON value")

func checkJSONBodySize(r *http.Request) error {
	if r.ContentLength > maxJSONBodyBytes {
		return &http.MaxBytesError{Limit: maxJSONBodyBytes}
	}
	return nil
}

func writeRequestBodyError(w http.ResponseWriter, err error) {
	var maxErr *http.MaxBytesError
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	var collectionErr *jsonCollectionLimitError
	switch {
	case errors.As(err, &maxErr):
		writeJSONErrorMessage(w, http.StatusRequestEntityTooLarge, "request_body_too_large", fmt.Sprintf("request body too large: max %d bytes", maxErr.Limit))
	case errors.Is(err, errUnexpectedTrailingJSON):
		writeJSONErrorMessage(w, http.StatusBadRequest, "unexpected_trailing_json", "request body must contain exactly one JSON value")
	case errors.As(err, &collectionErr):
		code := "invalid_request"
		if collectionErr.field == "sources" {
			code = "too_many_sources"
		}
		writeJSONErrorMessage(w, http.StatusBadRequest, code, err.Error())
	case errors.As(err, &typeErr):
		writeJSONErrorMessage(w, http.StatusBadRequest, "invalid_json_type", "invalid json: "+err.Error())
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		writeJSONErrorMessage(w, http.StatusBadRequest, "unknown_json_field", "invalid json: "+err.Error())
	case errors.As(err, &syntaxErr), errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		writeJSONErrorMessage(w, http.StatusBadRequest, "invalid_json", "invalid json: "+err.Error())
	default:
		writeJSONErrorMessage(w, http.StatusBadRequest, "invalid_request_body", "invalid request body")
	}
}

func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) error {
	if err := checkJSONBodySize(r); err != nil {
		return err
	}
	body := http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := decodeBoundedJSON(dec, dst); err != nil {
		return err
	}
	// Decode without a request schema so every second JSON value is classified
	// as trailing data, including objects with fields unknown to the request.
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errUnexpectedTrailingJSON
		}
		return err
	}
	if err := body.Close(); err != nil {
		return err
	}
	r.Body = http.NoBody
	r.ContentLength = 0
	return nil
}
