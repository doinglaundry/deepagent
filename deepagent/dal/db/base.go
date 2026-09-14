package db

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"gorm.io/gorm/schema"
)

// JsonSerialiser stores arbitrary Go values as JSON in a GORM field.
// It follows GORM's serializer contract so it can be registered with
// schema.RegisterSerializer or used through a serializer tag.
type JsonSerialiser struct{}

func (JsonSerialiser) Scan(ctx context.Context, field *schema.Field, dst reflect.Value, value any) error {
	if field == nil {
		return fmt.Errorf("json serializer: field is nil")
	}
	fieldValue := reflect.New(field.FieldType)
	if value != nil {
		var data []byte
		switch v := value.(type) {
		case []byte:
			data = v
		case string:
			data = []byte(v)
		default:
			var err error
			data, err = json.Marshal(v)
			if err != nil {
				return fmt.Errorf("json serializer: marshal database value: %w", err)
			}
		}
		if len(data) > 0 && string(data) != "null" {
			if err := json.Unmarshal(data, fieldValue.Interface()); err != nil {
				return fmt.Errorf("json serializer: unmarshal field %s: %w", field.Name, err)
			}
		}
	}
	field.ReflectValueOf(ctx, dst).Set(fieldValue.Elem())
	return nil
}

func (JsonSerialiser) Value(_ context.Context, field *schema.Field, _ reflect.Value, value any) (any, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("json serializer: marshal field %s: %w", fieldName(field), err)
	}
	if string(data) == "null" {
		if field != nil && field.TagSettings["NOT NULL"] != "" {
			return "", nil
		}
		return nil, nil
	}
	return string(data), nil
}

func fieldName(field *schema.Field) string {
	if field == nil {
		return "<unknown>"
	}
	return field.Name
}

var _ schema.SerializerInterface = JsonSerialiser{}
