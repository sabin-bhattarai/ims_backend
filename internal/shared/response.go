package shared

import "github.com/gofiber/fiber/v2"

// Envelope documents the success response shape for every endpoint. It exists
// primarily so swaggo can render it in the OpenAPI spec.
//
//	{ "data": <payload>, "meta": { ... } }
type Envelope struct {
	Data any `json:"data"`
	Meta any `json:"meta,omitempty"`
}

// ErrorEnvelope documents the failure response shape for every endpoint.
//
//	{ "error": { "code": "...", "message": "...", "details": { ... } } }
type ErrorEnvelope struct {
	Error *Error `json:"error"`
}

// OK writes 200 with a data envelope.
func OK(c *fiber.Ctx, data any) error {
	return c.Status(fiber.StatusOK).JSON(fiber.Map{"data": data})
}

// Created writes 201 with a data envelope.
func Created(c *fiber.Ctx, data any) error {
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{"data": data})
}

// NoContent writes 204 with an empty body.
func NoContent(c *fiber.Ctx) error { return c.SendStatus(fiber.StatusNoContent) }

// List writes 200 with a data envelope plus pagination meta.
func List(c *fiber.Ctx, data any, p PageMeta) error {
	return c.Status(fiber.StatusOK).JSON(fiber.Map{"data": data, "meta": p})
}
