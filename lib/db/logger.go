package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var _ logger.Interface = (*Logger)(nil)
var _ gorm.ParamsFilter = (*Logger)(nil)

// Logger is a GORM logger implementation that uses zerolog.
type Logger struct {
	opt *options
}

// options holds the configuration for the Logger.
type options struct {
	logParams         bool
	skipFrames        int
	defaultLogLevel   zerolog.Level
	fieldName         string
	ignoreNotFoundErr bool
	slowThreshold     time.Duration
	logAll            bool
}

// WithLogAll returns an OptionFn that configures the Logger to log all SQL
// queries, including those that are not slow or have no errors.
func WithLogAll() OptionFn {
	return func(o *options) {
		o.logAll = true
	}
}

// WithIgnoreNotFoundError returns an OptionFn that configures the Logger to
// ignore GORM's ErrRecordNotFound error.
func WithIgnoreNotFoundError() OptionFn {
	return func(o *options) {
		o.ignoreNotFoundErr = true
	}
}

// WithLogParams returns an OptionFn that configures the Logger to log SQL
// parameters.
func WithLogParams() OptionFn {
	return func(o *options) {
		o.logParams = true
	}
}

// WithSkipFrames returns an OptionFn that configures the Logger to skip the
// specified number of caller frames when logging.
func WithSkipFrames(skip int) OptionFn {
	return func(o *options) {
		o.skipFrames = skip
	}
}

// WithSqlFieldName returns an OptionFn that configures the Logger to use the
// specified field name for logging SQL queries.
func WithSqlFieldName(name string) OptionFn {
	return func(o *options) {
		o.fieldName = name
	}
}

// WithDefaultLogLevel returns an OptionFn that configures the Logger to use
// the specified zerolog.Level as the default log level.
func WithDefaultLogLevel(level zerolog.Level) OptionFn {
	return func(o *options) {
		o.defaultLogLevel = level
	}
}

// WithSlowThreshold returns an OptionFn that configures the Logger to log slow
// queries with a warning when their execution time exceeds the specified
// threshold.
func WithSlowThreshold(threshold time.Duration) OptionFn {
	return func(o *options) {
		o.slowThreshold = threshold
	}
}

// OptionFn is a function type used to configure the Logger.
type OptionFn func(*options)

// NewLogger creates a new Logger with the provided options.
//
// Available options:
//   - WithIgnoreNotFoundError(): Ignores GORM's ErrRecordNotFound error.
//   - WithLogParams(): Logs SQL parameters.
//   - WithSkipFrames(skip int): Skips the specified number of caller frames.
//   - WithSqlFieldName(name string): Sets the field name for SQL queries (default: "sql").
//   - WithDefaultLogLevel(level zerolog.Level): Sets the default log level (default: zerolog.DebugLevel).
//   - WithSlowThreshold(threshold time.Duration): Sets the slow query threshold (default: 500ms).
func NewLogger(
	opts ...OptionFn,
) Logger {
	opt := &options{
		defaultLogLevel: zerolog.DebugLevel,
		fieldName:       "sql",
		slowThreshold:   500 * time.Millisecond,
	}

	for _, fn := range opts {
		fn(opt)
	}

	return Logger{
		opt: opt,
	}
}

// LogMode sets the log mode for the logger.
func (l Logger) LogMode(logger.LogLevel) logger.Interface {
	return l
}

// Error logs an error message.
func (l Logger) Error(ctx context.Context, msg string, opts ...interface{}) {
	l.apply(zerolog.Ctx(ctx).Error()).Msg(fmt.Sprintf(msg, opts...))
}

// Warn logs a warning message.
func (l Logger) Warn(ctx context.Context, msg string, opts ...interface{}) {
	l.apply(zerolog.Ctx(ctx).Warn()).Msg(fmt.Sprintf(msg, opts...))
}

// Info logs an informational message.
func (l Logger) Info(ctx context.Context, msg string, opts ...interface{}) {
	l.apply(zerolog.Ctx(ctx).Info()).Msg(fmt.Sprintf(msg, opts...))
}

// apply applies common logging options to the zerolog.Event.
func (l Logger) apply(event *zerolog.Event) *zerolog.Event {
	if l.opt.skipFrames > 0 {
		event.CallerSkipFrame(l.opt.skipFrames)
	}

	return event
}

// Trace logs a GORM trace event.
// It logs the SQL query, elapsed time, rows affected, and any errors.
// If the query execution time exceeds the slow threshold, it logs a warning.
// If err is gorm.ErrRecordNotFound and WithIgnoreNotFoundError is enabled,
// the trace is skipped.
func (l Logger) Trace(ctx context.Context, begin time.Time, f func() (string, int64), err error) {
	logLevel := l.opt.defaultLogLevel

	shouldLog := l.opt.logAll

	if l.opt.slowThreshold > 0 && time.Since(begin) > l.opt.slowThreshold {
		logLevel = zerolog.WarnLevel
		shouldLog = true
	}

	event := zerolog.Ctx(ctx).WithLevel(logLevel)

	if err != nil {
		if l.opt.ignoreNotFoundErr && errors.Is(err, gorm.ErrRecordNotFound) {
			return
		}

		event = zerolog.Ctx(ctx).Error().Err(err)

		shouldLog = true
	}

	if !shouldLog {
		return
	}

	if l.opt.skipFrames > 0 {
		event = event.CallerSkipFrame(l.opt.skipFrames)
	}

	event.Int64("elapsed_ms", time.Since(begin).Milliseconds())

	sql, rows := f()

	if sql != "" {
		event.Str(l.opt.fieldName, sql)
	}

	if rows > -1 {
		event.Int64("rows_affected", rows)
	}

	event.Send()
}

// ParamsFilter filters SQL parameters. If WithLogParams is enabled, it returns
// the original SQL query and parameters. Otherwise, it returns the SQL query
// and nil parameters, effectively redacting them from the logs.
func (l Logger) ParamsFilter(_ context.Context, sql string, params ...interface{}) (string, []interface{}) {
	if !l.opt.logParams {
		return sql, nil
	}

	return sql, params
}
