# WebUFW

以單一 Go 執行檔管理 Linux 主機的 UFW 規則，並檢視 Docker 容器與轉送規則。前端使用繁體中文、素色表格與本機靜態資源，沒有 Node.js 或資料庫服務的執行依賴。

## 建置與測試

需要 Linux、Go 1.26 以上。

```bash
make build
make test
go test -race ./...
go vet ./...
```

`bin/webufw` 包含 HTML、CSS、JavaScript，**不包含 ufw-docker 腳本或修補檔**。啟動 WebUFW 不需先安裝 UFW、Docker 或 ufw-docker。Go 程式使用 `CGO_ENABLED=0` 建置；實際主機規則仍需 UFW，Docker 狀態查詢需 Docker Engine。不同 CPU 架構需各自建置。

## 安裝與執行

發布第一個 GitHub Release 後，Linux 使用者可直接安裝最新正式版：

```bash
curl -fsSL https://raw.githubusercontent.com/HSBearBig/WebUFW/main/install.sh | bash
```

從 GitHub Raw 取得的安裝腳本會透過 GitHub Releases API 取得最新正式版的 tag，依 CPU 架構下載對應的 `webufw-linux-amd64` 或 `webufw-linux-arm64`，比對同一版本的 `SHA256SUMS` 後呼叫 sudo 安裝。腳本雖從 `main` 取得，但執行檔只從正式 Release 下載，不會在安裝主機上建置，也不要求使用者安裝 Go。指定版本時可同時固定安裝腳本和執行檔：

```bash
curl -fsSL https://raw.githubusercontent.com/HSBearBig/WebUFW/v0.1.0/install.sh | bash -s -- --version v0.1.0
```

可在管線後加 `bash -s -- --dry-run` 預覽，或加 `--no-start` 只安裝檔案而不啟動服務。安裝器只負責 WebUFW；UFW、Docker 與 ufw-docker 的狀態在網頁設定頁顯示，ufw-docker 來源也在該頁選擇。**目前 GitHub 尚未發布 Release，所以上述管線會在第一版發布後才可使用。**手動建置開發版仍可使用：

```bash
make build
sudo ./bin/webufw run       # 前景測試
sudo ./bin/webufw install   # 安裝並啟動背景服務
```

推送版本 tag 後的自動發版流程、本機打包方式與檢查項目見 [發版說明](docs/releasing.md)。
首次 `run` 會在終端機輸出一次性設定碼；開啟 `http://127.0.0.1:8088` 建立管理者密碼。網頁「設定 → 環境檢查」會顯示 UFW、Docker 與 ufw-docker 缺項。不使用 Docker 可以略過 Docker 與 ufw-docker。`install` 預設安裝、啟用並啟動 WebUFW 的 systemd 服務；重新安裝會重啟服務。若只想先安裝檔案，使用 `install --no-start`。兩種安裝方式都不會安裝 UFW、Docker 或 ufw-docker；若尚未建立密碼，首次啟動的設定碼可用 `sudo journalctl -u webufw -n 30 --no-pager` 查看。

WebUFW 會先偵測 `/usr/local/bin/ufw-docker` 或 `/usr/bin/ufw-docker`；設定頁顯示實際路徑、來源、版本與 SHA256，不覆蓋既有腳本。也可在網頁選擇 HSBearBig fork 或 chaifeng 原版，預覽最新 commit 與 SHA256 後安裝到 `/var/lib/webufw/ufw-docker`。已驗證的 HSBearBig 版本可管理 Docker 規則；WebUFW 透過受鎖保護的 UFW 指令寫入相同註解格式，不執行腳本的廣泛刪除命令。未驗證的版本保留唯讀。

主程序保留 root 權限；網頁子程序優先使用 `webufw` 一般使用者，未安裝服務帳號時使用 `nobody`。兩者透過 `/run/webufw/agent.sock` 溝通，Agent 只接受固定操作。systemd 管理程序組，已確認的防火牆規則不隨 WebUFW 停止而清除。

## 功能

- 淺色／深色／跟隨系統，外觀偏好保留在本機瀏覽器。
- 主機入站／出站 allow、deny、reject，IP/CIDR、TCP/UDP、埠與範圍、註解。
- 檢視 Docker 容器、bridge 網路、服務埠、既有轉送規則與容器 IP 變更。
- 已驗證 HSBearBig 腳本可新增、縮限、刪除與同步 Docker 規則；未知版本保留唯讀。
- 每次變更先預覽；UFW 啟停、刪改及新增拒絕等操作需 60 秒內確認。
- CLI 與網頁共用 UFW 的實際規則與註解；開啟頁面時每 3 秒檢查更新。
- 原生 UFW 鎖、版本衝突檢查、持久化操作回復紀錄。
- 單一管理者、bcrypt、登入限速、HttpOnly/SameSite 工作階段、CSRF 與 Origin/Host 檢查。
- 最多 200 筆單次核心日誌查詢、最多 1,000 筆管理操作紀錄。

## 範圍與限制

第一版針對 Ubuntu 24.04、一般 Linux Docker Engine 的 bridge/NAT 網路。Swarm、rootless、Docker Desktop、Docker 原生 nftables 後端與特殊 gateway 模式保留唯讀；`iptables-nft` 相容模式可以使用。無法完整辨識的 UFW 規則、應用程式 profile、介面條件、來源埠及重複規則保留唯讀。

新增來源規則不代表其他來源一定被封鎖。ufw-docker 的信任網段、其他廣泛轉送規則，以及既有 conntrack 連線會影響結果。介面會呈現整合信任網段與預覽提示。

逾時或重新啟動時，Agent 只反向處理自己的變更，不還原整份 `/etc/ufw`。若 CLI 或外部程序造成衝突，或程序在指令執行到一半時被強制終止而無法確認結果，會保留紀錄並停止自動回復，等待人工核對。詳見部署文件。

- [部署與回復](docs/deployment.md)
- [架構與 API](docs/architecture.md)
- [驗證結果](docs/validation.md)
預設只監聽本機；可在設定中改為 `0.0.0.0:8088` 或指定的本機 IP:連接埠，重啟後以 `http://<主機 IP>:8088` 存取。區網連線目前是 HTTP，請自行限制可連入的網段；HTTPS 尚未提供。本專案採 GPL-3.0；下載的 ufw-docker 腳本保留其原始授權與來源。
