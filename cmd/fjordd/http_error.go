package main

import (
	"errors"
	"io/fs"
	"net/http"
)

// httpError is an error that says which status answers it, so a change made
// inside manager.Update can refuse with a 400 while a failed write stays 500.
type httpError struct {
	code int
	msg  string
}

func (e httpError) Error() string { return e.msg }

// replyErr answers err: an httpError with its own status, a stack that is not
// there with 404, anything else with 500.
func replyErr(w http.ResponseWriter, err error) {
	var he httpError
	switch {
	case errors.As(err, &he):
		http.Error(w, he.msg, he.code)
	case errors.Is(err, fs.ErrNotExist):
		http.Error(w, "Stack not found", 404)
	default:
		http.Error(w, err.Error(), 500)
	}
}
