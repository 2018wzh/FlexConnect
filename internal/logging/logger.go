package logging

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
	LevelFatal
)

var levelNames = map[Level]string{
	LevelDebug: "DEBUG",
	LevelInfo:  "INFO",
	LevelWarn:  "WARN",
	LevelError: "ERROR",
	LevelFatal: "FATAL",
}

type Logger struct {
	component string
}

var (
	globalMu    sync.Mutex
	globalLevel Level = LevelInfo
	globalOut   io.Writer
)

// Init initializes the global logger backend. formatText is reserved for future formats.
func Init(writer io.Writer, level Level, formatText bool) {
	globalMu.Lock()
	defer globalMu.Unlock()
	if writer == nil {
		globalOut = os.Stderr
	} else {
		globalOut = writer
	}
	globalLevel = level
	_ = formatText
}

func ensureInitialized() {
	globalMu.Lock()
	defer globalMu.Unlock()
	if globalOut != nil {
		return
	}
	globalOut = os.Stderr
}

func SetLevel(level Level) {
	globalMu.Lock()
	defer globalMu.Unlock()
	globalLevel = level
}

func ParseLevel(value string) Level {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return LevelDebug
	case "warn", "warning":
		return LevelWarn
	case "error":
		return LevelError
	case "fatal":
		return LevelFatal
	case "info", "":
		return LevelInfo
	default:
		return LevelInfo
	}
}

func WithComponent(component string) *Logger {
	return &Logger{component: component}
}

func shouldLog(level Level) bool {
	ensureInitialized()
	globalMu.Lock()
	defer globalMu.Unlock()
	return level >= globalLevel
}

func (l *Logger) Log(level Level, msg string) {
	if !shouldLog(level) {
		return
	}
	if l == nil || l.component == "" {
		l = &Logger{component: "app"}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	levelName := levelNames[level]
	line := fmt.Sprintf(`time=%s level=%s component=%s msg=%s`, now, levelName, l.component, strconv.Quote(msg))
	globalMu.Lock()
	_, _ = globalOut.Write([]byte(line + "\n"))
	globalMu.Unlock()
}

func (l *Logger) Print(v ...any) {
	l.Info(v...)
}

func (l *Logger) Printf(format string, args ...any) {
	l.logf(LevelInfo, format, args...)
}

func (l *Logger) Println(v ...any) {
	l.Info(v...)
}

func (l *Logger) logf(level Level, format string, args ...any) {
	l.Log(level, fmt.Sprintf(format, args...))
}

func (l *Logger) Debug(v ...any) {
	l.Log(LevelDebug, fmt.Sprint(v...))
}

func (l *Logger) Debugf(format string, args ...any) {
	l.logf(LevelDebug, format, args...)
}

func (l *Logger) Info(v ...any) {
	l.Log(LevelInfo, fmt.Sprint(v...))
}

func (l *Logger) Infof(format string, args ...any) {
	l.logf(LevelInfo, format, args...)
}

func (l *Logger) Warn(v ...any) {
	l.Log(LevelWarn, fmt.Sprint(v...))
}

func (l *Logger) Warnf(format string, args ...any) {
	l.logf(LevelWarn, format, args...)
}

func (l *Logger) Error(v ...any) {
	l.Log(LevelError, fmt.Sprint(v...))
}

func (l *Logger) Errorf(format string, args ...any) {
	l.logf(LevelError, format, args...)
}

func (l *Logger) Fatal(v ...any) {
	l.Log(LevelFatal, fmt.Sprint(v...))
	os.Exit(1)
}

func (l *Logger) Fatalf(format string, args ...any) {
	l.logf(LevelFatal, format, args...)
	os.Exit(1)
}

func Debug(v ...any) {
	WithComponent("app").Debug(v...)
}

func Debugf(format string, args ...any) {
	WithComponent("app").Debugf(format, args...)
}

func Print(v ...any) {
	WithComponent("app").Print(v...)
}

func Printf(format string, args ...any) {
	WithComponent("app").Printf(format, args...)
}

func Println(v ...any) {
	WithComponent("app").Println(v...)
}

func Info(v ...any) {
	WithComponent("app").Info(v...)
}

func Infof(format string, args ...any) {
	WithComponent("app").Infof(format, args...)
}

func Warn(v ...any) {
	WithComponent("app").Warn(v...)
}

func Warnf(format string, args ...any) {
	WithComponent("app").Warnf(format, args...)
}

func Error(v ...any) {
	WithComponent("app").Error(v...)
}

func Errorf(format string, args ...any) {
	WithComponent("app").Errorf(format, args...)
}

func Fatal(v ...any) {
	WithComponent("app").Fatal(v...)
}

func Fatalf(format string, args ...any) {
	WithComponent("app").Fatalf(format, args...)
}
