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

從 GitHub Raw 取得的安裝腳本會透過 GitHub Releases API 取得最新正式版的 tag，依 CPU 架構下載對應的 `webufw-linux-amd64` 或 `webufw-linux-arm64`，比對同一版本的 `SHA256SUMS` 後呼叫 sudo 安裝。腳本雖從 `main` 取得，但執行檔只從正式 Release 下載，不會在安裝主機上建置，也不要求使用者安裝 Go。指定版本時可同時固定安裝腳本和執行檔（將 `vX.Y.Z` 換成已發布的版本）：

```bash
curl -fsSL https://raw.githubusercontent.com/HSBearBig/WebUFW/vX.Y.Z/install.sh | bash -s -- --version vX.Y.Z
```

執行檔下載會顯示進度條，不設總下載時限；連線逾時為 15 秒，傳輸速率連續 60 秒低於 1 byte/s 時中止。逾時與暫時性 HTTP 錯誤最多自動重試 3 次，完成後仍會驗證 SHA256。若要用修正後的安裝器下載舊版本，可使用 `main/install.sh` 並指定 `--version v0.0.2`。

可在管線後加 `bash -s -- --dry-run` 預覽，或加 `--no-start` 只安裝檔案。安裝器會建立服務帳號、安裝至 `/usr/local/libexec/webufw/webufw`，並啟用、啟動 systemd 服務。系統不提供 `webufw` 指令，服務統一透過 `systemctl` 管理；升級會移除舊版 `/usr/local/bin/webufw`。

首次啟動會自動產生 `admin` 的初始密碼，儲存 bcrypt 雜湊並寫入服務日誌。安裝腳本等到 HTTP 監聽成功後顯示初始密碼；開啟 `http://127.0.0.1:8088` 即可登入，之後可在設定頁修改密碼。關閉安裝終端機後，可查閱：

```bash
sudo systemctl status webufw --no-pager -l
sudo journalctl -u webufw --no-pager --grep='WebUFW 初始密碼'
```

`status` 僅顯示最近的日誌；若初始密碼已不在最近紀錄中，請用 `journalctl` 查閱。密碼只在首次產生時記錄，重啟與重新安裝不會重設密碼。安裝器不會安裝 UFW、Docker 或 ufw-docker；登入後在「設定 → 環境檢查」查看缺項，不使用 Docker 可以略過 Docker 與 ufw-docker。

推送版本 tag 後的自動發版流程、本機打包方式與檢查項目見 [發版說明](docs/releasing.md)。

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
預設只監聽本機；可在設定中改為 `0.0.0.0:8088` 或指定的本機 IP:連接埠，執行 `sudo systemctl restart webufw` 後以 `http://<主機 IP>:8088` 存取。設定頁會分別顯示儲存位址與目前生效位址。修改監聽位址不會自動開放 UFW 或上游防火牆，連線檢查步驟見部署文件。區網連線目前是 HTTP，請自行限制可連入的網段；HTTPS 尚未提供。本專案採 GPL-3.0；下載的 ufw-docker 腳本保留其原始授權與來源。
