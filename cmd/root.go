package cmd

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

var (
	port             int
	shell            string
	autoConfirm      string
	target           string // 代理目标节点URL
	token            string // 认证token
	serverToken      string // 实际使用的token（指定或随机生成）
	maxUploadSize    int64  // 最大上传文件大小（字节），默认500MB
	proxyTimeout     int    // 代理超时时间（秒），默认30秒
	blockCommands    string // 要拦截的危险命令列表，逗号分隔，为空则不拦截
	enableBlockCheck bool   // 是否启用危险命令检查
	historyMutex     sync.RWMutex
	historyFile      string // 历史记录文件（启动时生成，带时间戳）
)

var rootCmd = &cobra.Command{
	Use:   "ops-tty-agent",
	Short: "命令行代理服务 - go-tty升级版",
	Run: func(cmd *cobra.Command, args []string) {
		startServer()
	},
}

func init() {
	rootCmd.Flags().IntVarP(&port, "port", "p", 8080, "服务端口")
	rootCmd.Flags().StringVarP(&shell, "shell", "s", "bash", "使用的shell类型")
	rootCmd.Flags().StringVarP(&autoConfirm, "auto-confirm", "a", "no", "是否自动确认执行命令 (yes/no)")
	rootCmd.Flags().StringVarP(&target, "target", "t", "", "代理目标节点URL (如 http://b-node:8080)")
	rootCmd.Flags().StringVarP(&token, "token", "k", "", "认证token (不指定则随机生成)")
	rootCmd.Flags().Int64VarP(&maxUploadSize, "max-upload-size", "m", 500*1024*1024, "最大上传文件大小（字节），默认500MB")
	rootCmd.Flags().IntVarP(&proxyTimeout, "proxy-timeout", "", 30, "代理超时时间（秒），默认30秒")
	rootCmd.Flags().BoolVarP(&enableBlockCheck, "enable-block-check", "", false, "启用危险命令检查")
	rootCmd.Flags().StringVarP(&blockCommands, "block-commands", "b", "", "要拦截的危险命令列表（逗号分隔），需配合 --enable-block-check 使用")
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

type CommandRequest struct {
	Cmd            string `json:"cmd"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"` // 0 = 默认 60s
}

// CommandResponse 是命令执行的结构化返回，把信息给足，让 LLM 自己判断。
type CommandResponse struct {
	Stdout     string `json:"stdout,omitempty"`      // 标准输出
	Stderr     string `json:"stderr,omitempty"`      // 标准错误
	ExitCode   int    `json:"exit_code"`             // 进程退出码（0=成功）
	Error      string `json:"error,omitempty"`       // 执行层面的错误（如超时、shell 不存在）
	DurationMs int64  `json:"duration_ms"`           // 执行耗时（毫秒）
	Truncated  bool   `json:"truncated,omitempty"`   // stdout 是否因超限被截断
}

// TransferRequest 是跨节点文件传输请求
type TransferRequest struct {
	SourceURL   string `json:"source_url"`            // 源文件下载地址，如 http://B:8080/download/file.txt
	SourceToken string `json:"source_token,omitempty"` // 源节点 Token（不填则用当前服务 Token）
	TargetURL   string `json:"target_url"`            // 目标上传地址，如 http://C:8080/upload/file.txt
	TargetToken string `json:"target_token,omitempty"` // 目标节点 Token（不填则用当前服务 Token）
}

// TransferResponse 是跨节点文件传输响应
type TransferResponse struct {
	Success   bool   `json:"success"`
	Bytes     int64  `json:"bytes,omitempty"`
	DurationMs int64 `json:"duration_ms,omitempty"`
	Error     string `json:"error,omitempty"`
}

// TailResponse 是 /tail 接口的结构化返回
type TailResponse struct {
	Lines              []string `json:"lines"`
	TotalLinesReturned int      `json:"total_lines_returned"`
	FileSize           int64    `json:"file_size"`
	Truncated          bool     `json:"truncated,omitempty"`
	Error              string   `json:"error,omitempty"`
}

// StatResponse 是 /stat 接口的结构化返回
type StatResponse struct {
	Path      string    `json:"path"`
	Type      string    `json:"type"`       // "file" | "dir" | "symlink"
	SizeBytes int64     `json:"size_bytes"`
	SizeHuman string    `json:"size_human"`
	Mtime     time.Time `json:"mtime"`
	Mode      string    `json:"mode"`
	Owner     string    `json:"owner,omitempty"`
	Group     string    `json:"group,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// DiskEntry 是单个挂载点的磁盘信息
type DiskEntry struct {
	Filesystem string `json:"filesystem"`
	Mountpoint string `json:"mountpoint"`
	SizeBytes  int64  `json:"size_bytes"`
	SizeHuman  string `json:"size_human"`
	UsedBytes  int64  `json:"used_bytes"`
	UsedHuman  string `json:"used_human"`
	FreeBytes  int64  `json:"free_bytes"`
	FreeHuman  string `json:"free_human"`
	UsedPercent string `json:"used_percent"`
}

// DiskResponse 是 /disk 接口的结构化返回
type DiskResponse struct {
	Filesystems []DiskEntry `json:"filesystems"`
	Error       string      `json:"error,omitempty"`
}

// ExecutionHistory 执行历史记录
type ExecutionHistory struct {
	Records []ExecutionRecord `json:"records"`
	NextID  int               `json:"next_id"`
}

// ExecutionRecord 单条执行记录
type ExecutionRecord struct {
	ID        int       `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Cmd       string    `json:"cmd"`
	Output    string    `json:"output"`
	Error     string    `json:"error,omitempty"`
	Success   bool      `json:"success"`
	Duration  int64     `json:"duration_ms"`
}

// 命令执行输出上限，防止恶意/意外命令耗尽内存
const (
	maxCommandStdout = 16 * 1024 * 1024 // 16MB
	maxCommandStderr = 4 * 1024         // 4KB（只保留尾部用于诊断）
)

// cappedWriter 是带上限的 io.Writer，超过上限后静默丢弃。
// 防止命令如 `yes` 或 `cat /dev/zero` 耗尽内存。
type cappedWriter struct {
	w   io.Writer
	max int
	n   int
}

func (c *cappedWriter) Write(p []byte) (int, error) {
	if c.n >= c.max {
		return len(p), nil
	}
	remaining := c.max - c.n
	if len(p) <= remaining {
		written, err := c.w.Write(p)
		c.n += written
		return written, err
	}
	written, err := c.w.Write(p[:remaining])
	c.n += written
	if err != nil {
		return written, err
	}
	return len(p), nil
}

// blockedPatterns 存储要拦截的命令模式
var blockedPatterns []string

// parseBlockedCommands 解析拦截命令列表
func parseBlockedCommands() {
	if blockCommands == "" {
		blockedPatterns = []string{}
		return
	}
	blockedPatterns = strings.Split(blockCommands, ",")
	for i := range blockedPatterns {
		blockedPatterns[i] = strings.TrimSpace(blockedPatterns[i])
	}
}

// isBlockedCommand 检查命令是否在拦截列表中
func isBlockedCommand(cmd string) bool {
	if !enableBlockCheck || len(blockedPatterns) == 0 {
		return false
	}
	for _, pattern := range blockedPatterns {
		if strings.Contains(cmd, pattern) {
			return true
		}
	}
	return false
}

// getProxyClient 获取带超时的HTTP客户端
func getProxyClient(timeout int) *http.Client {
	return &http.Client{
		Timeout: time.Duration(timeout) * time.Second,
	}
}

func startServer() {
	// 初始化token
	if token != "" {
		serverToken = token
	} else {
		serverToken = generateToken()
	}
	log.Printf("认证Token: %s", serverToken)
	log.Printf("请求时需在Header中添加: X-Token: %s", serverToken)

	// 生成历史记录文件名（带时间戳）
	startTime := time.Now()
	historyFile = fmt.Sprintf("ops-history_%s.json", startTime.Format("2006-01-02_15-04-05"))
	log.Printf("历史记录文件: %s", historyFile)

	// 解析拦截命令列表
	parseBlockedCommands()
	if enableBlockCheck && len(blockedPatterns) > 0 {
		log.Printf("危险命令检查已启用，拦截以下命令: %v", blockedPatterns)
	}

	// 初始化历史记录文件
	initHistoryFile()

	mux := http.NewServeMux()
	mux.HandleFunc("/cmd", handleCommand)
	mux.HandleFunc("/tail", handleTail)
	mux.HandleFunc("/stat", handleStat)
	mux.HandleFunc("/disk", handleDisk)
	mux.HandleFunc("/transfer", handleTransfer)
	mux.HandleFunc("/upload", handleUpload)
	mux.HandleFunc("/download/", handleDownload)
	mux.HandleFunc("/history", handleHistory)
	mux.HandleFunc("/history/", handleHistoryDetail)
	mux.HandleFunc("/history-files", handleHistoryFiles)
	mux.HandleFunc("/history-file/", handleHistoryFile)

	addr := fmt.Sprintf(":%d", port)
	if target != "" {
		// 代理模式：纯转发，忽略 shell 和 auto-confirm 参数
		log.Printf("代理服务已启动 (代理模式)")
		log.Printf("监听端口: %d, 目标节点: %s", port, target)
		log.Printf("说明: 代理模式下仅转发请求，不执行本地命令")
	} else {
		// 本地模式：执行本地命令
		log.Printf("代理服务已启动 (本地模式)")
		log.Printf("监听端口: %d, shell: %s, auto-confirm: %s", port, shell, autoConfirm)
	}
	log.Printf("访问 http://<主机地址>%s/cmd 执行命令", addr)
	log.Printf("访问 http://<主机地址>%s/tail?path=... 读取文件尾部", addr)
	log.Printf("访问 http://<主机地址>%s/stat?path=... 查看文件信息", addr)
	log.Printf("访问 http://<主机地址>%s/disk 查看磁盘使用", addr)
	log.Printf("访问 http://<主机地址>%s/transfer 跨节点文件传输", addr)
	log.Printf("访问 http://<主机地址>%s/upload 上传文件", addr)
	log.Printf("访问 http://<主机地址>%s/download/文件名 下载文件", addr)
	log.Printf("访问 http://<主机地址>%s/history 查看本次执行历史", addr)
	log.Printf("访问 http://<主机地址>%s/history-files 查看所有历史文件", addr)

	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("服务启动失败: %v", err)
	}
}

// initHistoryFile 初始化历史记录文件
func initHistoryFile() {
	// 确保目录存在
	dir := filepath.Dir(historyFile)
	if dir != "." && dir != "" {
		os.MkdirAll(dir, 0755)
	}

	// 如果文件不存在，创建空文件
	if _, err := os.Stat(historyFile); os.IsNotExist(err) {
		history := ExecutionHistory{
			Records: []ExecutionRecord{},
			NextID:  1,
		}
		saveHistory(&history)
	}
}

// loadHistory 加载历史记录
func loadHistory() (*ExecutionHistory, error) {
	data, err := os.ReadFile(historyFile)
	if err != nil {
		return nil, err
	}

	var history ExecutionHistory
	if err := json.Unmarshal(data, &history); err != nil {
		return nil, err
	}
	return &history, nil
}

// saveHistory 保存历史记录
func saveHistory(history *ExecutionHistory) error {
	data, err := json.MarshalIndent(history, "", "  ")
	if err != nil {
		return err
	}

	// 原子写入
	tmpFile := historyFile + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmpFile, historyFile)
}

// addRecord 添加执行记录（线程安全）
func addRecord(record *ExecutionRecord) error {
	historyMutex.Lock()
	defer historyMutex.Unlock()

	history, err := loadHistory()
	if err != nil {
		return err
	}

	record.ID = history.NextID
	history.Records = append(history.Records, *record)
	history.NextID++

	// 保留最近 1000 条记录
	if len(history.Records) > 1000 {
		history.Records = history.Records[len(history.Records)-1000:]
	}

	return saveHistory(history)
}

// generateToken 生成随机token
func generateToken() string {
	bytes := make([]byte, 16)
	rand.Read(bytes)
	return hex.EncodeToString(bytes)
}

// validateToken 验证请求token
func validateToken(r *http.Request) bool {
	reqToken := r.Header.Get("X-Token")
	return reqToken == serverToken
}

// unauthorized 返回未授权响应
func unauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	json.NewEncoder(w).Encode(map[string]string{
		"error": "未授权: 无效或缺失 X-Token",
	})
}

func handleCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "仅支持 POST 方法", http.StatusMethodNotAllowed)
		return
	}

	// 验证token
	if !validateToken(r) {
		unauthorized(w)
		return
	}

	var req CommandRequest
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "读取请求失败: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "解析请求失败: "+err.Error(), http.StatusBadRequest)
		return
	}

	log.Printf("远程请求cmd: %s", req.Cmd)

	// 检查危险命令
	if isBlockedCommand(req.Cmd) {
		log.Printf("拒绝执行危险命令: %s", req.Cmd)
		jsonEncode(w, CommandResponse{ExitCode: -1, Error: "拒绝执行危险命令"})
		return
	}

	// 代理模式：转发到目标节点
	if target != "" {
		proxyCommand(w, r, req)
		return
	}

	if strings.ToLower(autoConfirm) != "yes" {
		approved := promptConfirm()
		if !approved {
			log.Println("命令已被拒绝")
			jsonEncode(w, CommandResponse{ExitCode: -1, Error: "命令已被用户拒绝"})
			return
		}
		log.Println("cmd结果已成功返回!")
	}

	// 执行命令（支持请求级超时覆盖）
	timeout := 60 * time.Second
	if req.TimeoutSeconds > 0 {
		timeout = time.Duration(req.TimeoutSeconds) * time.Second
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	res := executeCommand(ctx, req.Cmd)

	// 记录执行历史（为了兼容旧历史记录，将 stdout+stderr 合并到 Output）
	record := &ExecutionRecord{
		Timestamp: time.Now(),
		Cmd:       req.Cmd,
		Output:    res.Stdout,
		Success:   res.ExitCode == 0 && res.Error == "",
		Duration:  res.DurationMs,
	}
	if res.Error != "" {
		record.Error = res.Error
	}
	if res.Stderr != "" {
		record.Error += " | stderr: " + res.Stderr
	}
	if res.ExitCode != 0 {
		log.Printf("命令执行失败: %s, exit_code=%d, error=%s, stderr=%s", req.Cmd, res.ExitCode, res.Error, res.Stderr)
	} else {
		log.Printf("命令执行成功: %s, stdout=%dB, duration=%dms", req.Cmd, len(res.Stdout), res.DurationMs)
	}

	// 保存历史记录
	if saveErr := addRecord(record); saveErr != nil {
		log.Printf("保存历史记录失败: %v", saveErr)
	}

	jsonEncode(w, res)
}

// handleHistory 获取历史记录列表
func handleHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "仅支持 GET 方法", http.StatusMethodNotAllowed)
		return
	}

	// 验证token
	if !validateToken(r) {
		unauthorized(w)
		return
	}

	history, err := loadHistory()
	if err != nil {
		http.Error(w, "加载历史记录失败", http.StatusInternalServerError)
		return
	}

	// 可选：限制返回数量
	limit := 100
	if len(history.Records) > limit {
		history.Records = history.Records[len(history.Records)-limit:]
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(history.Records)
}

// handleHistoryDetail 获取单条历史记录详情
func handleHistoryDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "仅支持 GET 方法", http.StatusMethodNotAllowed)
		return
	}

	// 验证token
	if !validateToken(r) {
		unauthorized(w)
		return
	}

	// 提取 ID
	idStr := strings.TrimPrefix(r.URL.Path, "/history/")
	if idStr == "" {
		http.Error(w, "缺少 ID", http.StatusBadRequest)
		return
	}

	var id int
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil {
		http.Error(w, "无效的 ID", http.StatusBadRequest)
		return
	}

	history, err := loadHistory()
	if err != nil {
		http.Error(w, "加载历史记录失败", http.StatusInternalServerError)
		return
	}

	for _, record := range history.Records {
		if record.ID == id {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(record)
			return
		}
	}

	http.Error(w, "记录不存在", http.StatusNotFound)
}

// proxyCommand 转发命令到目标节点
func proxyCommand(w http.ResponseWriter, r *http.Request, req CommandRequest) {
	targetURL := target + "/cmd"
	jsonData, _ := json.Marshal(req)

	httpReq, err := http.NewRequest(http.MethodPost, targetURL, strings.NewReader(string(jsonData)))
	if err != nil {
		log.Printf("创建转发请求失败: %v", err)
		jsonEncode(w, CommandResponse{ExitCode: -1, Error: "创建转发请求失败: " + err.Error()})
		return
	}

	// 转发token
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Token", r.Header.Get("X-Token"))

	startTime := time.Now()
	client := getProxyClient(proxyTimeout)
	resp, err := client.Do(httpReq)
	if err != nil {
		log.Printf("代理转发失败: %v", err)
		jsonEncode(w, CommandResponse{ExitCode: -1, Error: "代理转发失败: " + err.Error()})
		return
	}
	defer resp.Body.Close()

	var result CommandResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Printf("解析目标响应失败: %v", err)
		jsonEncode(w, CommandResponse{ExitCode: -1, Error: "解析目标响应失败: " + err.Error()})
		return
	}

	log.Printf("代理转发成功: %s -> %s", req.Cmd, target)

	// 记录执行历史（兼容旧格式：将 stdout+stderr 合并到 Output）
	record := &ExecutionRecord{
		Timestamp: startTime,
		Cmd:       req.Cmd,
		Output:    result.Stdout,
		Success:   result.ExitCode == 0 && result.Error == "",
		Duration:  time.Since(startTime).Milliseconds(),
	}
	if result.Error != "" {
		record.Error = result.Error
	}
	if result.Stderr != "" {
		record.Error += " | stderr: " + result.Stderr
	}
	if saveErr := addRecord(record); saveErr != nil {
		log.Printf("保存历史记录失败: %v", saveErr)
	}

	jsonEncode(w, result)
}

func promptConfirm() bool {
	fmt.Print("是否同意执行 y/n: ")
	var response string
	fmt.Scanln(&response)
	return strings.ToLower(strings.TrimSpace(response)) == "y"
}

func executeCommand(ctx context.Context, cmdStr string) CommandResponse {
	var cmd *exec.Cmd

	switch shell {
	case "bash":
		cmd = exec.CommandContext(ctx, "bash", "-c", cmdStr)
	case "zsh":
		cmd = exec.CommandContext(ctx, "zsh", "-c", cmdStr)
	case "sh":
		cmd = exec.CommandContext(ctx, "sh", "-c", cmdStr)
	default:
		return CommandResponse{ExitCode: -1, Error: fmt.Sprintf("不支持的shell类型: %s", shell)}
	}

	stdoutBuf := &bytes.Buffer{}
	stderrBuf := &bytes.Buffer{}
	cmd.Stdout = &cappedWriter{w: stdoutBuf, max: maxCommandStdout}
	cmd.Stderr = &cappedWriter{w: stderrBuf, max: maxCommandStderr}

	start := time.Now()
	err := cmd.Run()
	duration := time.Since(start).Milliseconds()

	res := CommandResponse{
		Stdout:     stdoutBuf.String(),
		Stderr:     stderrBuf.String(),
		DurationMs: duration,
	}

	// 检查 stdout 是否被截断
	if stdoutBuf.Len() >= maxCommandStdout {
		res.Truncated = true
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			res.ExitCode = exitErr.ExitCode()
		} else {
			res.ExitCode = -1
			res.Error = err.Error()
		}
		return res
	}

	res.ExitCode = 0
	return res
}

func jsonEncode(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut && r.Method != http.MethodPost {
		http.Error(w, "仅支持 PUT 或 POST 方法", http.StatusMethodNotAllowed)
		return
	}

	// 验证token
	if !validateToken(r) {
		unauthorized(w)
		return
	}

	// 获取文件名
	filename := r.URL.Path
	if strings.HasPrefix(filename, "/upload/") {
		filename = strings.TrimPrefix(filename, "/upload/")
	} else {
		// 从请求头获取文件名
		contentDisposition := r.Header.Get("Content-Disposition")
		if contentDisposition != "" {
			parts := strings.Split(contentDisposition, ";")
			for _, part := range parts {
				part = strings.TrimSpace(part)
				if strings.HasPrefix(part, "filename=") {
					filename = strings.Trim(part[9:], "\"'")
					break
				}
			}
		}
		// 如果仍未获取到文件名，使用默认文件名
		if filename == "/upload" || filename == "" {
			filename = "uploaded_file"
		}
	}

	// 代理模式：转发到目标节点
	if target != "" {
		proxyUpload(w, r, filename)
		return
	}

	// 检查文件大小限制
	contentLength := r.ContentLength
	if contentLength > maxUploadSize {
		log.Printf("文件大小超过限制: %d > %d", contentLength, maxUploadSize)
		http.Error(w, fmt.Sprintf("文件大小超过限制（最大%dMB）", maxUploadSize/(1024*1024)), http.StatusRequestEntityTooLarge)
		return
	}

	// 创建上传目录
	if err := os.MkdirAll("uploads", 0755); err != nil {
		log.Printf("创建上传目录失败: %v", err)
		http.Error(w, "创建上传目录失败", http.StatusInternalServerError)
		return
	}

	// 保存文件
	filePath := filepath.Join("uploads", filename)
	dst, err := os.Create(filePath)
	if err != nil {
		log.Printf("创建文件失败: %v", err)
		http.Error(w, "创建文件失败", http.StatusInternalServerError)
		return
	}
	defer dst.Close()

	// 使用进度跟踪写入，支持检测卡住
	tracker := newProgressTracker(contentLength)
	if _, err = writeWithProgress(dst, r.Body, tracker); err != nil {
		log.Printf("写入文件失败: %v", err)
		http.Error(w, "写入文件失败: "+err.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("文件上传成功: %s", filePath)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"message": "文件上传成功",
		"path":    filePath,
	})
}

// uploadProgressTracker 跟踪上传进度，用于区分正常慢速上传和真正卡住
type uploadProgressTracker struct {
	totalSize     int64
	writtenSize   int64
	lastWritten   int64
	lastCheckTime time.Time
	stuckTimeout  time.Duration // 超过这个时间没有新数据则认为卡住
}

func newProgressTracker(totalSize int64) *uploadProgressTracker {
	return &uploadProgressTracker{
		totalSize:     totalSize,
		writtenSize:   0,
		lastWritten:   0,
		lastCheckTime: time.Now(),
		stuckTimeout:  30 * time.Second,
	}
}

// isStuck 检查上传是否卡住（超过stuckTimeout没有新数据写入）
func (t *uploadProgressTracker) isStuck() bool {
	if t.writtenSize == t.lastWritten {
		return time.Since(t.lastCheckTime) > t.stuckTimeout
	}
	t.lastWritten = t.writtenSize
	t.lastCheckTime = time.Now()
	return false
}

// writeWithProgress 带进度跟踪的写入，支持检测卡住并中断
func writeWithProgress(dst *os.File, src io.Reader, tracker *uploadProgressTracker) (int64, error) {
	buffer := make([]byte, 32*1024) // 32KB buffer
	totalWritten := int64(0)

	for {
		n, err := src.Read(buffer)
		if n > 0 {
			written, writeErr := dst.Write(buffer[:n])
			if writeErr != nil {
				return totalWritten, writeErr
			}
			totalWritten += int64(written)
			tracker.writtenSize = totalWritten

			// 每写入10MB打印进度
			if totalWritten%(10*1024*1024) == 0 {
				log.Printf("上传进度: %dMB / %dMB", totalWritten/(1024*1024), tracker.totalSize/(1024*1024))
			}

			// 检查是否卡住
			if tracker.isStuck() {
				return totalWritten, fmt.Errorf("上传卡住（超过30秒无新数据）")
			}
		}
		if err != nil {
			if err == io.EOF {
				return totalWritten, nil
			}
			return totalWritten, err
		}
	}
}

func proxyUpload(w http.ResponseWriter, r *http.Request, filename string) {
	targetURL := target + "/upload/" + filename

	// 创建流式转发请求，不将整个文件读入内存
	req, err := http.NewRequest(http.MethodPut, targetURL, r.Body)
	if err != nil {
		log.Printf("创建转发请求失败: %v", err)
		http.Error(w, "创建转发请求失败", http.StatusInternalServerError)
		return
	}

	// 复制原始请求头
	if disp := r.Header.Get("Content-Disposition"); disp != "" {
		req.Header.Set("Content-Disposition", disp)
	}
	// 转发token
	req.Header.Set("X-Token", r.Header.Get("X-Token"))

	client := getProxyClient(proxyTimeout)
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("代理上传失败: %v", err)
		http.Error(w, "代理上传失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	// 返回目标节点的响应
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
	log.Printf("代理上传成功: %s -> %s", filename, target)
}

func handleDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "仅支持 GET 方法", http.StatusMethodNotAllowed)
		return
	}

	// 验证token
	if !validateToken(r) {
		unauthorized(w)
		return
	}

	// 获取文件名
	filename := r.URL.Path
	if strings.HasPrefix(filename, "/download/") {
		filename = strings.TrimPrefix(filename, "/download/")
	} else {
		http.Error(w, "无效的请求路径", http.StatusBadRequest)
		return
	}

	// 代理模式：转发到目标节点
	if target != "" {
		proxyDownload(w, r, filename)
		return
	}

	// 构建文件路径
	filePath := filepath.Join("uploads", filename)

	// 检查文件是否存在
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		http.Error(w, "文件不存在", http.StatusNotFound)
		return
	}

	// 设置响应头
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filename))
	w.Header().Set("Content-Type", "application/octet-stream")

	// 发送文件内容
	src, err := os.Open(filePath)
	if err != nil {
		log.Printf("打开文件失败: %v", err)
		http.Error(w, "打开文件失败", http.StatusInternalServerError)
		return
	}
	defer src.Close()

	if _, err = io.Copy(w, src); err != nil {
		log.Printf("发送文件失败: %v", err)
		http.Error(w, "发送文件失败", http.StatusInternalServerError)
		return
	}

	log.Printf("文件下载成功: %s", filePath)
}

// proxyDownload 转发下载请求到目标节点
func proxyDownload(w http.ResponseWriter, r *http.Request, filename string) {
	targetURL := target + "/download/" + filename

	req, err := http.NewRequest(http.MethodGet, targetURL, nil)
	if err != nil {
		log.Printf("创建转发请求失败: %v", err)
		http.Error(w, "创建转发请求失败", http.StatusInternalServerError)
		return
	}

	// 转发token
	req.Header.Set("X-Token", r.Header.Get("X-Token"))

	client := getProxyClient(proxyTimeout)
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("代理下载失败: %v", err)
		http.Error(w, "代理下载失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
		return
	}

	// 设置响应头并流式传输
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filename))
	w.Header().Set("Content-Type", "application/octet-stream")
	io.Copy(w, resp.Body)
	log.Printf("代理下载成功: %s -> %s", filename, target)
}

// handleTransfer 跨节点文件传输：A（本机）作为中转，从 source 下载并上传到 target
// POST /transfer
// Body: {"source_url":"http://B:8080/download/file.txt","target_url":"http://C:8080/upload/file.txt"}
func handleTransfer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "仅支持 POST 方法", http.StatusMethodNotAllowed)
		return
	}
	if !validateToken(r) {
		unauthorized(w)
		return
	}

	var req TransferRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonEncode(w, TransferResponse{Success: false, Error: "解析请求失败: " + err.Error()})
		return
	}
	if req.SourceURL == "" || req.TargetURL == "" {
		jsonEncode(w, TransferResponse{Success: false, Error: "缺少 source_url 或 target_url"})
		return
	}

	sourceToken := req.SourceToken
	if sourceToken == "" {
		sourceToken = serverToken
	}
	targetToken := req.TargetToken
	if targetToken == "" {
		targetToken = serverToken
	}

	start := time.Now()

	// 1. 从源节点下载（流式，不落地磁盘）
	downloadReq, err := http.NewRequest(http.MethodGet, req.SourceURL, nil)
	if err != nil {
		jsonEncode(w, TransferResponse{Success: false, Error: "创建下载请求失败: " + err.Error()})
		return
	}
	downloadReq.Header.Set("X-Token", sourceToken)

	client := getProxyClient(proxyTimeout)
	downloadResp, err := client.Do(downloadReq)
	if err != nil {
		jsonEncode(w, TransferResponse{Success: false, Error: "下载失败: " + err.Error()})
		return
	}
	defer downloadResp.Body.Close()

	if downloadResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(downloadResp.Body)
		jsonEncode(w, TransferResponse{Success: false, Error: fmt.Sprintf("源节点返回 %d: %s", downloadResp.StatusCode, string(body))})
		return
	}

	// 2. 直接流式上传到目标节点（不经过内存/磁盘）
	uploadReq, err := http.NewRequest(http.MethodPut, req.TargetURL, downloadResp.Body)
	if err != nil {
		jsonEncode(w, TransferResponse{Success: false, Error: "创建上传请求失败: " + err.Error()})
		return
	}
	uploadReq.Header.Set("X-Token", targetToken)
	uploadReq.Header.Set("Content-Type", "application/octet-stream")
	if length := downloadResp.ContentLength; length > 0 {
		uploadReq.ContentLength = length
	}

	uploadResp, err := client.Do(uploadReq)
	if err != nil {
		jsonEncode(w, TransferResponse{Success: false, Error: "上传失败: " + err.Error()})
		return
	}
	defer uploadResp.Body.Close()

	if uploadResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(uploadResp.Body)
		jsonEncode(w, TransferResponse{Success: false, Error: fmt.Sprintf("目标节点返回 %d: %s", uploadResp.StatusCode, string(body))})
		return
	}

	// 3. 返回成功
	duration := time.Since(start).Milliseconds()
	var bytes int64
	if downloadResp.ContentLength > 0 {
		bytes = downloadResp.ContentLength
	}
	jsonEncode(w, TransferResponse{
		Success:    true,
		Bytes:      bytes,
		DurationMs: duration,
	})
	log.Printf("跨节点传输成功: %s -> %s, 耗时 %dms", req.SourceURL, req.TargetURL, duration)
}

// HistoryFileInfo 历史文件信息
type HistoryFileInfo struct {
	Filename  string `json:"filename"`
	StartTime string `json:"start_time"`
	Records   int    `json:"records"`
}

// handleHistoryFiles 列出所有历史文件
func handleHistoryFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "仅支持 GET 方法", http.StatusMethodNotAllowed)
		return
	}

	// 验证token
	if !validateToken(r) {
		unauthorized(w)
		return
	}

	// 查找所有历史文件
	files, err := filepath.Glob("ops-history_*.json")
	if err != nil {
		http.Error(w, "查找历史文件失败", http.StatusInternalServerError)
		return
	}

	// 按时间倒序排列（最新的在前）
	for i, j := 0, len(files)-1; i < j; i, j = i+1, j-1 {
		files[i], files[j] = files[j], files[i]
	}

	// 读取每个文件的基本信息
	var result []HistoryFileInfo
	for _, f := range files {
		// 从文件名解析时间: ops-history_2026-05-29_22-21-12.json
		name := filepath.Base(f)
		timeStr := strings.TrimPrefix(name, "ops-history_")
		timeStr = strings.TrimSuffix(timeStr, ".json")
		timeStr = strings.Replace(timeStr, "_", " ", 1) // 第一个下划线替换为空格

		// 读取文件获取记录数
		data, err := os.ReadFile(f)
		records := 0
		if err == nil {
			var history ExecutionHistory
			if json.Unmarshal(data, &history) == nil {
				records = len(history.Records)
			}
		}

		result = append(result, HistoryFileInfo{
			Filename:  name,
			StartTime: timeStr,
			Records:   records,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// handleHistoryFile 读取指定历史文件
func handleHistoryFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "仅支持 GET 方法", http.StatusMethodNotAllowed)
		return
	}

	// 验证token
	if !validateToken(r) {
		unauthorized(w)
		return
	}

	// 获取文件名
	filename := strings.TrimPrefix(r.URL.Path, "/history-file/")
	if filename == "" {
		http.Error(w, "缺少文件名", http.StatusBadRequest)
		return
	}

	// 安全检查：只允许 ops-history_*.json 文件
	if !strings.HasPrefix(filename, "ops-history_") || !strings.HasSuffix(filename, ".json") {
		http.Error(w, "无效的文件名", http.StatusBadRequest)
		return
	}

	// 读取文件
	data, err := os.ReadFile(filename)
	if err != nil {
		http.Error(w, "文件不存在", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

// handleTail 读取文件最后 N 行（类似 tail -n）
// GET /tail?path=/var/log/syslog&lines=100&max_bytes=1048576
func handleTail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "仅支持 GET 方法", http.StatusMethodNotAllowed)
		return
	}
	if !validateToken(r) {
		unauthorized(w)
		return
	}

	path := r.URL.Query().Get("path")
	if path == "" {
		jsonEncode(w, TailResponse{Error: "缺少 path 参数"})
		return
	}
	if !filepath.IsAbs(path) {
		jsonEncode(w, TailResponse{Error: "path 必须是绝对路径"})
		return
	}
	if strings.Contains(path, "..") {
		jsonEncode(w, TailResponse{Error: "path 不能包含 .."})
		return
	}

	lines := 100
	if v := r.URL.Query().Get("lines"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			lines = n
		}
	}
	maxBytes := int64(1 << 20) // 1 MiB
	if v := r.URL.Query().Get("max_bytes"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			maxBytes = n
		}
	}

	res := TailResponse{Lines: []string{}}

	f, err := os.Open(path)
	if err != nil {
		res.Error = err.Error()
		jsonEncode(w, res)
		return
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		res.Error = err.Error()
		jsonEncode(w, res)
		return
	}
	res.FileSize = st.Size()

	var data []byte
	if st.Size() > maxBytes {
		res.Truncated = true
		if _, err := f.Seek(st.Size()-maxBytes, io.SeekStart); err != nil {
			res.Error = err.Error()
			jsonEncode(w, res)
			return
		}
	}
	data, err = io.ReadAll(f)
	if err != nil {
		res.Error = err.Error()
		jsonEncode(w, res)
		return
	}

	all := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if res.Truncated && len(all) > 0 {
		// 首行可能因 seek 到中间而残缺，丢弃
		all = all[1:]
	}
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	if len(all) == 1 && all[0] == "" {
		all = []string{}
	}
	res.Lines = all
	res.TotalLinesReturned = len(all)
	jsonEncode(w, res)
}

// handleStat 查看文件/目录的元信息
// GET /stat?path=/var/log
func handleStat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "仅支持 GET 方法", http.StatusMethodNotAllowed)
		return
	}
	if !validateToken(r) {
		unauthorized(w)
		return
	}

	path := r.URL.Query().Get("path")
	if path == "" {
		jsonEncode(w, StatResponse{Error: "缺少 path 参数"})
		return
	}
	if !filepath.IsAbs(path) {
		jsonEncode(w, StatResponse{Error: "path 必须是绝对路径"})
		return
	}
	if strings.Contains(path, "..") {
		jsonEncode(w, StatResponse{Error: "path 不能包含 .."})
		return
	}

	st, err := os.Stat(path)
	if err != nil {
		jsonEncode(w, StatResponse{Path: path, Error: err.Error()})
		return
	}

	typ := "file"
	if st.IsDir() {
		typ = "dir"
	}
	if st.Mode()&os.ModeSymlink != 0 {
		typ = "symlink"
	}

	res := StatResponse{
		Path:      path,
		Type:      typ,
		SizeBytes: st.Size(),
		SizeHuman: humanizeBytes(st.Size()),
		Mtime:     st.ModTime(),
		Mode:      st.Mode().String(),
	}

	// 尝试获取 owner/group（Linux/macOS）
	if sys, ok := st.Sys().(*syscall.Stat_t); ok {
		if u, err := user.LookupId(fmt.Sprintf("%d", sys.Uid)); err == nil {
			res.Owner = u.Username
		}
		if g, err := user.LookupGroupId(fmt.Sprintf("%d", sys.Gid)); err == nil {
			res.Group = g.Name
		}
	}

	jsonEncode(w, res)
}

// handleDisk 查看磁盘使用情况
// GET /disk
func handleDisk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "仅支持 GET 方法", http.StatusMethodNotAllowed)
		return
	}
	if !validateToken(r) {
		unauthorized(w)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// 使用 `df -kP` 获取所有挂载点（POSIX 兼容）
	cmd := exec.CommandContext(ctx, "df", "-kP")
	out, err := cmd.Output()
	if err != nil {
		errStr := err.Error()
		if exitErr, ok := err.(*exec.ExitError); ok && len(exitErr.Stderr) > 0 {
			errStr = string(exitErr.Stderr)
		}
		jsonEncode(w, DiskResponse{Error: errStr})
		return
	}

	var entries []DiskEntry
	lines := strings.Split(string(out), "\n")
	for i, line := range lines {
		if i == 0 {
			continue // 跳过表头
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// df -kP 输出格式：Filesystem 1K-blocks Used Available Use% Mounted_on
		// 注意 Filesystem 可能包含空格（如 "map auto_home"），所以从末尾解析
		parts := strings.Fields(line)
		if len(parts) < 6 {
			continue
		}
		mountpoint := strings.Join(parts[5:], " ")
		if total, e1 := strconv.ParseInt(parts[1], 10, 64); e1 == nil {
			if used, e2 := strconv.ParseInt(parts[2], 10, 64); e2 == nil {
				if avail, e3 := strconv.ParseInt(parts[3], 10, 64); e3 == nil {
					total *= 1024
					used *= 1024
					avail *= 1024
					entries = append(entries, DiskEntry{
						Filesystem:  parts[0],
						Mountpoint:  mountpoint,
						SizeBytes:   total,
						SizeHuman:   humanizeBytes(total),
						UsedBytes:   used,
						UsedHuman:   humanizeBytes(used),
						FreeBytes:   avail,
						FreeHuman:   humanizeBytes(avail),
						UsedPercent: parts[4],
					})
				}
			}
		}
	}

	jsonEncode(w, DiskResponse{Filesystems: entries})
}

// humanizeBytes 将字节数转为人类可读格式
func humanizeBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
