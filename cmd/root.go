package cmd

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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
	Cmd string `json:"cmd"`
}

type CommandResponse struct {
	Output string `json:"output"`
	Error  string `json:"error,omitempty"`
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
		jsonEncode(w, CommandResponse{Output: "", Error: "拒绝执行危险命令"})
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
			jsonEncode(w, CommandResponse{Output: "", Error: "命令已被用户拒绝"})
			return
		}
		log.Println("cmd结果已成功返回!")
	}

	// 记录开始时间
	startTime := time.Now()

	output, err := executeCommand(req.Cmd)

	// 记录执行历史
	record := &ExecutionRecord{
		Timestamp: startTime,
		Cmd:       req.Cmd,
		Output:    output,
		Success:   err == nil,
		Duration:  time.Since(startTime).Milliseconds(),
	}
	if err != nil {
		record.Error = err.Error()
		log.Printf("命令执行失败: %s, 错误: %v, 输出: %s", req.Cmd, err, output)
	} else {
		log.Printf("命令执行成功: %s, 输出: %s", req.Cmd, output)
	}

	// 保存历史记录
	if saveErr := addRecord(record); saveErr != nil {
		log.Printf("保存历史记录失败: %v", saveErr)
	}

	if err != nil {
		jsonEncode(w, CommandResponse{Output: output, Error: err.Error()})
		return
	}
	jsonEncode(w, CommandResponse{Output: output})
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
		jsonEncode(w, CommandResponse{Output: "", Error: "创建转发请求失败: " + err.Error()})
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
		jsonEncode(w, CommandResponse{Output: "", Error: "代理转发失败: " + err.Error()})
		return
	}
	defer resp.Body.Close()

	var result CommandResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Printf("解析目标响应失败: %v", err)
		jsonEncode(w, CommandResponse{Output: "", Error: "解析目标响应失败: " + err.Error()})
		return
	}

	log.Printf("代理转发成功: %s -> %s", req.Cmd, target)

	// 记录执行历史
	record := &ExecutionRecord{
		Timestamp: startTime,
		Cmd:       req.Cmd,
		Output:    result.Output,
		Error:     result.Error,
		Success:   result.Error == "",
		Duration:  time.Since(startTime).Milliseconds(),
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

func executeCommand(cmdStr string) (string, error) {
	var cmd *exec.Cmd

	switch shell {
	case "bash":
		cmd = exec.Command("bash", "-c", cmdStr)
	case "zsh":
		cmd = exec.Command("zsh", "-c", cmdStr)
	case "sh":
		cmd = exec.Command("sh", "-c", cmdStr)
	default:
		return "", fmt.Errorf("不支持的shell类型: %s", shell)
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), err
	}

	return string(output), nil
}

func jsonEncode(w http.ResponseWriter, v interface{}) {
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

	// 读取请求体
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "读取请求体失败", http.StatusInternalServerError)
		return
	}

	// 创建转发请求
	req, err := http.NewRequest(http.MethodPut, targetURL, strings.NewReader(string(body)))
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
