# minecraft-server-backup-manager

一個 Golang 編寫的 主要是自用的 Minecraft伺服器啟動器/異步備份工具，
用於取代傳統 功能單一的 .bat .sh 啟動腳本。

單一執行檔可以管理Vanllia, NeoForge, Forge, Fabric伺服器。
可以在沒安裝備份模組時高效的協助備份，或是當作第二層保險。

## 🖥️功能特點

* **定時自動備份**: 可設定固定的時間間隔(例如每 6 小時），在伺服器運行的同時於背景自動執行備份，不影響伺服器運作。
* **備份清理**: 可同時設定兩種清理模式:
  * **保留數量**: 只保留最近 N 個備份。
  * **磁碟空間**: 限制備份資料夾的總大小，自動刪除最舊的備份以釋放空間。
* **多線程使用**: 基於現代多核心CPU優勢，可設定並行壓縮備份檔案，縮短了大型世界地圖的備份時間。
* **崩潰自動重啟**: 啟動並持續監控伺服器進程。
* **簡單高效**: 所有設定(Java 路徑、記憶體分配、備份策略等)都集中在一個高可讀的 `config.toml` 檔案。
* **跨平台&單一執行檔**: 用Golang編寫，可以編譯成Windows,Linux的單一執行檔，並且不需要外部依賴，簡單易用。

## 🚀開始使用

### 環境
1. **Java**: 你必須安裝了適用於你伺服器版本的 Java 執行環境。
2. **Minecraft 伺服器檔案**: 你需要擁有一個可以正常運行的 Minecraft 伺服器。
**可選**: [Golang編譯環境](https://go.dev/dl/)(1.20或以上)

### 安裝與設定
* 前往本專案的 [Releases]([https://github.com/YOUR_USERNAME/YOUR_REPOSITORY/releases](https://github.com/abcde89525/minecraft-server-backup-manger/releases)) 頁面，下載對應你作業系統的最新版本的執行檔。
* 或從原始碼編譯:
  1. 確保你已安裝 Go 1.20或以上
  2. 克隆本專案
  ```bash
    git clone https://github.com/abcde89525/minecraft-server-backup-manger.git
    cd minecraft-server-backup-manger
  ```
  3. 編譯
  ```bash
    go build
  ```

### 首次執行
1. 將下載或編譯好的執行檔 **放到你的 Minecraft 伺服器根目錄下** (也就是包含 `world` 資料夾、`plugins` 資料夾的地方)。
2. 直接執行它，並自動為你生成一個 `config.toml.example` 檔案。
3. 將 `config.toml.example` **重新命名**為 `config.toml`。
4. 打開 `config.toml` 檔案，根據下面的說明，修改成你自己的設定。
5. 儲存 `config.toml` 後，再次執行 `MinecraftManager.exe` 就會在管理器的監控下啟動了! (大概)

## ⚙️ 設定檔說明 (`config.toml`)
*  `window_title` = "My Minecraft Server"
### `[server]` 區塊 - 伺服器啟動設定
*  `java_path`: **(必要)** Java 可執行檔的絕對路徑。由於不同伺服器版本可能需要不同 Java 版本，請填寫絕對路徑。
    - (Windows 例): `java_path = 'C:\Program Files\Zulu\zulu-21\bin\java.exe'`
    - (Linux 例): `java_path = '/usr/lib/jvm/java-17-openjdk/bin/java'`
*  `jvm_args`: **(必要)** Java 虛擬機的啟動參數。
    - **對於模組化伺服器 (NeoForge/Forge/Fabric)**: 通常包含記憶體設定和 `@argfile` 參數。**不要**包含 `-jar`。
        ```toml
        jvm_args = ["-Xmx6G", "@user_jvm_args.txt", "@libraries/.../win_args.txt"]
        ```
    - **對於原版伺服器 (Vanilla/Paper)**: 這裡需要包含記憶體設定、`-jar` 標誌和 JAR 檔案的名稱。
        ```toml
        jvm_args = ["-Xmx4G", "-jar", "paper-1.20.1.jar"]
        ```
*   `server_args`: 附加在最後的伺服器參數，最常見的就是 `nogui`。
    ```toml
    server_args = ["nogui"]
    ```
*   `auto_restart`: 是否在伺服器關閉或崩潰後自動重啟，布林值。
*   `restart_delay_seconds`: 自動重啟前的等待秒數。
### `[backup]` 區塊 - 備份設定
*   `enabled`: 是否啟用備份功能(包括啟動時備份和定時備份)。
*   `interval`: 自動備份的時間間隔。支援  `m` (分鐘), `h` (小時), `d` (天)。例如 `"30m"`, `"12h"`, `"1d"`。
*   `manager_commands`: 管理器專用的內部指令。當你在主控台輸入這些指令時，管理器會自己處理，而不會轉發給伺服器。
*   `compression_level`: ZIP 壓縮等級，範圍 `0` - `9`。`0`=不壓縮，`1`=最快，`9`=最高壓縮。推薦 `5` 或 `6`。
*   `workers`: 執行壓縮任務的並行執行緒數。推薦設定為你 CPU 核心數的一半。
*   `sources`: 需要備份的檔案或資料夾列表。**支援相對路徑和絕對路徑**。相對路徑是相對於本執行檔的位置。
    ```toml
    sources = ["world", "world_nether", "plugins"]
    ```
*   `exclusions`: 備份時要忽略的檔案或資料夾列表。支援 `*` 萬用字元。
    ```toml
    exclusions = ["logs/*", "cache/*", "plugins/dynmap/*"]
    ```
*   `destination`: 備份檔案的儲存位置。**支援相對路徑和絕對路徑**。如果留空或設定為相對路徑，它會被建立在執行檔旁邊。
*   `retention_count`: 保留最近的備份數量。設為 `0` 表示不以此為限制。
*   `max_total_size_gb`: 備份資料夾允許的最大總大小 (GB)。設為 `0` 表示不以此為限制。

## 🤝 貢獻

歡迎任何形式的貢獻！如果你發現了 BUG 或有新的功能建議，請先提出一個 [Issue](https://github.com/abcde89525/minecraft-server-backup-manger/issues)。如果你希望提交程式碼，請 Fork 本專案並提交一個 Pull Request。


```
MIT License

Copyright (c) 2025 sakana_yuni

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

