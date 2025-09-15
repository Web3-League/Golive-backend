// api/pkg/response/response.go
package response

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-playground/validator/v10"
)

// ErrorResponse represents an error response
type ErrorResponse struct {
	Error   string                 `json:"error"`
	Code    int                    `json:"code"`
	Details map[string]interface{} `json:"details,omitempty"`
}

// SuccessResponse represents a success response
type SuccessResponse struct {
	Success bool                   `json:"success"`
	Message string                 `json:"message,omitempty"`
	Data    map[string]interface{} `json:"data,omitempty"`
}

// ValidationErrorResponse represents validation error response
type ValidationErrorResponse struct {
	Error  string            `json:"error"`
	Code   int               `json:"code"`
	Fields map[string]string `json:"fields"`
}

// JSON sends a JSON response
func JSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(data); err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// JSONWithStatus sends a JSON response with custom status code
func JSONWithStatus(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// Error sends an error response
func Error(w http.ResponseWriter, message string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	
	response := ErrorResponse{
		Error: message,
		Code:  code,
	}
	
	json.NewEncoder(w).Encode(response)
}

// ErrorWithDetails sends an error response with additional details
func ErrorWithDetails(w http.ResponseWriter, message string, code int, details map[string]interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	
	response := ErrorResponse{
		Error:   message,
		Code:    code,
		Details: details,
	}
	
	json.NewEncoder(w).Encode(response)
}

// Success sends a success response
func Success(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/json")
	
	response := SuccessResponse{
		Success: true,
		Message: message,
	}
	
	json.NewEncoder(w).Encode(response)
}

// SuccessWithData sends a success response with data
func SuccessWithData(w http.ResponseWriter, message string, data map[string]interface{}) {
	w.Header().Set("Content-Type", "application/json")
	
	response := SuccessResponse{
		Success: true,
		Message: message,
		Data:    data,
	}
	
	json.NewEncoder(w).Encode(response)
}

// ValidationError sends a validation error response
func ValidationError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	
	fields := make(map[string]string)
	
	if validationErrors, ok := err.(validator.ValidationErrors); ok {
		for _, ve := range validationErrors {
			field := strings.ToLower(ve.Field())
			switch ve.Tag() {
			case "required":
				fields[field] = "This field is required"
			case "email":
				fields[field] = "Must be a valid email address"
			case "min":
				fields[field] = "Minimum length is " + ve.Param()
			case "max":
				fields[field] = "Maximum length is " + ve.Param()
			case "alphanum":
				fields[field] = "Must contain only letters and numbers"
			case "eqfield":
				fields[field] = "Must match " + ve.Param()
			default:
				fields[field] = "Invalid value"
			}
		}
	}
	
	response := ValidationErrorResponse{
		Error:  "Validation failed",
		Code:   http.StatusBadRequest,
		Fields: fields,
	}
	
	json.NewEncoder(w).Encode(response)
}

// NotFound sends a 404 error response
func NotFound(w http.ResponseWriter, message string) {
	if message == "" {
		message = "Resource not found"
	}
	Error(w, message, http.StatusNotFound)
}

// Unauthorized sends a 401 error response
func Unauthorized(w http.ResponseWriter, message string) {
	if message == "" {
		message = "Unauthorized"
	}
	Error(w, message, http.StatusUnauthorized)
}

// Forbidden sends a 403 error response
func Forbidden(w http.ResponseWriter, message string) {
	if message == "" {
		message = "Forbidden"
	}
	Error(w, message, http.StatusForbidden)
}

// BadRequest sends a 400 error response
func BadRequest(w http.ResponseWriter, message string) {
	if message == "" {
		message = "Bad request"
	}
	Error(w, message, http.StatusBadRequest)
}

// InternalServerError sends a 500 error response
func InternalServerError(w http.ResponseWriter, message string) {
	if message == "" {
		message = "Internal server error"
	}
	Error(w, message, http.StatusInternalServerError)
}

// Conflict sends a 409 error response
func Conflict(w http.ResponseWriter, message string) {
	if message == "" {
		message = "Conflict"
	}
	Error(w, message, http.StatusConflict)
}

// TooManyRequests sends a 429 error response
func TooManyRequests(w http.ResponseWriter, message string) {
	if message == "" {
		message = "Too many requests"
	}
	Error(w, message, http.StatusTooManyRequests)
}

// Created sends a 201 response with data
func Created(w http.ResponseWriter, data interface{}) {
	JSONWithStatus(w, http.StatusCreated, data)
}

// NoContent sends a 204 response
func NoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}