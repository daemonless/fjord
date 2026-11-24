package main

import "net/http"

// networksChanged wraps a networks handler: a request that changes a
// network (anything but GET) and succeeds tells every open page, so a
// stack page opened earlier offers a network made since.
func (s *server) networksChanged(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			h(w, r)
			return
		}
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		h(rec, r)
		if rec.code < 300 && s.events != nil {
			s.events.publish([]byte(`{"type":"networks"}`))
		}
	}
}

// statusRecorder keeps the status a handler wrote.
type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.code = code
	r.ResponseWriter.WriteHeader(code)
}

// Flush passes through, for a handler that streams.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
