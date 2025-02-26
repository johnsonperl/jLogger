package jLogger

import (
    "log"
    "os"
    "path/filepath"
    "github.com/natefinch/lumberjack"
    "time"
    "sync"
    "errors"
    "fmt"
    "strings"
)

type logMessage struct {
    level string
    timestamp time.Time   // 记录日志产生时间
    msg   []interface{}
}

const timeFormat = "2006-01-02 15:04:05.000"

// 使用channel缓冲区，避免日志写入阻塞主线程
// 使用buffer缓冲区，避免日志写入阻塞channel；同时区分出不同级别的日志，分别写入不同的缓冲区，目的是使文件写入更加有序，不用在不同文件之间频繁跳转，减少磁盘IO
// 使用定时器，定时刷新缓冲区
// 使用sync.Mutex，保证并发安全，避免多个goroutine同时写入缓冲区，也避免在刷新缓冲区时，有其他goroutine写入缓冲区
type Logger struct {
    InfoLogger  *log.Logger
    DebugLogger *log.Logger
    ErrorLogger *log.Logger
    logChannel  chan logMessage
    bufferInfo []logMessage // Info缓冲区
    bufferDebug []logMessage // Debug缓冲区
    bufferError []logMessage // Error缓冲区
    bufferSize int
    flushInterval time.Duration
    info_mu sync.Mutex
    debug_mu sync.Mutex
    error_mu sync.Mutex
    once      sync.Once // 保证Close方法只执行一次
    wg        sync.WaitGroup // 保证所有日志写入完成后再关闭
    closed    bool // 保证Close方法只执行一次
    log_level string // 日志级别
}

func NewLogger(logDir, logPrefix string, bufferSize int, flushInterval time.Duration, log_level string) (*Logger, error) {
    if err := os.MkdirAll(logDir, 0755); err != nil {
        log.Fatalf("创建或访问日志目录失败: %v", err)
    }

    if bufferSize <= 0 {
        return nil, errors.New("bufferSize必须大于0")
    }

    infoLogPath := filepath.Join(logDir, logPrefix+"_info.log")
    debugLogPath := filepath.Join(logDir, logPrefix+"_debug.log")
    errorLogPath := filepath.Join(logDir, logPrefix+"_error.log")


    infoLogger := log.New(&lumberjack.Logger{
        Filename:   infoLogPath,
        MaxSize:    50, // megabytes
        MaxBackups: 365, // 日志文件最多保存备份的个数
        MaxAge:     1, // days 历史日志保留天数
        Compress:   true,
        LocalTime:  true,
    }, "INFO: ", 0)

    debugLogger := log.New(&lumberjack.Logger{
        Filename:   debugLogPath,
        MaxSize:    50, // megabytes
        MaxBackups: 365, // 日志文件最多保存备份的个数
        MaxAge:     10, // days 历史日志保留天数
        Compress:   true,
        LocalTime:  true,
    }, "DEBUG: ", 0)

    errorLogger := log.New(&lumberjack.Logger{
        Filename:   errorLogPath,
        MaxSize:    50, // megabytes
        MaxBackups: 365, // 日志文件最多保存备份的个数
        MaxAge:     30, // days 历史日志保留天数
        Compress:   true,
        LocalTime:  true,
    }, "ERROR: ", 0)

    logger := &Logger{
        InfoLogger:  infoLogger,
        DebugLogger: debugLogger,
        ErrorLogger: errorLogger,
        logChannel:  make(chan logMessage, 5000), // 缓冲通道，容量为5000
        bufferInfo:  make([]logMessage, 0, bufferSize),
        bufferDebug: make([]logMessage, 0, bufferSize),
        bufferError: make([]logMessage, 0, bufferSize),
        bufferSize:  bufferSize,
        flushInterval: flushInterval,
        log_level: log_level,
    }

    go logger.processLogMessages()
    logger.wg.Add(1) // 保证processLogMessages执行完毕后再关闭

    go logger.flushBufferPeriodically()

    return logger, nil
}

func (l *Logger) processLogMessages() {
    defer l.wg.Done()

    for msg := range l.logChannel {
        var needFlushInfo, needFlushDebug, needFlushError bool
        if msg.level == "INFO" {
            l.info_mu.Lock()
            l.bufferInfo = append(l.bufferInfo, msg)
            // 判断是否需要刷新缓冲区，放在锁内，避免在所外判断，buffer大小已经发生变化
            needFlushInfo = len(l.bufferInfo) >= l.bufferSize
            l.info_mu.Unlock()
        } else if msg.level == "DEBUG" {
            l.debug_mu.Lock()
            l.bufferDebug = append(l.bufferDebug, msg)
            // 判断是否需要刷新缓冲区，放在锁内，避免在所外判断，buffer大小已经发生变化
            needFlushDebug = len(l.bufferDebug) >= l.bufferSize
            l.debug_mu.Unlock()
        } else if msg.level == "ERROR" {
            l.error_mu.Lock()
            l.bufferError = append(l.bufferError, msg)
            // 判断是否需要刷新缓冲区，放在锁内，避免在所外判断，buffer大小已经发生变化
            needFlushError = len(l.bufferError) >= l.bufferSize
            l.error_mu.Unlock()
        }

        // log.Println("写入缓冲区:", msg.level, msg.msg)

        if needFlushInfo{
            // log.Println("Info缓冲区已满，刷新缓冲区")
            l.flushInfoBuffer()
        }

        if needFlushDebug {
            // log.Println("Debug缓冲区已满，刷新缓冲区")
            l.flushDebugBuffer()
        }

        if needFlushError {
            // log.Println("Error缓冲区已满，刷新缓冲区")
            l.flushErrorBuffer()
        }
    }
}

// 统一flush方法
func (l *Logger) flushBuffer(buffer *[]logMessage, mu *sync.Mutex, logger *log.Logger) {
    mu.Lock()
    // 复制数据后立即释放锁
    tmp := make([]logMessage, len(*buffer))
    copy(tmp, *buffer)
    *buffer = (*buffer)[:0]
    mu.Unlock()
    
    // 写入文件（无需持有锁）
    for _, msg := range tmp {
        logger.Println(msg.timestamp.Format(timeFormat), strings.TrimSpace(fmt.Sprintln(msg.msg...)))
    }
}

func (l *Logger) flushInfoBuffer() {
    l.flushBuffer(&l.bufferInfo, &l.info_mu, l.InfoLogger)
}

func (l *Logger) flushDebugBuffer() {
    l.flushBuffer(&l.bufferDebug, &l.debug_mu, l.DebugLogger)
}

func (l *Logger) flushErrorBuffer() {
    l.flushBuffer(&l.bufferError, &l.error_mu, l.ErrorLogger)
}

func (l *Logger) flushBufferPeriodically() {
    ticker := time.NewTicker(l.flushInterval)
    defer ticker.Stop()
    for range ticker.C {
        // log.Println("定时刷新缓冲区")
        l.flushInfoBuffer()
        l.flushDebugBuffer()
        l.flushErrorBuffer()
    }
}

// 老版本的写法，不使用buffer缓冲区
// func (l *Logger) processLogMessages() {
//     for msg := range l.logChannel {
//         switch msg.level {
//         case "INFO":
//             l.InfoLogger.Println(msg.msg...)
//         case "DEBUG":
//             l.DebugLogger.Println(msg.msg...)
//         case "ERROR":
//             l.ErrorLogger.Println(msg.msg...)
//         }
//     }
// }

// 自动根据日志等级，记录日志：DEBUG时，Info、Debug、Error方法都能写入日志；INFO只有Info和Error方法可以写入日志，ERROR时，只有Error方法可以写入日志
// 通过config中的LOG_LEVEL设置日志级别
func (l *Logger) Info(v ...interface{}) {
    if l.log_level == "INFO" || l.log_level == "DEBUG" {
        // 立即捕获当前时间
        eventTime := time.Now()

        select {
        case l.logChannel <- logMessage{level: "INFO", msg: v, timestamp: eventTime}:
        default:
            // 通道已满，丢弃日志或处理备用方案
            l.InfoLogger.Println("日志通道已满，进入主线程写入日志:", v)
        }
    }
}

func (l *Logger) Debug(v ...interface{}) {
    if l.log_level == "DEBUG" {
        // 立即捕获当前时间
        eventTime := time.Now()

        select {
        case l.logChannel <- logMessage{level: "DEBUG", msg: v, timestamp: eventTime}:
        default:
            // 通道已满，丢弃日志或处理备用方案
            l.DebugLogger.Println("日志通道已满，进入主线程写入日志", v)
        }
    }
}

func (l *Logger) Error(v ...interface{}) {
    // 立即捕获当前时间
    eventTime := time.Now()

    select {
    case l.logChannel <- logMessage{level: "ERROR", msg: v, timestamp: eventTime}:
    default:
        // 通道已满，丢弃日志或处理备用方案
        l.ErrorLogger.Println("日志通道已满，进入主线程写入日志", v)
    }
}

// 添加 Close 方法
func (l *Logger) Close() {
    l.once.Do(func() {
        close(l.logChannel)
        l.wg.Wait()      // 等待消息处理完成
        // 最终刷新所有缓冲区
        l.flushInfoBuffer()
        l.flushDebugBuffer()
        l.flushErrorBuffer()
    })
}


// // 调试
// log_normal,_ := NewLogger(config.LOG_DIR, config.LOG_PREFIX, 20, 10 * time.Second)
// log_normal.Debug("main.go", "DEBUG模式")
// log_normal.Info("main.go", "RELEASE模式")
// log_normal.Error("main.go", "panic 发生", r)
// log_normal.Info("main.go", "程序结束")
// log_normal.Close()