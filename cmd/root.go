package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

var (
	port        int
	shell       string
	autoConfirm string
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

func startServer() {
	mux := http.NewServeMux()
	mux.HandleFunc("/cmd", handleCommand)
	mux.HandleFunc("/upload", handleUpload)

	addr := fmt.Sprintf(":%d", port)
	log.Printf("代理服务已启动，监听端口 %d (shell: %s, auto-confirm: %s)", port, shell, autoConfirm)
	log.Printf("访问 http://<主机地址>%s/cmd 执行命令", addr)
	log.Printf("访问 http://<主机地址>%s/upload 上传文件", addr)

	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("服务启动失败: %v", err)
	}
}

func handleCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "仅支持 POST 方法", http.StatusMethodNotAllowed)
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

	if strings.ToLower(autoConfirm) != "yes" {
		approved := promptConfirm()
		if !approved {
			log.Println("命令已被拒绝")
			jsonEncode(w, CommandResponse{Output: "", Error: "命令已被用户拒绝"})
			return
		}
		log.Println("cmd结果已成功返回!")
	}

	output, err := executeCommand(req.Cmd)
	if err != nil {
		jsonEncode(w, CommandResponse{Output: output, Error: err.Error()})
		return
	}

	jsonEncode(w, CommandResponse{Output: output})
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

	// 读取并写入文件内容
	if _, err = io.Copy(dst, r.Body); err != nil {
		log.Printf("写入文件失败: %v", err)
		http.Error(w, "写入文件失败", http.StatusInternalServerError)
		return
	}

	log.Printf("文件上传成功: %s", filePath)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"message": "文件上传成功",
		"path":    filePath,
	})
}
