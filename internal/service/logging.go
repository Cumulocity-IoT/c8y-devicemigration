package service

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strings"
)

// The platform can change a microservice's log level while it runs, through
// /service/{name}/loggers - the Spring Boot actuator endpoints, which is what
// `c8y microservices loggers` talks to. Implementing that shape means the same
// tooling works here, and it means turning on debug output during a migration
// does not mean redeploying and losing the run.

// LogLevel is the level every log line is filtered against. A LevelVar so it can
// be changed while the service is running.
var LogLevel = new(slog.LevelVar)

// defaultLevel is what a reset returns to.
const defaultLevel = slog.LevelInfo

// SetupLogging sends structured logs to stdout, which is the only place the
// platform collects them from.
func SetupLogging() {
	LogLevel.Set(defaultLevel)
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: LogLevel,
	})))
}

// rootLogger is the only logger there is to configure.
//
// Go's slog has no logger hierarchy to expose, so this reports one logger named
// the way the actuator names its root. Anything else is a 404 rather than a
// pretence that it was configured.
const rootLogger = "ROOT"

// levels are the levels the actuator vocabulary uses, mapped to slog's.
var levels = map[string]slog.Level{
	"ERROR": slog.LevelError,
	"WARN":  slog.LevelWarn,
	"INFO":  slog.LevelInfo,
	"DEBUG": slog.LevelDebug,
	"TRACE": slog.LevelDebug, // slog has nothing finer, and saying so beats refusing
}

// levelName is the actuator name of a slog level.
func levelName(level slog.Level) string {
	switch {
	case level >= slog.LevelError:
		return "ERROR"
	case level >= slog.LevelWarn:
		return "WARN"
	case level >= slog.LevelInfo:
		return "INFO"
	default:
		return "DEBUG"
	}
}

// loggerConfiguration is one logger, in the actuator's shape.
type loggerConfiguration struct {
	ConfiguredLevel string `json:"configuredLevel"`
	EffectiveLevel  string `json:"effectiveLevel"`
}

// LoggersResponse is the actuator's /loggers document.
type LoggersResponse struct {
	Levels  []string                       `json:"levels"`
	Loggers map[string]loggerConfiguration `json:"loggers"`
	Groups  map[string]any                 `json:"groups"`
}

func (s *Service) handleListLoggers(w http.ResponseWriter, r *http.Request) {
	if _, err := s.Authorize(r.Context(), r); err != nil {
		writeError(w, r, err)
		return
	}

	current := levelName(LogLevel.Level())
	writeJSON(w, r, http.StatusOK, LoggersResponse{
		Levels: []string{"ERROR", "WARN", "INFO", "DEBUG"},
		Loggers: map[string]loggerConfiguration{
			rootLogger: {ConfiguredLevel: current, EffectiveLevel: current},
		},
		Groups: map[string]any{},
	})
}

func (s *Service) handleGetLogger(w http.ResponseWriter, r *http.Request) {
	if _, err := s.Authorize(r.Context(), r); err != nil {
		writeError(w, r, err)
		return
	}

	name := r.PathValue("name")
	if !strings.EqualFold(name, rootLogger) {
		writeError(w, r, &NotFoundError{What: "logger " + name})
		return
	}

	current := levelName(LogLevel.Level())
	writeJSON(w, r, http.StatusOK, loggerConfiguration{
		ConfiguredLevel: current, EffectiveLevel: current,
	})
}

// handleSetLogger changes the level while the service runs.
//
// A null configuredLevel resets, which is how the actuator - and therefore
// `c8y microservices loggers delete` - says "back to the default".
func (s *Service) handleSetLogger(w http.ResponseWriter, r *http.Request) {
	if _, err := s.Authorize(r.Context(), r); err != nil {
		writeError(w, r, err)
		return
	}

	name := r.PathValue("name")
	if !strings.EqualFold(name, rootLogger) {
		writeError(w, r, &NotFoundError{What: "logger " + name})
		return
	}

	// Decoded loosely on purpose: the field is nullable, and a missing one means
	// the same as a null one.
	var body struct {
		ConfiguredLevel *string `json:"configuredLevel"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, &BadRequestError{
			Reason: "could not read the request body: " + err.Error()})
		return
	}

	if body.ConfiguredLevel == nil || *body.ConfiguredLevel == "" {
		LogLevel.Set(defaultLevel)
		slog.Info("log level reset", "level", levelName(defaultLevel))
		w.WriteHeader(http.StatusNoContent)
		return
	}

	level, ok := levels[strings.ToUpper(*body.ConfiguredLevel)]
	if !ok {
		writeError(w, r, &BadRequestError{Reason: "unknown level " +
			*body.ConfiguredLevel + ", expected one of ERROR, WARN, INFO, DEBUG"})
		return
	}

	LogLevel.Set(level)
	// Logged at the level that is always visible, or turning logging down would
	// be the last thing anybody saw about it.
	slog.Error("log level changed", "level", levelName(level))
	w.WriteHeader(http.StatusNoContent)
}
