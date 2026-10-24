package api

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"sync"
)

var errRequestByteBudget = errors.New("retained request byte budget exceeded")

type requestByteContextKey struct{}

// requestByteBudget bounds request-owned strings across preparation, queueing,
// and execution. It is not an RSS limit: decoder scratch, responses, and the
// sandbox have separate concurrency and resource limits.
type requestByteBudget struct {
	mu          sync.Mutex
	limit, used int64
}

type requestByteReservation struct {
	budget *requestByteBudget
	held   int64 // Protected by budget.mu.
}

func (r *requestByteReservation) resize(n int64) error {
	r.budget.mu.Lock()
	defer r.budget.mu.Unlock()
	delta := n - r.held
	if n < 0 || delta > r.budget.limit-r.budget.used {
		return errRequestByteBudget
	}
	r.budget.used += delta
	r.held = n
	return nil
}

func retainRequestBytes(ctx context.Context, req any) error {
	r, _ := ctx.Value(requestByteContextKey{}).(*requestByteReservation)
	if r == nil {
		return nil
	}
	return r.resize(requestStringBytes(reflect.ValueOf(req)))
}

// Request models contain strings, structs, slices, and string-keyed maps. Count
// every string (including metadata) so newly added payload fields cannot silently
// escape the retained-data budget. Structural allocation and decoder scratch
// are outside this payload budget; collection limits are enforced separately.
func requestStringBytes(v reflect.Value) int64 {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			return requestStringBytes(v.Elem())
		}
	case reflect.String:
		return int64(v.Len())
	case reflect.Struct:
		var n int64
		for i := 0; i < v.NumField(); i++ {
			n += requestStringBytes(v.Field(i))
		}
		return n
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice {
			// Duplicate JSON arrays can shrink while their backing allocation
			// still retains strings beyond len. Charge those references too.
			v = v.Slice(0, v.Cap())
		}
		var n int64
		for i := 0; i < v.Len(); i++ {
			n += requestStringBytes(v.Index(i))
		}
		return n
	case reflect.Map:
		var n int64
		it := v.MapRange()
		for it.Next() {
			n += requestStringBytes(it.Key()) + requestStringBytes(it.Value())
		}
		return n
	}
	return 0
}

// Reserve the maximum replacement string before downloading. The caller shrinks
// the reservation to its actual retained size, or releases it on failure. This
// deliberately rejects before fetching if the worst-case field will not fit.
func reservePayloadBytes(ctx context.Context, maximum int64) (func(int64), error) {
	r, _ := ctx.Value(requestByteContextKey{}).(*requestByteReservation)
	if r == nil {
		return func(int64) {}, nil
	}
	r.budget.mu.Lock()
	if maximum < 0 || maximum > r.budget.limit-r.budget.used {
		r.budget.mu.Unlock()
		return nil, errRequestByteBudget
	}
	r.budget.used += maximum
	r.held += maximum
	r.budget.mu.Unlock()
	return func(actual int64) {
		r.budget.mu.Lock()
		defer r.budget.mu.Unlock()
		r.budget.used -= maximum - actual
		r.held -= maximum - actual
	}, nil
}

// Closing a restored HMAC body must drop its bytes even when an outer request
// copy still holds the ReadCloser during queueing or execution.
type restoredRequestBody struct{ *bytes.Reader }

func (b *restoredRequestBody) Close() error {
	b.Reset(nil)
	return nil
}
