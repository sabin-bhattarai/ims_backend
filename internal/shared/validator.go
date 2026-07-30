package shared

import (
	"reflect"
	"strings"
	"sync"

	"github.com/go-playground/validator/v10"
	"github.com/gofiber/fiber/v2"
)

var (
	validate     *validator.Validate
	validateOnce sync.Once
)

func instance() *validator.Validate {
	validateOnce.Do(func() {
		validate = validator.New(validator.WithRequiredStructEnabled())
		// Report the JSON field name, not the Go field name: the client sent
		// `full_name` and that is what the error should point at.
		validate.RegisterTagNameFunc(func(f reflect.StructField) string {
			name := strings.SplitN(f.Tag.Get("json"), ",", 2)[0]
			if name == "-" || name == "" {
				return f.Name
			}
			return name
		})
	})
	return validate
}

// BindAndValidate decodes a JSON request body into dst and validates it,
// returning a 400 with per-field details on failure.
func BindAndValidate(c *fiber.Ctx, dst any) error {
	if err := c.BodyParser(dst); err != nil {
		return Validation("request body is not valid JSON").WithCause(err)
	}
	return Validate(dst)
}

// Validate runs struct validation and converts the result into an *Error whose
// details map field name to a human-readable message.
func Validate(dst any) error {
	err := instance().Struct(dst)
	if err == nil {
		return nil
	}

	var invalid *validator.InvalidValidationError
	if ok := asInvalid(err, &invalid); ok {
		return Internal("validation target is not a struct").WithCause(err)
	}

	details := map[string]any{}
	for _, fe := range err.(validator.ValidationErrors) {
		details[fe.Field()] = describe(fe)
	}
	return Validation("one or more fields are invalid").WithDetails(details)
}

func asInvalid(err error, target **validator.InvalidValidationError) bool {
	e, ok := err.(*validator.InvalidValidationError)
	if ok {
		*target = e
	}
	return ok
}

func describe(fe validator.FieldError) string {
	switch fe.Tag() {
	case "required":
		return "is required"
	case "email":
		return "must be a valid email address"
	case "min":
		if fe.Kind() == reflect.String {
			return "must be at least " + fe.Param() + " characters"
		}
		return "must be at least " + fe.Param()
	case "max":
		if fe.Kind() == reflect.String {
			return "must be at most " + fe.Param() + " characters"
		}
		return "must be at most " + fe.Param()
	case "gt":
		return "must be greater than " + fe.Param()
	case "gte":
		return "must be greater than or equal to " + fe.Param()
	case "lte":
		return "must be less than or equal to " + fe.Param()
	case "oneof":
		return "must be one of: " + strings.ReplaceAll(fe.Param(), " ", ", ")
	case "uuid", "uuid4":
		return "must be a valid id"
	case "url":
		return "must be a valid URL"
	case "dive", "required_without", "required_with":
		return "is required in this context"
	default:
		return "failed the " + fe.Tag() + " check"
	}
}
