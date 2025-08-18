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

// 全域變數 結構定義
var (
	config      Config
	backupMutex sync.Mutex
	logFile     *os.File
)

// config.toml
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

// set work dir
func init() {
	exePath, err := os.Executable()
	if err != nil {
		log.Fatalf("ERROR: Unable to get file path: %v", err)
	}
	exeDir := filepath.Dir(exePath)

	isGoRun := strings.Contains(exeDir, "go-build") || strings.HasPrefix(exeDir, os.TempDir())
	if !isGoRun {
		if err := os.Chdir(exeDir); err != nil {
			log.Fatalf("ERROR: Unable switch to program root directory %s: %v", exeDir, err)
		}
	}
}

// main
func main() {
	exec.Command("cmd", "/c", "chcp 65001 > nul").Run()
	
	if err := loadConfig(); err != nil {
		fmt.Printf("ERROR: %v\n", err)
		fmt.Println("Press Enter to exit...")
		bufio.NewReader(os.Stdin).ReadBytes('\n')
		os.Exit(1)
	}

	InitI18n(config.General.Language)

	if err := setupLogger(); err != nil {
		log.Fatalf(I18n("error_setting_log"), err)
	}
	defer logFile.Close()

	if config.General.WindowTitle != "" {
		exec.Command("cmd", "/c", "title", config.General.WindowTitle).Run()
	}

	log.Println("Minecraft Server Manager v1.9 by yuni_sakana")
	log.Printf(I18n("backup_directory_is"), config.Backup.Destination)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan
		log.Println(I18n("manager_shutdown"))
		cancel()
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		runServerManager(ctx)
	}()

	wg.Wait()
}

// runServerManager
func runServerManager(ctx context.Context) {
	workDir := mustGetwd()

	if config.Backup.Enabled {
		runBackup()
	}

	var backupWg sync.WaitGroup
	if config.Backup.Enabled {
		backupWg.Add(1)
		go func() {
			defer backupWg.Done()
			runBackupScheduler(ctx)
		}()
	}

	for {
		select {
		case <-ctx.Done():
			backupWg.Wait()
			return
		default:
		}

		log.Println(I18n("server_starting"))

		allArgs := []string{}
		allArgs = append(allArgs, config.Server.JvmArgs...)
		allArgs = append(allArgs, config.Server.ServerArgs...)

		cmd := exec.CommandContext(ctx, config.Server.JavaPath, allArgs...)
		cmd.Dir = workDir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		serverStdin, err := cmd.StdinPipe()
		if err != nil {
		}

		if err := cmd.Start(); err != nil {
			log.Printf(I18n("error_start_server_failed"), err)
			if !config.Server.AutoRestart {
				break
			}
			goto RESTART_DELAY
		}

		go proxyConsoleInput(ctx, serverStdin)

		if err := cmd.Wait(); err != nil {
			if ctx.Err() == context.Canceled {
				log.Println(I18n("server_process_terminated"))
			} else {
				log.Printf(I18n("server_process_error"), err)
			}
		} else {
			log.Println(I18n("server_process_exited"))
		}

		serverStdin.Close()

		if !config.Server.AutoRestart {
			log.Println(I18n("server_auto_restart_disabled"))
			break
		}

	RESTART_DELAY:
		log.Printf(I18n("server_restarting"), config.Server.RestartDelaySeconds)
		select {
		case <-time.After(time.Duration(config.Server.RestartDelaySeconds) * time.Second):
		case <-ctx.Done():
			log.Println(I18n("server_restart_terminated"))
			backupWg.Wait()
			return
		}
	}
	
	backupWg.Wait()
}

// proxyConsoleInput
func proxyConsoleInput(ctx context.Context, serverStdin io.WriteCloser) {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
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
				if _, err := io.WriteString(serverStdin, line+"\n"); err != nil {
					return
				}
			}
		}
	}
}

// handleManagerCommand
func handleManagerCommand(command string) {
	switch strings.ToLower(command) {
	case "backup":
		go runBackup()
	case "exit":
		log.Println(I18n("manager_exit_command_received"))
		if p, err := os.FindProcess(os.Getpid()); err == nil {
			p.Signal(syscall.SIGINT)
		}
	}
}

// runBackupScheduler 備份trigger
func runBackupScheduler(ctx context.Context) {
	interval, err := time.ParseDuration(config.Backup.Interval)
	if err != nil {
		log.Printf(I18n("config_backup_interval_format_invalid"), config.Backup.Interval, err)
		return
	}

	log.Printf(I18n("backup_scheduled_enabled"), interval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			go runBackup()
		case <-ctx.Done():
			return
		}
	}
}

// runBackup 
func runBackup() {
	if !backupMutex.TryLock() {
		log.Println(I18n("backup_skipped_previous_unfinished"))
		return
	}
	defer backupMutex.Unlock()

	log.Println("====================")
	log.Println(I18n("backup_started"))
	startTime := time.Now()

	backupFilename := fmt.Sprintf("backup-%s.zip", startTime.Format("2006-01-02_15-04-05"))
	backupFilepath := filepath.Join(config.Backup.Destination, backupFilename)

	filesToBackup, err := collectFiles()
	if err != nil {
		log.Printf(I18n("backup_collect_files_failed"), err)
		return
	}

	log.Printf(I18n("backup_found_files_to_backup"), len(filesToBackup))
	if len(filesToBackup) == 0 {
		log.Println(I18n("backup_no_files_found"))
		log.Println("====================")
		return
	}

	if err := createZipArchive(backupFilepath, filesToBackup); err != nil {
		log.Printf(I18n("backup_create_archive_failed"), err)
		os.Remove(backupFilepath)
		return
	}

	cleanupBackups()

	duration := time.Since(startTime).Round(time.Second)
	fileInfo, err := os.Stat(backupFilepath)
	if err == nil {
		fileSizeMB := float64(fileInfo.Size()) / 1024 / 1024
		log.Printf(I18n("backup_successful_size"), backupFilepath, fileSizeMB)
	} else {
		log.Printf(I18n("backup_successful"), backupFilepath)
	}
	log.Printf(I18n("backup_total_time"), duration)
	log.Println("====================")
}

// createZipArchive
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
					log.Printf(I18n("backup_add_file_to_archive_failed"), path, err)
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

// addFileToZip
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

	m.Lock()
	defer m.Unlock()

	writer, err := zipWriter.CreateHeader(header)
	if err != nil {
		return err
	}

	_, err = io.Copy(writer, fileToZip)
	return err
}

// collectFiles
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
				log.Printf(I18n("backup_backup_source_not_found"), sourcePath)
				continue
			}
			return nil, fmt.Errorf(I18n("backup_traversing_path_error"), sourcePath, err)
		}
	}
	return files, nil
}

// isExcluded
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

// cleanupBackups
func cleanupBackups() {
	files, err := os.ReadDir(config.Backup.Destination)
	if err != nil {
		log.Printf(I18n("backup_dir_get_failed"), err)
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

	sort.Slice(backups, func(i, j int) bool {
		return backups[i].ModTime().Before(backups[j].ModTime())
	})

	if config.Backup.RetentionCount > 0 && len(backups) > config.Backup.RetentionCount {
		toDeleteCount := len(backups) - config.Backup.RetentionCount
		log.Printf(I18n("backup_pruning_by_count_limit"), len(backups), config.Backup.RetentionCount, toDeleteCount)
		for i := 0; i < toDeleteCount; i++ {
			fileToDelete := backups[i]
			pathToDelete := filepath.Join(config.Backup.Destination, fileToDelete.Name())
			if err := os.Remove(pathToDelete); err == nil {
				log.Printf(I18n("backup_pruned_by_count_limit"), fileToDelete.Name())
			}
		}
		backups = backups[toDeleteCount:]
	}

	if config.Backup.MaxTotalSizeGB > 0 {
		maxSizeBytes := int64(config.Backup.MaxTotalSizeGB) * 1024 * 1024 * 1024
		var totalSize int64
		for _, b := range backups {
			totalSize += b.Size()
		}
		if totalSize > maxSizeBytes {
			log.Printf(I18n("backup_pruning_by_size_limit"), float64(totalSize)/1e9, config.Backup.MaxTotalSizeGB)
			for totalSize > maxSizeBytes && len(backups) > 0 {
				fileToDelete := backups[0]
				pathToDelete := filepath.Join(config.Backup.Destination, fileToDelete.Name())
				if err := os.Remove(pathToDelete); err == nil {
					totalSize -= fileToDelete.Size()
					backups = backups[1:]
				} else {
					break
				}
			}
		}
	}
}

// mustGetwd
func mustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		log.Fatalf(I18n("error_get_working_dir"), err)
	}
	return wd
}

// setupLogger
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

// load config.toml
func loadConfig() error {
	configPath := "config.toml"
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		log.Printf(I18n("config_not_found_generating_template"), configPath)
		if err := createExampleConfig(configPath + ".example"); err != nil {
			return fmt.Errorf(I18n("config_create_template_failed"), err)
		}
		return fmt.Errorf(I18n("config_user_action_required"), configPath, configPath)
	}
	if _, err := toml.DecodeFile(configPath, &config); err != nil {
		return fmt.Errorf(I18n("config_parse_failed"), err)
	}
	return normalizeConfigPathsAndDefaults()
}

// normalizeConfigPathsAndDefaults
func normalizeConfigPathsAndDefaults() error {
	workDir := mustGetwd()
	
	// Java Path
	if config.Server.JavaPath == "" {
		return fmt.Errorf(I18n("config_java_path_required"))
	}
	config.Server.JavaPath = filepath.Clean(config.Server.JavaPath)
	
	// Backup Sources
	if len(config.Backup.Sources) == 0 {
		config.Backup.Sources = []string{"world"}
	}
	for i, src := range config.Backup.Sources {
		if !filepath.IsAbs(src) {
			config.Backup.Sources[i] = filepath.Join(workDir, src)
		}
	}
	
	// Backup Path
	dest := config.Backup.Destination
	if dest == "" {
		dest = "backups"
	}
	if !filepath.IsAbs(dest) {
		config.Backup.Destination = filepath.Join(workDir, dest)
	}
	if err := os.MkdirAll(config.Backup.Destination, 0755); err != nil {
		return fmt.Errorf(I18n("backup_dir_create_failed"), config.Backup.Destination, err)
	}
	
	// Other defaults
	if config.Backup.Workers <= 0 {
		config.Backup.Workers = 4
	}
	if config.Backup.CompressionLevel < flate.NoCompression || config.Backup.CompressionLevel > flate.BestCompression {
		config.Backup.CompressionLevel = 5
	}
	if len(config.Backup.ManagerCommands) == 0 {
		config.Backup.ManagerCommands = []string{"backup", "exit"}
	}
	
	// Discord defaults
	if len(config.Discord.ForwardEvents) == 0 {
		config.Discord.ForwardEvents = []string{"chat", "join", "leave"}
	}
	if config.Discord.IngameFormat == "" {
		config.Discord.IngameFormat = "[Discord] <{{ .Username }}> {{ .Message }}"
	}
	return nil
}

// createExampleConfig
func createExampleConfig(path string) error {
	// GitHub URL
	url := "https://raw.githubusercontent.com/abcde89525/minecraft-server-backup-manger/main/config.toml.example"
	
	log.Printf(I18n("config_downloading_template"), url)

	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf(I18n("config_download_template_failed"), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf(I18n("config_download_template_failed"), resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf(I18n("config_template_read_content_failed"), err)
	}

	err = os.WriteFile(path, body, 0644)
	if err != nil {
		return fmt.Errorf(I18n("config_template_write_failed"), err)
	}

	log.Printf(I18n("config_template_saved_successfully"), path)
	return nil
}