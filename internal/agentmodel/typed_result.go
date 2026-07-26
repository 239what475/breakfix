package agentmodel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"

	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
)

var ErrResultAlreadySubmitted = errors.New("typed result was already submitted")

// ResultTool exposes exactly one structured completion tool to a model. It
// keeps the schema derived from the Go result type while using encoding/json's
// strict decoder: unknown fields, a second JSON document, and a caller-defined
// missing or invalid field all fail the tool call.
type ResultTool[T any] struct {
	info     *schema.ToolInfo
	validate func(T) error

	mu     sync.Mutex
	called bool
	value  T
}

func NewResultTool[T any](name, description string, validate func(T) error) (*ResultTool[T], error) {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(description) == "" || validate == nil {
		return nil, errors.New("typed result tool requires name, description, and validator")
	}
	info, err := toolutils.GoStruct2ToolInfo[T](name, description)
	if err != nil {
		return nil, fmt.Errorf("derive typed result schema: %w", err)
	}
	return &ResultTool[T]{info: info, validate: validate}, nil
}

func (t *ResultTool[T]) Info(context.Context) (*schema.ToolInfo, error) {
	return t.info, nil
}

func (t *ResultTool[T]) InvokableRun(_ context.Context, arguments string, _ ...tool.Option) (string, error) {
	value, err := decodeStrict[T](arguments)
	if err != nil {
		return "", err
	}
	if err := t.validate(value); err != nil {
		return "", err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.called {
		return "", ErrResultAlreadySubmitted
	}
	t.called = true
	t.value = value
	return `{"accepted":true}`, nil
}

func (t *ResultTool[T]) Value() (T, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.value, t.called
}

func decodeStrict[T any](arguments string) (T, error) {
	var zero T
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.DisallowUnknownFields()
	var value T
	if err := decoder.Decode(&value); err != nil {
		return zero, fmt.Errorf("decode typed result: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return zero, errors.New("typed result contains a second JSON document")
		}
		return zero, fmt.Errorf("decode typed result suffix: %w", err)
	}
	if err := requireTaggedFields[T](arguments); err != nil {
		return zero, err
	}
	return value, nil
}

func requireTaggedFields[T any](arguments string) error {
	typeOf := reflect.TypeFor[T]()
	for typeOf.Kind() == reflect.Pointer {
		typeOf = typeOf.Elem()
	}
	if typeOf.Kind() != reflect.Struct {
		return nil
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal([]byte(arguments), &values); err != nil {
		return fmt.Errorf("decode typed result fields: %w", err)
	}
	for index := 0; index < typeOf.NumField(); index++ {
		field := typeOf.Field(index)
		if field.PkgPath != "" || !tagHasRequired(field.Tag.Get("jsonschema")) {
			continue
		}
		name := jsonFieldName(field)
		if name == "" {
			continue
		}
		if _, exists := values[name]; !exists {
			return fmt.Errorf("typed result is missing required field %q", name)
		}
	}
	return nil
}

func tagHasRequired(tag string) bool {
	for _, value := range strings.Split(tag, ",") {
		if strings.TrimSpace(value) == "required" {
			return true
		}
	}
	return false
}

func jsonFieldName(field reflect.StructField) string {
	tag := field.Tag.Get("json")
	if tag == "-" {
		return ""
	}
	name, _, _ := strings.Cut(tag, ",")
	if name != "" {
		return name
	}
	return field.Name
}
