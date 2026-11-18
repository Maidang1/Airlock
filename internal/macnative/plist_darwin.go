//go:build darwin && cgo

package macnative

/*
#cgo CFLAGS: -fobjc-arc -mmacosx-version-min=14.0
#cgo LDFLAGS: -framework Cocoa -framework Security -framework LocalAuthentication
#include <stdlib.h>
#include "native_darwin.h"
*/
import "C"
import (
	"encoding/json"
	"errors"
	"unsafe"
)

// ParsePlist decodes an XML or binary macOS property list dictionary into a Go map.
func ParsePlist(data []byte) (map[string]any, error) {
	if len(data) == 0 {
		return nil, errors.New("empty plist")
	}
	var outJSON *C.char
	var outErr *C.char
	rc := C.cage_parse_plist_json(unsafe.Pointer(&data[0]), C.size_t(len(data)), &outJSON, &outErr)
	if outErr != nil {
		defer C.free(unsafe.Pointer(outErr))
	}
	if outJSON != nil {
		defer C.free(unsafe.Pointer(outJSON))
	}
	if rc != 0 {
		if outErr != nil {
			return nil, errors.New(C.GoString(outErr))
		}
		return nil, errors.New("failed to parse plist")
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(C.GoString(outJSON)), &res); err != nil {
		return nil, err
	}
	return res, nil
}
