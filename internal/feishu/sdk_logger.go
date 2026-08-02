package feishu

import (
	"context"
	"fmt"
	"io"
	"log"
	"regexp"
	"strings"
)

var (
	urlQueryPattern    = regexp.MustCompile(`(?i)(wss?|https?)://([^\s?]+)\?[^\s]+`)
	namedSecretPattern = regexp.MustCompile(`(?i)(app_secret|ticket|token|signature|authorization)(\s*[:=]\s*)(?:Bearer\s+)?[^\s,;]+`)
)

type sdkLogger struct {
	logger *log.Logger
}

func newSDKLogger(out io.Writer) *sdkLogger {
	return &sdkLogger{logger: log.New(out, "", log.Ldate|log.Lmicroseconds)}
}

func (l *sdkLogger) Debug(_ context.Context, args ...interface{}) { l.write("Debug", args...) }
func (l *sdkLogger) Info(_ context.Context, args ...interface{})  { l.write("Info", args...) }
func (l *sdkLogger) Warn(_ context.Context, args ...interface{})  { l.write("Warn", args...) }
func (l *sdkLogger) Error(_ context.Context, args ...interface{}) { l.write("Error", args...) }

func (l *sdkLogger) write(level string, args ...interface{}) {
	parts := make([]string, len(args))
	for i, arg := range args {
		parts[i] = fmt.Sprint(arg)
	}
	message := redactSDKLog(strings.Join(parts, " "))
	l.logger.Printf("[%s] %s", level, message)
}

func redactSDKLog(message string) string {
	message = urlQueryPattern.ReplaceAllString(message, `$1://$2?[REDACTED]`)
	return namedSecretPattern.ReplaceAllString(message, `$1$2[REDACTED]`)
}
