package shared

import (
	"fmt"
	"log"
	"os"
	"time"
)

type LogLevel int

const (
	DEBUG LogLevel = iota
	INFO
	WARN
	ERROR
)

var logLevel = INFO

var logPrefix = map[LogLevel]string{
	DEBUG: "[DEBUG]",
	INFO:  "[INFO] ",
	WARN:  "[WARN] ",
	ERROR: "[ERROR]",
}

func SetLogLevel(level LogLevel) {
	logLevel = level
}

func Log(level LogLevel, format string, args ...interface{}) {
	if level < logLevel {
		return
	}
	msg := fmt.Sprintf(format, args...)
	log.Printf("%s %s %s", time.Now().Format("2006-01-02 15:04:05"), logPrefix[level], msg)
}

func Debugf(format string, args ...interface{}) { Log(DEBUG, format, args...) }
func Infof(format string, args ...interface{})  { Log(INFO, format, args...) }
func Warnf(format string, args ...interface{})  { Log(WARN, format, args...) }
func Errorf(format string, args ...interface{}) { Log(ERROR, format, args...) }

// FileLogger writes logs to both stdout and a file
type FileLogger struct {
	file   *os.File
	logger *log.Logger
}

func NewFileLogger(path string) (*FileLogger, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}
	return &FileLogger{
		file:   f,
		logger: log.New(f, "", log.LstdFlags),
	}, nil
}

func (fl *FileLogger) Write(p []byte) (n int, err error) {
	os.Stdout.Write(p)
	return fl.file.Write(p)
}

func (fl *FileLogger) Close() error {
	return fl.file.Close()
}
