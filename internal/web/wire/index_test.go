package wire

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestAt_EmptyPath(t *testing.T) {
	v := []any{"a", "b"}
	got, err := At(v)
	if err != nil {
		t.Fatalf("At(empty): %v", err)
	}
	gotSlice, ok := got.([]any)
	if !ok {
		t.Fatalf("At(empty) type = %T, want []any", got)
	}
	if len(gotSlice) != len(v) || gotSlice[0] != v[0] || gotSlice[1] != v[1] {
		t.Fatalf("At(empty) = %v, want %v", got, v)
	}
}

func TestAt_OutOfRangeReturnsShapeDriftError(t *testing.T) {
	v := []any{"a"}
	_, err := At(v, 5)
	if err == nil {
		t.Fatalf("At(out-of-range): expected error")
	}
	var sde *ShapeDriftError
	if !errors.As(err, &sde) {
		t.Fatalf("At error not ShapeDriftError: %T", err)
	}
	if !errors.Is(err, ErrDecoding) {
		t.Fatalf("At error does not wrap ErrDecoding: %v", err)
	}
	if sde.Reason != "out_of_range" {
		t.Fatalf("At reason = %q, want out_of_range", sde.Reason)
	}
	if sde.Path != "[5]" {
		t.Fatalf("At path = %q, want [5]", sde.Path)
	}
}

func TestAt_NilListReturnsShapeDriftError(t *testing.T) {
	var v []any // nil
	_, err := At(v, 0)
	if err == nil {
		t.Fatalf("At(nil): expected error")
	}
	var sde *ShapeDriftError
	if !errors.As(err, &sde) {
		t.Fatalf("At error not ShapeDriftError: %T", err)
	}
	if sde.Reason != "out_of_range" {
		t.Fatalf("At reason = %q, want out_of_range", sde.Reason)
	}
}

func TestAt_NotAListReturnsShapeDriftError(t *testing.T) {
	v := "string-not-list"
	_, err := At(v, 0)
	if err == nil {
		t.Fatalf("At(string): expected error")
	}
	var sde *ShapeDriftError
	if !errors.As(err, &sde) {
		t.Fatalf("At error not ShapeDriftError: %T", err)
	}
	if sde.Reason != "not_a_list" {
		t.Fatalf("At reason = %q, want not_a_list", sde.Reason)
	}
}

func TestAt_DeepNestedPath(t *testing.T) {
	v := []any{
		[]any{
			[]any{"x", "y", "z"},
		},
	}
	got, err := At(v, 0, 0, 2)
	if err != nil {
		t.Fatalf("At deep: %v", err)
	}
	if got != "z" {
		t.Fatalf("At deep = %v, want z", got)
	}
}

func TestAt_MapNotListReturnsShapeDriftError(t *testing.T) {
	v := map[string]any{"a": 1}
	_, err := At(v, 0)
	if err == nil {
		t.Fatalf("At(map): expected error")
	}
	var sde *ShapeDriftError
	if !errors.As(err, &sde) {
		t.Fatalf("At error not ShapeDriftError: %T", err)
	}
}

func TestStr_Success(t *testing.T) {
	v := []any{"a", "b"}
	got, err := Str(v, 0)
	if err != nil {
		t.Fatalf("Str: %v", err)
	}
	if got != "a" {
		t.Fatalf("Str = %q, want a", got)
	}
}

func TestStr_TypeMismatchReturnsShapeDriftError(t *testing.T) {
	v := []any{42}
	_, err := Str(v, 0)
	if err == nil {
		t.Fatalf("Str(int): expected error")
	}
	var sde *ShapeDriftError
	if !errors.As(err, &sde) {
		t.Fatalf("Str error not ShapeDriftError: %T", err)
	}
	if sde.Reason != "not_a_string" {
		t.Fatalf("Str reason = %q, want not_a_string", sde.Reason)
	}
	if sde.GotType != "int" {
		t.Fatalf("Str GotType = %q, want int", sde.GotType)
	}
}

func TestInt_JSONNumber(t *testing.T) {
	doc := []byte(`{"id": 9007199254740993}`)
	var raw map[string]any
	if err := Unmarshal(doc, &raw); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	got, err := Int(raw, "id")
	if err != nil {
		t.Fatalf("Int: %v", err)
	}
	// 2^53 + 1 — fits in int64 but loses precision in float64, so it
	// exercises the json.Number path through wire.Unmarshal's UseNumber.
	const want int64 = 9007199254740993
	if got != want {
		t.Fatalf("Int = %d, want %d", got, want)
	}
}

func TestInt_NativeInt(t *testing.T) {
	v := []any{42}
	got, err := Int(v, 0)
	if err != nil {
		t.Fatalf("Int: %v", err)
	}
	if got != 42 {
		t.Fatalf("Int = %d, want 42", got)
	}
}

func TestInt_FloatThatIsInteger(t *testing.T) {
	v := []any{float64(7)}
	got, err := Int(v, 0)
	if err != nil {
		t.Fatalf("Int(float=integer): %v", err)
	}
	if got != 7 {
		t.Fatalf("Int = %d, want 7", got)
	}
}

func TestInt_FloatThatIsFractionalReturnsShapeDriftError(t *testing.T) {
	v := []any{3.14}
	_, err := Int(v, 0)
	if err == nil {
		t.Fatalf("Int(fractional): expected error")
	}
	var sde *ShapeDriftError
	if !errors.As(err, &sde) {
		t.Fatalf("Int error not ShapeDriftError: %T", err)
	}
	if sde.Reason != "not_an_int" {
		t.Fatalf("Int reason = %q, want not_an_int", sde.Reason)
	}
}

func TestInt_BoolRejected(t *testing.T) {
	v := []any{true}
	_, err := Int(v, 0)
	if err == nil {
		t.Fatalf("Int(bool): expected error")
	}
	var sde *ShapeDriftError
	if !errors.As(err, &sde) {
		t.Fatalf("Int error not ShapeDriftError: %T", err)
	}
	if sde.Reason != "not_an_int" {
		t.Fatalf("Int reason = %q, want not_an_int", sde.Reason)
	}
}

func TestInt_NilPathReturnsShapeDriftError(t *testing.T) {
	v := []any{nil}
	_, err := Int(v, 0)
	if err == nil {
		t.Fatalf("Int(nil): expected error")
	}
	var sde *ShapeDriftError
	if !errors.As(err, &sde) {
		t.Fatalf("Int error not ShapeDriftError: %T", err)
	}
}

func TestBool_Success(t *testing.T) {
	v := []any{true, false}
	got, err := Bool(v, 1)
	if err != nil {
		t.Fatalf("Bool: %v", err)
	}
	if got != false {
		t.Fatalf("Bool = %v, want false", got)
	}
}

func TestBool_TypeMismatchReturnsShapeDriftError(t *testing.T) {
	v := []any{"true"}
	_, err := Bool(v, 0)
	if err == nil {
		t.Fatalf("Bool(string): expected error")
	}
	var sde *ShapeDriftError
	if !errors.As(err, &sde) {
		t.Fatalf("Bool error not ShapeDriftError: %T", err)
	}
	if sde.Reason != "not_a_bool" {
		t.Fatalf("Bool reason = %q, want not_a_bool", sde.Reason)
	}
}

func TestBool_JSONNumberRejected(t *testing.T) {
	// json.Number 0 must not pass as bool — the contract is strict.
	v := []any{json.Number("0")}
	_, err := Bool(v, 0)
	if err == nil {
		t.Fatalf("Bool(json.Number): expected error")
	}
	var sde *ShapeDriftError
	if !errors.As(err, &sde) {
		t.Fatalf("Bool error not ShapeDriftError: %T", err)
	}
}

func TestList_Success(t *testing.T) {
	v := []any{[]any{"a", "b"}}
	got, err := List(v, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List len = %d, want 2", len(got))
	}
}

func TestList_EmptyListIsNotAnError(t *testing.T) {
	v := []any{[]any{}}
	got, err := List(v, 0)
	if err != nil {
		t.Fatalf("List(empty): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("List len = %d, want 0", len(got))
	}
}

func TestList_NilListIsNotAnError(t *testing.T) {
	// nil at the slot is surfaced as an empty list so callers can
	// uniformly check len(). matches Python decoder behavior.
	v := []any{nil}
	got, err := List(v, 0)
	if err != nil {
		t.Fatalf("List(nil): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("List len = %d, want 0", len(got))
	}
}

func TestList_TypeMismatchReturnsShapeDriftError(t *testing.T) {
	v := []any{"string-not-list"}
	_, err := List(v, 0)
	if err == nil {
		t.Fatalf("List(string): expected error")
	}
	var sde *ShapeDriftError
	if !errors.As(err, &sde) {
		t.Fatalf("List error not ShapeDriftError: %T", err)
	}
	if sde.Reason != "not_a_list_value" {
		t.Fatalf("List reason = %q, want not_a_list_value", sde.Reason)
	}
}

func TestList_StringSliceIsAccepted(t *testing.T) {
	v := []any{[]string{"a", "b"}}
	got, err := List(v, 0)
	if err != nil {
		t.Fatalf("List(string slice): %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List len = %d, want 2", len(got))
	}
}

func TestOptStr_MissingReturnsZeroValueAndFalse(t *testing.T) {
	v := []any{"a"}
	s, ok := OptStr(v, 5)
	if ok {
		t.Fatalf("OptStr(missing) ok = true, want false")
	}
	if s != "" {
		t.Fatalf("OptStr(missing) = %q, want empty", s)
	}
}

func TestOptStr_PresentReturnsValueAndTrue(t *testing.T) {
	v := []any{"hello"}
	s, ok := OptStr(v, 0)
	if !ok {
		t.Fatalf("OptStr(present) ok = false, want true")
	}
	if s != "hello" {
		t.Fatalf("OptStr(present) = %q, want hello", s)
	}
}

func TestOptStr_NilValueIsSilent(t *testing.T) {
	v := []any{nil}
	_, ok := OptStr(v, 0)
	if ok {
		t.Fatalf("OptStr(nil) ok = true, want false (missing)")
	}
}

func TestOptInt_MissingReturnsZeroValueAndFalse(t *testing.T) {
	v := []any{"a"}
	i, ok := OptInt(v, 0)
	if ok {
		t.Fatalf("OptInt(missing) ok = true, want false")
	}
	if i != 0 {
		t.Fatalf("OptInt(missing) = %d, want 0", i)
	}
}

func TestOptInt_PresentJSONNumber(t *testing.T) {
	doc := []byte(`{"id":42}`)
	var raw map[string]any
	if err := Unmarshal(doc, &raw); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	i, ok := OptInt(raw, "id")
	if !ok {
		t.Fatalf("OptInt(present) ok = false, want true")
	}
	if i != 42 {
		t.Fatalf("OptInt(present) = %d, want 42", i)
	}
}

func TestOptBool_MissingReturnsFalseAndFalse(t *testing.T) {
	v := []any{}
	b, ok := OptBool(v, 0)
	if ok {
		t.Fatalf("OptBool(missing) ok = true, want false")
	}
	if b != false {
		t.Fatalf("OptBool(missing) = true, want false")
	}
}

func TestOptBool_PresentReturnsValueAndTrue(t *testing.T) {
	v := []any{true}
	b, ok := OptBool(v, 0)
	if !ok {
		t.Fatalf("OptBool(present) ok = false, want true")
	}
	if b != true {
		t.Fatalf("OptBool(present) = false, want true")
	}
}

func TestOptList_MissingReturnsNilAndFalse(t *testing.T) {
	v := []any{}
	l, ok := OptList(v, 0)
	if ok {
		t.Fatalf("OptList(missing) ok = true, want false")
	}
	if l != nil {
		t.Fatalf("OptList(missing) = %v, want nil", l)
	}
}

func TestOptList_PresentEmptyListReturnsEmptyListAndTrue(t *testing.T) {
	v := []any{[]any{}}
	l, ok := OptList(v, 0)
	if !ok {
		t.Fatalf("OptList(empty) ok = false, want true (present-empty)")
	}
	if len(l) != 0 {
		t.Fatalf("OptList(empty) len = %d, want 0", len(l))
	}
}

func TestOptList_PresentReturnsValueAndTrue(t *testing.T) {
	v := []any{[]any{"a", "b"}}
	l, ok := OptList(v, 0)
	if !ok {
		t.Fatalf("OptList(present) ok = false, want true")
	}
	if len(l) != 2 {
		t.Fatalf("OptList(present) len = %d, want 2", len(l))
	}
}

func TestShapeDriftError_MethodField(t *testing.T) {
	err := &ShapeDriftError{Path: "[0]", Method: "ListNotebooks", Reason: "out_of_range"}
	got := err.Error()
	if !strings.Contains(got, "ListNotebooks") {
		t.Fatalf("Error() = %q, want it to include method name", got)
	}
	if !strings.Contains(got, "[0]") {
		t.Fatalf("Error() = %q, want it to include path", got)
	}
}

func TestShapeDriftError_NoMethod(t *testing.T) {
	err := &ShapeDriftError{Path: "[1]", Reason: "not_a_string", GotType: "int"}
	got := err.Error()
	if strings.Contains(got, "on ") {
		t.Fatalf("Error() = %q, want no 'on' prefix without method", got)
	}
}

func TestAt_StringKeyInMap(t *testing.T) {
	v := map[string]any{"k": "v"}
	got, err := At(v, "k")
	if err != nil {
		t.Fatalf("At(map[string]any): %v", err)
	}
	if got != "v" {
		t.Fatalf("At(map) = %v, want v", got)
	}
}

func TestAt_MissingStringKeyReturnsShapeDriftError(t *testing.T) {
	v := map[string]any{"k": "v"}
	_, err := At(v, "missing")
	if err == nil {
		t.Fatalf("At(missing key): expected error")
	}
	var sde *ShapeDriftError
	if !errors.As(err, &sde) {
		t.Fatalf("At error not ShapeDriftError: %T", err)
	}
	if sde.Reason != "out_of_range" {
		t.Fatalf("At reason = %q, want out_of_range", sde.Reason)
	}
}

func TestAt_NonStringKeyInMapReturnsShapeDriftError(t *testing.T) {
	v := map[string]any{"k": "v"}
	_, err := At(v, 0)
	if err == nil {
		t.Fatalf("At(int-key into map): expected error")
	}
	var sde *ShapeDriftError
	if !errors.As(err, &sde) {
		t.Fatalf("At error not ShapeDriftError: %T", err)
	}
}

func TestAt_NonIntIndexInListReturnsShapeDriftError(t *testing.T) {
	v := []any{"a"}
	_, err := At(v, "not-an-int")
	if err == nil {
		t.Fatalf("At(string-key into list): expected error")
	}
	var sde *ShapeDriftError
	if !errors.As(err, &sde) {
		t.Fatalf("At error not ShapeDriftError: %T", err)
	}
}

func TestAt_StringSliceIndexing(t *testing.T) {
	v := []any{[]string{"a", "b"}}
	got, err := At(v, 0, 1)
	if err != nil {
		t.Fatalf("At(string-slice): %v", err)
	}
	if got != "b" {
		t.Fatalf("At(string-slice) = %v, want b", got)
	}
}

func TestInt_JSONNumberOutOfRange(t *testing.T) {
	// A json.Number that overflows int64 must return a ShapeDriftError,
	// not a strconv error wrapped in a generic error.
	doc := []byte(`{"id": 99999999999999999999999999}`)
	var raw map[string]any
	if err := Unmarshal(doc, &raw); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	_, err := Int(raw, "id")
	if err == nil {
		t.Fatalf("Int(overflow): expected error")
	}
	var sde *ShapeDriftError
	if !errors.As(err, &sde) {
		t.Fatalf("Int error not ShapeDriftError: %T", err)
	}
}

func TestOptList_StringSlice(t *testing.T) {
	v := []any{[]string{"a", "b"}}
	l, ok := OptList(v, 0)
	if !ok {
		t.Fatalf("OptList(string slice) ok = false, want true")
	}
	if len(l) != 2 {
		t.Fatalf("OptList(string slice) len = %d, want 2", len(l))
	}
}

func TestOptList_NilValue(t *testing.T) {
	v := []any{nil}
	l, ok := OptList(v, 0)
	if !ok {
		t.Fatalf("OptList(nil) ok = false, want true (present-null)")
	}
	if len(l) != 0 {
		t.Fatalf("OptList(nil) len = %d, want 0", len(l))
	}
}

func TestFormatPath_StringKey(t *testing.T) {
	got := formatPath([]any{"key"})
	if !strings.Contains(got, "key") {
		t.Fatalf("formatPath(string) = %q, want it to contain key", got)
	}
}

func TestFormatPath_OtherType(t *testing.T) {
	// Anything other than int/string falls through to %v.
	got := formatPath([]any{true})
	if !strings.Contains(got, "true") {
		t.Fatalf("formatPath(bool) = %q, want it to contain 'true'", got)
	}
}

func TestShapeDriftError_UnwrapReturnsErrDecoding(t *testing.T) {
	err := &ShapeDriftError{Path: "[0]", Reason: "out_of_range"}
	if !errors.Is(err, ErrDecoding) {
		t.Fatalf("ShapeDriftError does not unwrap to ErrDecoding")
	}
}
