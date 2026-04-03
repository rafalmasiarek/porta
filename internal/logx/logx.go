package logx

import (
    "fmt"
    "time"
)

func Info(component, format string, args ...any) {
    prefix := time.Now().UTC().Format(time.RFC3339) + " [" + component + "] "
    fmt.Printf(prefix+format+"\n", args...)
}
