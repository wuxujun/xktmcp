package logger

import (
	"fmt"
	"io"
	"log"
)

// Init 初始化全局日志输出
func Init(w io.Writer) {
	log.SetOutput(w)
	// 增加文件行号，方便定位日志来源
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
}

// Infof 普通信息日志
func Infof(format string, v ...any) {
	_ = log.Output(2, fmt.Sprintf("[INFO] "+format, v...))
}

// Errorf 错误日志
func Errorf(format string, v ...any) {
	_ = log.Output(2, fmt.Sprintf("[ERROR] "+format, v...))
}

// Toolf 工具调用相关日志
func Toolf(toolName string, format string, v ...any) {
	args := append([]any{toolName}, v...)
	_ = log.Output(2, fmt.Sprintf("[TOOL:%s] "+format, args...))
}

// APIf 外部 API 调用相关日志
func APIf(apiName string, format string, v ...any) {
	args := append([]any{apiName}, v...)
	_ = log.Output(2, fmt.Sprintf("[API:%s] "+format, args...))
}
