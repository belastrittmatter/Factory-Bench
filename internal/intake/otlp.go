package intake

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
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

// LogsHandler returns the POST /v1/logs handler: OTLP/HTTP JSON logs, as Claude Code exports them.
// Each log record is stored as one raw event (source otlp, type otlp.<event.name>) whose payload is its attributes,
// resource attributes included, plus attempt_id. after runs once new records are stored.
func LogsHandler(db *sql.DB, after func()) http.Handler {
	var mu sync.Mutex
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		factory := authorize(db, w, r)
		if factory == "" {
			return
		}
		if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
			http.Error(w, "only OTLP/HTTP JSON (Content-Type application/json) is supported", http.StatusUnsupportedMediaType)
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
					attrs := map[string]any{}
					for _, kv := range rl.Resource.Attributes {
						attrs[kv.Key] = kv.Value.plain()
					}
					for _, kv := range lr.Attributes { // the record's own value wins
						attrs[kv.Key] = kv.Value.plain()
					}
					attempt, _ := attrs["factorybench.attempt"].(string)
					if attempt == "" {
						rejected++
						continue
					}
					name, _ := attrs["event.name"].(string)
					for k := range attrs {
						if dropped(k, name) {
							delete(attrs, k)
						}
					}
					attrs["attempt_id"] = attempt // where normalize and reports look for it, as on contract events
					ts := store.Now()
					if lr.TimeUnixNano > 0 {
						ts = store.FormatTS(time.Unix(0, int64(lr.TimeUnixNano)))
					}
					if s, ok := attrs["event.timestamp"].(string); ok {
						if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
							ts = store.FormatTS(t)
						}
					}
					payload, err := json.Marshal(attrs)
					if err != nil {
						internalError(w, err)
						return
					}
					session, _ := attrs["session.id"].(string)
					seq, ok := attrs["event.sequence"]
					id := fmt.Sprintf("%s:%v", session, seq)
					if session == "" || !ok {
						s := sha256.Sum256(payload)
						id = "nokey:" + hex.EncodeToString(s[:8])
					}
					dup, err := store.InsertEvent(db, "otlp", factory, id, "otlp."+name, ts, string(payload), nil)
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
