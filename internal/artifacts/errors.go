package artifacts

import (
	"errors"
	"fmt"
	"net/http"

	"go.pinesandbox.io/computer/internal/base/problem"
)

// NotFoundError means the Computer's artifacts have no file at Path, the caller's path
// (storage answered 404). Err is storage's *problem.APIError.
type NotFoundError struct {
	Path string
	Err  error
}

func (e *NotFoundError) Error() string { return fmt.Sprintf("pinesandbox: no artifact at %q", e.Path) }

func (e *NotFoundError) Unwrap() error { return e.Err }

// StorageFullError means the Computer stores more than its storage limit, so its storage
// token is read-only and nothing was written. Delete files to make room.
type StorageFullError struct {
	UsedBytes  int64
	LimitBytes int64
}

func (e *StorageFullError) Error() string {
	return fmt.Sprintf("pinesandbox: the Computer's artifacts are full (%d of %d bytes used)", e.UsedBytes, e.LimitBytes)
}

// notFound turns a storage 404 into a *NotFoundError for path; other errors pass through.
func notFound(err error, path string) error {
	var ae *problem.APIError
	if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
		return &NotFoundError{Path: path, Err: ae}
	}
	return err
}
