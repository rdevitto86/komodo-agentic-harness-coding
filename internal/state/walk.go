package state

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
)

var (
	timeType = reflect.TypeOf(time.Time{})
	rawType  = reflect.TypeOf(json.RawMessage(nil))
)

// walkNil returns the path of the first nil slice or map that its field tag does not allow to be absent.
// With fix set it replaces each one with an empty value and returns "".
func walkNil(v reflect.Value, path string, fix bool) string {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return ""
		}
		return walkNil(v.Elem(), path, fix)
	case reflect.Struct:
		if v.Type() == timeType {
			return ""
		}
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
			if !f.IsExported() || name == "-" || f.Type == rawType {
				continue
			}
			optional := strings.Contains(opts, "omitempty") || strings.Contains(opts, "omitzero")
			if bad := walkField(v.Field(i), path+"."+name, optional, fix); bad != "" {
				return bad
			}
		}
	}
	return ""
}

// walkField checks one field: a nil slice or map is allowed only when optional, and the contents are walked.
func walkField(f reflect.Value, path string, optional, fix bool) string {
	switch f.Kind() {
	case reflect.Slice:
		if f.IsNil() {
			if optional {
				return ""
			}
			if !fix {
				return path
			}
			f.Set(reflect.MakeSlice(f.Type(), 0, 0))
			return ""
		}
		for i := 0; i < f.Len(); i++ {
			if bad := walkNil(f.Index(i), fmt.Sprintf("%s[%d]", path, i), fix); bad != "" {
				return bad
			}
		}
	case reflect.Map:
		if f.IsNil() {
			if optional {
				return ""
			}
			if !fix {
				return path
			}
			f.Set(reflect.MakeMap(f.Type()))
			return ""
		}
		keys := f.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return fmt.Sprint(keys[i]) < fmt.Sprint(keys[j]) })
		for _, k := range keys {
			val := reflect.New(f.Type().Elem()).Elem()
			val.Set(f.MapIndex(k))
			if bad := walkNil(val, path+"."+fmt.Sprint(k), fix); bad != "" {
				return bad
			}
			if fix {
				f.SetMapIndex(k, val)
			}
		}
	default:
		return walkNil(f, path, fix)
	}
	return ""
}
