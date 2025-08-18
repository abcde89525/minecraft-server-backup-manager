package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/Xuanwo/go-locale"
	"github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
)

// 痊癒變數
var bundle *i18n.Bundle
var localizer *i18n.Localizer

const (
	i18nDir         = "i18n"
	githubApiURL    = "https://api.github.com/repos/abcde89525/minecraft-server-backup-manager/contents/i18n"
	defaultLanguage = "en"
)

type GithubContent struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	DownloadURL string `json:"download_url"`
}

// InitI18n 初始化 i18n
func InitI18n(languageFromConfig string) {
	bundle = i18n.NewBundle(language.English)
	bundle.RegisterUnmarshalFunc("toml", toml.Unmarshal)

	// GitHub 下載本地化文件
	if err := ensureAndDownloadLanguages(); err != nil {
		log.Printf("Warning: Failed to download or update language files, will use local files if available. Error: %v", err)
	}

	// 從 i18n 資料夾載入.toml
	files, err := os.ReadDir(i18nDir)
	if err != nil {
		log.Printf("Warning: Failed to read i18n directory '%s'. No localizations will be available. Error: %v", i18nDir, err)
	} else {
		loadedCount := 0
		for _, file := range files {
			if strings.HasSuffix(file.Name(), ".toml") {
				path := filepath.Join(i18nDir, file.Name())
				if _, err := bundle.LoadMessageFile(path); err != nil {
					log.Printf("Warning: Failed to load language file '%s': %v", path, err)
				} else {
					loadedCount++
				}
			}
		}
		log.Printf("i18n: %d language files loaded from local directory.", loadedCount)
	}

	langTag := determineLanguage(languageFromConfig, bundle)
	log.Printf("i18n: Target language resolved to '%s'", langTag)
	
	localizer = i18n.NewLocalizer(bundle, langTag, defaultLanguage)
	log.Println("i18n: Localization module initialized successfully.")
}

func I18n(messageID string) string {
	if localizer == nil {
		log.Printf("Warning: i18n module not initialized, returning message ID: %s", messageID)
		return messageID
	}

	translated, err := localizer.Localize(&i18n.LocalizeConfig{
		MessageID: messageID,
	})

	if err != nil {
		// 無翻譯 返回 messageID
		return messageID
	}
	return translated
}

// determineLanguage
func determineLanguage(languageFromConfig string, bundle *i18n.Bundle) string {
	var preferredTags []language.Tag

	// 配置文件
	if languageFromConfig != "" {
		tag, err := language.Parse(strings.Replace(languageFromConfig, "_", "-", -1))
		if err == nil {
			preferredTags = append(preferredTags, tag)
			log.Printf("i18n: Language specified from config: '%s'", tag.String())
		}
	}

	// 系統語言
	sysTag, err := locale.Detect()
	if err == nil {
		preferredTags = append(preferredTags, sysTag)
		log.Printf("i18n: Detected system language: '%s'", sysTag.String())
	} else {
		log.Printf("Warning: Could not detect system language: %v", err)
	}

	availableTags := bundle.LanguageTags()
	if len(availableTags) == 0 {
		log.Println("Warning: No language files were loaded, falling back to default language.")
		return defaultLanguage
	}

	matcher := language.NewMatcher(availableTags)
	_, index, _ := matcher.Match(preferredTags...)
	return availableTags[index].String()
}


// ensureAndDownloadLanguages 檢查 i18n 資料夾/下載翻譯文件
func ensureAndDownloadLanguages() error {
	if _, err := os.Stat(i18nDir); os.IsNotExist(err) {
		log.Printf("i18n: Directory '%s' not found, creating...", i18nDir)
		if err := os.Mkdir(i18nDir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", i18nDir, err)
		}
	}

	remoteFiles, err := getRemoteLanguageFiles()
	if err != nil {
		return fmt.Errorf("could not get remote file list: %w", err)
	}
	if len(remoteFiles) == 0 {
		log.Println("i18n: No remote language files found to download.")
		return nil
	}

	log.Printf("i18n: Found %d language files on remote. Starting download/update...", len(remoteFiles))
	for _, fileInfo := range remoteFiles {
		if err := downloadFile(fileInfo.Name, fileInfo.DownloadURL); err != nil {
			log.Printf("Warning: Failed to download language file '%s': %v", fileInfo.Name, err)
		}
	}
	log.Println("i18n: Language file check completed.")
	return nil
}

// getRemoteLanguageFiles GitHub API 查找文件列表
func getRemoteLanguageFiles() ([]GithubContent, error) {
	resp, err := http.Get(githubApiURL)
	if err != nil {
		return nil, fmt.Errorf("http get failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bad status from GitHub API: %s", resp.Status)
	}

	var contents []GithubContent
	if err := json.NewDecoder(resp.Body).Decode(&contents); err != nil {
		return nil, fmt.Errorf("failed to decode json response: %w", err)
	}

	var tomlFiles []GithubContent
	for _, c := range contents {
		if c.Type == "file" && strings.HasSuffix(c.Name, ".toml") {
			tomlFiles = append(tomlFiles, c)
		}
	}
	return tomlFiles, nil
}

// downloadFile
func downloadFile(filename, url string) error {
	localPath := filepath.Join(i18nDir, filename)

	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("http get failed for %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad status: %s", resp.Status)
	}

	out, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("failed to create local file: %w", err)
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return fmt.Errorf("failed to write to local file: %w", err)
	}
	return nil
}