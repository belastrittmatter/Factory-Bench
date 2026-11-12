package intake

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"factorybench/internal/store"
)

// otlpLogs is an OTLP/HTTP JSON logs request, reduced to the fields FactoryBench reads.
type otlpLogs struct {
	ResourceLogs []struct {
		Resource struct {
			Attributes []otlpKV `json:"attributes"`
		} `json:"resource"`
		ScopeLogs []struct {
			LogRecords []struct {
				TimeUnixNano flexInt  `json:"timeUnixNano"`
				Attributes   []otlpKV `json:"attributes"`
			} `json:"logRecords"`
		} `json:"scopeLogs"`
	} `json:"resourceLogs"`
}

type otlpKV struct {
	Key   string    `json:"key"`
	Value otlpValue `json:"value"`
}

type otlpValue struct {
	StringValue *string  `json:"stringValue"`
	IntValue    *flexInt `json:"intValue"`
	DoubleValue *float64 `json:"doubleValue"`
	BoolValue   *bool    `json:"boolValue"`
	ArrayValue  *struct {
		Values []otlpValue `json:"values"`
	} `json:"arrayValue"`
}

// flexInt is an OTLP int64: the protobuf JSON mapping sends a string, Claude Code sends a number.
type flexInt int64

func (n *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		return nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	*n = flexInt(v)
	return err
}

func (v otlpValue) plain() any {
	switch {
	case v.StringValue != nil:
		return *v.StringValue
	case v.IntValue != nil:
		return int64(*v.IntValue)
	case v.DoubleValue != nil:
		return *v.DoubleValue
	case v.BoolValue != nil:
		return *v.BoolValue
	case v.ArrayValue != nil:
		a := []any{}
		for _, x := range v.ArrayValue.Values {
			a = append(a, x.plain())
		}
		return a
	}
	return nil
}

// dropAttrs are personal data or content; they never reach the events table.
var dropAttrs = map[string]bool{
	"user.email": true, "user.account_uuid": true, "user.account_id": true, "user.id": true, "organization.id": true,
	"prompt": true, "prompt_text": true, "response": true, "tool_parameters": true, "tool_input": true,
	"anthropic.custom_base_url": true,
}

// dropped reports whether attribute key of event name must not be stored. Raw API bodies (OTEL_LOG_RAW_API_BODIES)
// are any key with "body"; error is free text, except on api_* events where it is the API's error and the only cause.
func dropped(key, name string) bool {
	return dropAttrs[key] || strings.Contains(key, "body") || key == "error" && !strings.HasPrefix(name, "api_")
}

// record turns one log record and its resource's attributes into an event; ok is false without factorybench.attempt.
// The payload is the attributes, resource ones included, minus dropped ones, plus attempt_id.
func record(resource, attributes []otlpKV, nano flexInt) (id, typ, ts, payload string, ok bool) {
	attrs := map[string]any{}
	for _, kv := range slices.Concat(resource, attributes) { // the record's own value wins
		attrs[kv.Key] = kv.Value.plain()
	}
	attempt, _ := attrs["factorybench.attempt"].(string)
	if attempt == "" {
		return "", "", "", "", false
	}
	name, _ := attrs["event.name"].(string)
	for k := range attrs {
		if dropped(k, name) {
			delete(attrs, k)
		}
	}
	attrs["attempt_id"] = attempt // where normalize and reports look for it, as on contract events
	ts = store.Now()
	if nano > 0 {
		ts = store.FormatTS(time.Unix(0, int64(nano)))
	}
	stamp, _ := attrs["event.timestamp"].(string)
	if t, err := time.Parse(time.RFC3339Nano, stamp); err == nil {
		ts = store.FormatTS(t)
	}
	b, _ := json.Marshal(attrs) // decoded from JSON, so it always encodes
	session, _ := attrs["session.id"].(string)
	seq, hasSeq := attrs["event.sequence"]
	// a resumed session restarts event.sequence in the new process; the timestamp keeps those apart
	id = fmt.Sprintf("%s:%v:%s", session, seq, stamp)
	if session == "" || !hasSeq {
		s := sha256.Sum256(b)
		id = "nokey:" + hex.EncodeToString(s[:8])
	}
	return id, "otlp." + name, ts, string(b), true
}

// LogsHandler returns the POST /v1/logs handler: OTLP/HTTP JSON logs, as Claude Code exports them.
// Each log record is stored as one raw event (source otlp, type otlp.<event.name>, see record). after runs once new records are stored.
func LogsHandler(db *sql.DB, after func()) http.Handler {
	var mu sync.Mutex
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		factory := authorize(db, w, r)
		if factory == "" {
			return
		}
		mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if ce := r.Header.Get("Content-Encoding"); mt != "application/json" || ce != "" && ce != "identity" {
			http.Error(w, "only uncompressed OTLP/HTTP JSON is supported: set OTEL_EXPORTER_OTLP_PROTOCOL=http/json "+
				"and leave OTEL_EXPORTER_OTLP_COMPRESSION unset", http.StatusUnsupportedMediaType)
			return
		}
		var req otlpLogs
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&req); err != nil {
			http.Error(w, "body must be an OTLP/HTTP JSON logs request", http.StatusBadRequest)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		added, rejected := 0, 0
		for _, rl := range req.ResourceLogs {
			for _, sl := range rl.ScopeLogs {
				for _, lr := range sl.LogRecords {
					id, typ, ts, payload, ok := record(rl.Resource.Attributes, lr.Attributes, lr.TimeUnixNano)
					if !ok {
						rejected++
						continue
					}
					dup, err := store.InsertEvent(db, "otlp", factory, id, typ, ts, payload, nil)
					if err != nil {
						internalError(w, err)
						return
					}
					if !dup {
						added++
					}
				}
			}
		}
		if after != nil && added > 0 {
			after()
		}
		resp := map[string]any{}
		if rejected > 0 {
			resp["partialSuccess"] = map[string]any{"rejectedLogRecords": rejected,
				"errorMessage": "log records without factorybench.attempt are not stored"}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
}
