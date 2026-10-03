package db

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

type MySQLClient struct {
	read  *gorm.DB
	write *gorm.DB
}

// NewSQL opens separate read/write connections, or reuses supplied handles.
func NewSQL(ctx context.Context, dsn, readDSN string, sharedDB ...*gorm.DB) (*MySQLClient, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var write, read *gorm.DB
	if len(sharedDB) > 0 {
		write = sharedDB[0]
	}
	if len(sharedDB) > 1 {
		read = sharedDB[1]
	}
	if write == nil {
		if strings.TrimSpace(dsn) == "" {
			return nil, fmt.Errorf("mysql: write DSN is required")
		}
		var err error
		write, err = gorm.Open(mysql.Open(dsn), &gorm.Config{})
		if err != nil {
			return nil, err
		}
	}
	if read == nil {
		if strings.TrimSpace(readDSN) == "" {
			read = write
		} else {
			var err error
			read, err = gorm.Open(mysql.Open(readDSN), &gorm.Config{})
			if err != nil {
				return nil, err
			}
		}
	}
	return &MySQLClient{read: read, write: write}, nil
}

// DB uses the transaction in ctx first, then the selected connection.
func (c *MySQLClient) DB(ctx context.Context, primary bool) *gorm.DB {
	if ctx == nil {
		ctx = context.Background()
	}
	tx, ok := ctx.Value(c).(*gorm.DB)
	if ok && tx != nil {
		return tx.WithContext(ctx)
	}
	if primary {
		return c.write.WithContext(ctx)
	}
	return c.read.WithContext(ctx)
}

func (c *MySQLClient) Transaction(ctx context.Context, fn func(context.Context) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return c.write.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(context.WithValue(ctx, c, tx))
	})
}

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
			err := json.Unmarshal(data, fieldValue.Interface())
			if err != nil {
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

func init() {
	schema.RegisterSerializer("coordinator_json", JsonSerialiser{})
}
