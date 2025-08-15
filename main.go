package main

import (
	"archive/zip"
	"bufio"
	"compress/flate"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"net/http"

	"github.com/BurntSushi/toml"
)

// 全域變數,結構定義

var (
	config      Config
	backupMutex sync.Mutex
	logFile     *os.File
)

// Config 結構, config.toml 映射
type Config struct {
	General struct {
		WindowTitle string `toml:"window_title"`
		Language    string `toml:"language"`
	} `toml:"general"`

	Server struct {
		JavaPath            string   `toml:"java_path"`
		JvmArgs             []string `toml:"jvm_args"`
		ServerArgs          []string `toml:"server_args"`
		AutoRestart         bool     `toml:"auto_restart"`
		RestartDelaySeconds int      `toml:"restart_delay_seconds"`
	} `toml:"server"`

	Backup struct {
		Enabled           bool     `toml:"enabled"`
		Interval          string   `toml:"interval"`
		ManagerCommands   []string `toml:"manager_commands"`
		CompressionLevel  int      `toml:"compression_level"`
		Sources           []string `toml:"sources"`
		Exclusions        []string `toml:"exclusions"`
		Destination       string   `toml:"destination"`
		RetentionCount    int      `toml:"retention_count"`
		MaxTotalSizeGB    int      `toml:"max_total_size_gb"`
		Workers           int      `toml:"workers"`
	} `toml:"backup"`

	Discord struct {
		Enabled             bool     `toml:"enabled"`
		BotToken            string   `toml:"bot_token"`
		ChannelID           string   `toml:"channel_id"`
		ForwardEvents       []string `toml:"forward_events"`
		ForwardFromDiscord  bool     `toml:"forward_from_discord"`
		IngameFormat        string   `toml:"ingame_format"`
		Patterns            struct {
			Chat        string `toml:"chat"`
			Join        string `toml:"join"`
			Leave       string `toml:"leave"`
			Death       string `toml:"death"`
			Advancement string `toml:"advancement"`
		} `toml:"patterns"`
	} `toml:"discord"`
}

// 初始化

// 設定工作目錄
func init() {
	exePath, err := os.Executable()
	if err != nil {
		log.Fatalf("致命錯誤: 無法獲取可執行檔案路徑: %v", err)
	}
	exeDir := filepath.Dir(exePath)

	// 'go run' 例外
	isGoRun := strings.Contains(exeDir, "go-build") || strings.HasPrefix(exeDir, os.TempDir())
	if !isGoRun {
		if err := os.Chdir(exeDir); err != nil {
			log.Fatalf("致命錯誤: 無法切換到程式根目錄 %s: %v", exeDir, err)
		}
	}
}

// 主程序
func main() {
	// 語言環境
	exec.Command("cmd", "/c", "chcp 65001 > nul").Run()
	
	// 載入config
	if err := loadConfig(); err != nil {
		fmt.Printf("錯誤: %v\n", err)
		fmt.Println("請按 Enter 鍵退出...")
		bufio.NewReader(os.Stdin).ReadBytes('\n')
		os.Exit(1)
	}

	// 初始化log文件
	if err := setupLogger(); err != nil {
		log.Fatalf("無法設定日誌: %v", err)
	}
	defer logFile.Close()

	// 設定視窗標題
	if config.WindowTitle != "" {
		exec.Command("cmd", "/c", "title", config.WindowTitle).Run()
	}

	log.Println("Minecraft Server Manager v18. by yuni_sakana")
	log.Printf("工作目錄 (基準點): %s", mustGetwd())
	log.Printf("備份目錄: %s", config.Backup.Destination)

	// 建立帶取消功能的上下文
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	// 輸入監聽器，捕獲 Ctrl+C
	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan // 阻塞直到接收到訊號
		log.Println("主程式關閉...")
		cancel() // 發送關閉通知
	}()

	// 啟動核心服務
	wg.Add(1) // 只需等待伺服器管理器
	go func() {
		defer wg.Done()
		runServerManager(ctx)
	}()

	// 等待所有核心服務結束
	wg.Wait()
	log.Println("程式退出。")
}

// 核心邏輯 - 伺服器與備份管理

// runServerManager 負責伺服器管理（啟動、監控、重啟）
func runServerManager(ctx context.Context) {
	workDir := mustGetwd()

	// 首次啟動前執行一次備份
	if config.Backup.Enabled {
		log.Println("初始備份...")
		runBackup()
	}

	// 定時備份排程器(異步)
	var backupWg sync.WaitGroup
	if config.Backup.Enabled {
		backupWg.Add(1)
		go func() {
			defer backupWg.Done()
			runBackupScheduler(ctx)
		}()
	}

	// 主重啟迴圈
	for {
		// 在每次迴圈開始時，檢查關閉訊號
		select {
		case <-ctx.Done():
			log.Println("Close Signal 停止重啟迴圈。")
			backupWg.Wait() // 等待備份排程器結束
			return
		default:
			// 繼續執行
		}

		log.Println("正在啟動伺服器...")

		// 準備啟動參數
		allArgs := []string{}
		allArgs = append(allArgs, config.Server.JvmArgs...)
		allArgs = append(allArgs, config.Server.ServerArgs...)

		// 建立帶上下文的指令
		cmd := exec.CommandContext(ctx, config.Server.JavaPath, allArgs...)
		cmd.Dir = workDir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		// 建立輸入代理
		serverStdin, err := cmd.StdinPipe()
		if err != nil {
			log.Printf("錯誤：無法建立到伺服器的輸入代理: %v", err)
			return // 嚴重錯誤，退出管理器
		}

		// 非同步啟動伺服器
		if err := cmd.Start(); err != nil {
			log.Printf("錯誤：啟動伺服器失敗: %v", err)
			if !config.Server.AutoRestart {
				break
			}
			goto RESTART_DELAY // 如果啟動失敗，直接跳到重啟延遲
		}

		// 啟動輸入代理
		go proxyConsoleInput(ctx, serverStdin)

		// 同步等待伺服器行程結束
		if err := cmd.Wait(); err != nil {
			if ctx.Err() == context.Canceled {
				log.Println("伺服器行程被終止。")
			} else {
				log.Printf("伺服器行程出錯 (可能是正常關閉或崩潰): %v", err)
			}
		} else {
			log.Println("伺服器行程退出。")
		}

		serverStdin.Close() // 確保代理在行程結束後關閉

		// 檢查是否需要重啟
		if !config.Server.AutoRestart {
			log.Println("自動重啟已禁用。")
			break // 跳出 for 迴圈
		}

	RESTART_DELAY:
		log.Printf("將在 %d 秒後重啟...", config.Server.RestartDelaySeconds)
		select {
		case <-time.After(time.Duration(config.Server.RestartDelaySeconds) * time.Second):
		case <-ctx.Done():
			log.Println("終止重啟。")
			backupWg.Wait()
			return
		}
	}

	// 如果迴圈正常結束（例如 auto_restart=false），等待背景任務
	log.Println("退出管理迴圈。")
	backupWg.Wait()
}

// proxyConsoleInput 扮演輸入代理，讀取主控台輸入後決定是自己處理還是轉發給伺服器
func proxyConsoleInput(ctx context.Context, serverStdin io.WriteCloser) {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		select {
		case <-ctx.Done(): // 如果上下文被取消，停止讀取
			return
		default:
			line := scanner.Text()
			isManagerCmd := false
			for _, cmd := range config.Backup.ManagerCommands {
				if strings.ToLower(line) == cmd {
					isManagerCmd = true
					break
				}
			}

			if isManagerCmd {
				handleManagerCommand(line)
			} else {
				// 附加換行符並寫入代理
				if _, err := io.WriteString(serverStdin, line+"\n"); err != nil {
					// 寫入失敗通常意味著伺服器已關閉，可以安全退出
					return
				}
			}
		}
	}
}

// handleManagerCommand 處理內部指令
func handleManagerCommand(command string) {
	switch strings.ToLower(command) {
	case "backup":
		log.Println("收到手動備份指令...")
		go runBackup() // 異步備份
	case "exit":
		log.Println("收到退出指令，正在關閉程式...")
		// 向自身發送中斷訊號以關閉
		if p, err := os.FindProcess(os.Getpid()); err == nil {
			p.Signal(syscall.SIGINT)
		}
	}
}

// runBackupScheduler 備份trigger
func runBackupScheduler(ctx context.Context) {
	interval, err := time.ParseDuration(config.Backup.Interval)
	if err != nil {
		log.Printf("錯誤的備份時間間隔格式 '%s'，定時備份已禁用。錯誤: %v", config.Backup.Interval, err)
		return
	}

	log.Printf("定時備份已啟用，將在 %v 後開始備份。", interval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			log.Println("準備執行備份...")
			go runBackup()
		case <-ctx.Done():
			log.Println("備份排程器收到關閉通知，停止工作。")
			return
		}
	}
}

// 備份功能

// runBackup 是備份任務的入口點，帶鎖防止重疊
func runBackup() {
	if !backupMutex.TryLock() {
		log.Println("警告：上一次備份任務尚未完成，本次備份已跳過。")
		return
	}
	defer backupMutex.Unlock()

	log.Println("====================")
	log.Println("備份開始")
	startTime := time.Now()

	backupFilename := fmt.Sprintf("backup-%s.zip", startTime.Format("2006-01-02_15-04-05"))
	backupFilepath := filepath.Join(config.Backup.Destination, backupFilename)

	filesToBackup, err := collectFiles()
	if err != nil {
		log.Printf("錯誤：收集檔案失敗: %v", err)
		return
	}

	log.Printf("找到 %d 個檔案需要備份。", len(filesToBackup))
	if len(filesToBackup) == 0 {
		log.Println("沒有找到需要備份的檔案，任務結束。")
		log.Println("====================")
		return
	}

	if err := createZipArchive(backupFilepath, filesToBackup); err != nil {
		log.Printf("錯誤：建立壓縮檔失敗: %v", err)
		os.Remove(backupFilepath) // 嘗試刪除不完整的備份檔
		return
	}

	cleanupBackups()

	duration := time.Since(startTime).Round(time.Second)
	fileInfo, err := os.Stat(backupFilepath)
	if err == nil {
		fileSizeMB := float64(fileInfo.Size()) / 1024 / 1024
		log.Printf("備份成功！檔案位於: %s (大小: %.2f MB)", backupFilepath, fileSizeMB)
	} else {
		log.Printf("備份成功！檔案位於: %s", backupFilepath)
	}
	log.Printf("總耗時: %v", duration)
	log.Println("====================")
}

// createZipArchive 使用 worker pool 模式並行壓縮檔案
func createZipArchive(archivePath string, files []string) error {
	archiveFile, err := os.Create(archivePath)
	if err != nil {
		return err
	}
	defer archiveFile.Close()

	zipWriter := zip.NewWriter(archiveFile)
	defer zipWriter.Close()

	compressor := func(out io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(out, config.Backup.CompressionLevel)
	}
	zipWriter.RegisterCompressor(zip.Deflate, compressor)

	var wg sync.WaitGroup
	var writerMutex sync.Mutex
	jobs := make(chan string, len(files))

	for i := 0; i < config.Backup.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range jobs {
				if err := addFileToZip(zipWriter, path, &writerMutex); err != nil {
					log.Printf("警告：無法添加檔案到壓縮檔 %s: %v", path, err)
				}
			}
		}()
	}

	for _, file := range files {
		jobs <- file
	}
	close(jobs)

	wg.Wait()
	return nil
}

// addFileToZip 將單個檔案添加到 ZIP 存檔中，異步鎖保證安全
func addFileToZip(zipWriter *zip.Writer, filePath string, m *sync.Mutex) error {
	fileToZip, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer fileToZip.Close()

	info, err := fileToZip.Stat()
	if err != nil {
		return err
	}

	workDir := mustGetwd()
	relativePath, err := filepath.Rel(workDir, filePath)
	if err != nil {
		relativePath = filepath.Base(filePath)
	}

	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	header.Name = filepath.ToSlash(relativePath)
	header.Method = zip.Deflate

	// 鎖定寫操作
	m.Lock()
	defer m.Unlock()

	writer, err := zipWriter.CreateHeader(header)
	if err != nil {
		return err
	}

	_, err = io.Copy(writer, fileToZip)
	return err
}

// 輔助函式

// collectFiles 收集所有需要備份的檔案路徑
func collectFiles() ([]string, error) {
	var files []string
	for _, sourcePath := range config.Backup.Sources {
		err := filepath.Walk(sourcePath, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			if isExcluded(path) {
				return nil
			}
			files = append(files, path)
			return nil
		})
		if err != nil {
			if os.IsNotExist(err) {
				log.Printf("警告: 備份來源 '%s' 不存在，已跳過。", sourcePath)
				continue
			}
			return nil, fmt.Errorf("遍歷 %s 時出錯: %v", sourcePath, err)
		}
	}
	return files, nil
}

// isExcluded 檢查檔案是否應被排除
func isExcluded(path string) bool {
	workDir := mustGetwd()
	relPath, err := filepath.Rel(workDir, path)
	if err != nil {
		relPath = path
	}
	relPath = filepath.ToSlash(relPath)
	for _, pattern := range config.Backup.Exclusions {
		pattern = filepath.ToSlash(pattern)
		match, _ := filepath.Match(pattern, relPath)
		if match {
			return true
		}
	}
	return false
}

// cleanupBackups 根據策略清理舊的備份
func cleanupBackups() {
	files, err := os.ReadDir(config.Backup.Destination)
	if err != nil {
		log.Printf("錯誤：無法讀取備份目錄: %v", err)
		return
	}

	var backups []os.FileInfo
	for _, file := range files {
		if !file.IsDir() && strings.HasPrefix(file.Name(), "backup-") && strings.HasSuffix(file.Name(), ".zip") {
			if info, err := file.Info(); err == nil {
				backups = append(backups, info)
			}
		}
	}

	if len(backups) == 0 {
		return
	}

	// 按時間從舊到新排序
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].ModTime().Before(backups[j].ModTime())
	})

	// 按數量清理
	if config.Backup.RetentionCount > 0 && len(backups) > config.Backup.RetentionCount {
		toDeleteCount := len(backups) - config.Backup.RetentionCount
		log.Printf("數量超出限制 (%d > %d)，準備刪除 %d 個舊備份。", len(backups), config.Backup.RetentionCount, toDeleteCount)
		for i := 0; i < toDeleteCount; i++ {
			fileToDelete := backups[i]
			pathToDelete := filepath.Join(config.Backup.Destination, fileToDelete.Name())
			if err := os.Remove(pathToDelete); err == nil {
				log.Printf("已刪除舊備份 (數量限制): %s", fileToDelete.Name())
			}
		}
		backups = backups[toDeleteCount:]
	}

	// 按總大小清理
	if config.Backup.MaxTotalSizeGB > 0 {
		maxSizeBytes := int64(config.Backup.MaxTotalSizeGB) * 1024 * 1024 * 1024
		var totalSize int64
		for _, b := range backups {
			totalSize += b.Size()
		}
		if totalSize > maxSizeBytes {
			log.Printf("總大小超出限制 (%.2fGB > %dGB)，準備刪除舊備份。", float64(totalSize)/1e9, config.Backup.MaxTotalSizeGB)
			for totalSize > maxSizeBytes && len(backups) > 0 {
				fileToDelete := backups[0]
				pathToDelete := filepath.Join(config.Backup.Destination, fileToDelete.Name())
				if err := os.Remove(pathToDelete); err == nil {
					totalSize -= fileToDelete.Size()
					backups = backups[1:]
				} else {
					break // 如果刪除失敗，停止清理
				}
			}
		}
	}
}

func mustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		log.Fatalf("致命錯誤: 無法獲取當前工作目錄: %v", err)
	}
	return wd
}

func setupLogger() error {
	var err error
	logFile, err = os.OpenFile("manager.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	mw := io.MultiWriter(os.Stdout, logFile)
	log.SetOutput(mw)
	log.SetFlags(log.Ldate | log.Ltime)
	return nil
}

// 設定相關

// loadConfig 讀取並解析 config.toml
func loadConfig() error {
	configPath := "config.toml"
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		log.Printf("找不到 %s，正在為您生成一個範本檔案...", configPath)
		if err := createExampleConfig(configPath + ".example"); err != nil {
			return fmt.Errorf("無法建立範本設定檔: %v", err)
		}
		return fmt.Errorf("請修改 %s.example，並將其改名為 %s 後再重新啟動程式", configPath, configPath)
	}
	if _, err := toml.DecodeFile(configPath, &config); err != nil {
		return fmt.Errorf("解析設定檔失敗: %v", err)
	}
	return normalizeConfigPathsAndDefaults()
}

// normalizeConfigPathsAndDefaults 將相對路徑轉換為絕對路徑並設定預設值
func normalizeConfigPathsAndDefaults() error {
	workDir := mustGetwd()
	if config.Server.JavaPath == "" {
		return fmt.Errorf("[server.java_path] 是必要選項，不能為空")
	}
	config.Server.JavaPath = filepath.Clean(config.Server.JavaPath)
	if len(config.Backup.Sources) == 0 {
		config.Backup.Sources = []string{"world"}
	}
	for i, src := range config.Backup.Sources {
		if !filepath.IsAbs(src) {
			config.Backup.Sources[i] = filepath.Join(workDir, src)
		}
	}
	dest := config.Backup.Destination
	if dest == "" {
		dest = "backups"
	}
	if !filepath.IsAbs(dest) {
		config.Backup.Destination = filepath.Join(workDir, dest)
	}
	if err := os.MkdirAll(config.Backup.Destination, 0755); err != nil {
		return fmt.Errorf("無法建立備份目錄 %s: %v", config.Backup.Destination, err)
	}
	if config.Backup.Workers <= 0 {
		config.Backup.Workers = 4
	}
	if config.Backup.CompressionLevel < flate.NoCompression || config.Backup.CompressionLevel > flate.BestCompression {
		config.Backup.CompressionLevel = 5
	}
	if len(config.Backup.ManagerCommands) == 0 {
		config.Backup.ManagerCommands = []string{"backup", "exit"}
	}
	return nil
}

// createExampleConfig 生成設定檔範本
func createExampleConfig(path string) error {
	// GitHub URL
	url := "https://raw.githubusercontent.com/abcde89525/minecraft-server-backup-manger/main/config.toml.example"
	
	log.Printf("正在從 %s 下載設定檔範本...", url)

	// SEND HTTP GET REQUEST
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("下載設定檔範本失敗: %v", err)
	}
	defer resp.Body.Close()

	// CHECK HTTP CODE
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下載設定檔範本失敗: %s", resp.Status)
	}

	// CHECK RESPONSE CONTENT
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("讀取設定檔範本內容失敗: %v", err)
	}

	// SAVE FILE
	err = os.WriteFile(path, body, 0644)
	if err != nil {
		return fmt.Errorf("寫入設定檔範本到磁碟失敗: %v", err)
	}

	log.Printf("設定檔範本已成功儲存至: %s", path)
	return nil
}
