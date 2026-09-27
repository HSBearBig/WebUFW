# 部署與維護

## 安裝

WebUFW 可以先獨立啟動；UFW 和 Docker 都是功能選項，不是管理介面的啟動條件。WebUFW 不透過容器部署。

```bash
curl -fsSL https://raw.githubusercontent.com/HSBearBig/WebUFW/main/install.sh | bash
# 第一個 Release 發布前，可先從原始碼建置：
make build
sudo ./bin/webufw install
```

第一次啟動時，終端機會輸出一次性設定碼。於 `http://127.0.0.1:8088` 設定管理者密碼，接著到「設定 → 環境檢查」查看 UFW、Docker Engine 與 ufw-docker。Ubuntu 主機可自行安裝 `ufw`；不使用 Docker 的主機不需安裝 Docker 或 ufw-docker。服務帳號和 systemd 為選用：

```bash
sudo ./bin/webufw install
sudo journalctl -u webufw -n 30 --no-pager  # 查看首次設定碼
```

GitHub Raw 的 `install.sh` 先向 GitHub Releases API 解析最新正式版，再下載對應架構的 binary 與 `SHA256SUMS`，校驗後透過 sudo 呼叫 Go 程式內的 `install`。腳本只安裝 WebUFW，不偵測或安裝 UFW、Docker、ufw-docker；啟動後於網頁設定頁查看依賴狀態，並按需要選擇 ufw-docker 來源。預設啟用並啟動 systemd 服務；重新安裝會重啟服務。若只要複製程式與服務檔，使用 `./install.sh --no-start`。直接在前景使用 `run` 時，先停止該程序再執行會啟動 systemd 的 `install`，避免兩者爭用監聽位址。

相關路徑（WebUFW 私有的 ufw-docker 與 source.json 只在使用者於網頁確認後建立）：

| 路徑 | 用途 |
|---|---|
| `/usr/local/bin/webufw` | 單一執行檔 |
| `/var/lib/webufw/ufw-docker` | 僅在網頁確認後才安裝的選用腳本 |
| `/var/lib/webufw/source.json` | 所選來源、commit、SHA256 與 URL |
| `/etc/webufw/config.json` | 首次網頁設定後建立；root:root / 0600，監聽設定及 bcrypt 雜湊 |
| `/var/lib/webufw/` | root:root / 0700，待確認操作及輪替紀錄 |
| `/run/webufw/agent.sock` | root:webufw / 0660，限制本機 Agent RPC |
| `/etc/systemd/system/webufw.service` | root Agent 啟動一般使用者 web 子程序 |

無互動安裝可選用 `--password-file /root/webufw-password`，檔案需 0600，內容是一行初始密碼；不指定時於網頁首次設定。既有設定與密碼不會因重新安裝而重設。安裝器不下載 ufw-docker 腳本、不另行開放防火牆管理埠，也不啟用 UFW。

WebUFW 會偵測系統既有的 `/usr/local/bin/ufw-docker` 或 `/usr/bin/ufw-docker`，顯示其路徑與 SHA256，不修改或覆蓋它。也可選擇 HSBearBig fork 與 chaifeng 原版，核對最新 commit 和 SHA256 後安裝到私有資料目錄；更換私有腳本前會備份。私有腳本優先於系統腳本。已驗證 HSBearBig 版本支援 WebUFW Docker 規則寫入；未知版本僅供檢視。

## UFW 與 Docker 整合

可先使用主機規則功能。Docker 規則寫入還需要已驗證的 ufw-docker 腳本、Docker bridge 網路與 ufw-docker 的宿主機整合；執行中的 `DOCKER-USER` 必須跳到 `ufw-user-forward`（IPv6 為 `ufw6-user-forward`）。WebUFW 在 UFW 鎖下逐條寫入、刪除並回復規則，使用與 HSBearBig CLI 相同的註解格式。

在具有主控台或其他復原途徑的維護時段，先確保 SSH／管理網段已允許，再依實際網路設定 ufw-docker。以下命令會改變防火牆，應先閱讀預覽與既有規則：

```bash
sudo /usr/local/bin/ufw-docker check --docker-subnets
sudo /usr/local/bin/ufw-docker install --docker-subnets
sudo ufw reload
```

`--docker-subnets` 會以 Docker 網段產生整合。請檢查信任來源的 RETURN 規則是否符合你的隔離需求；來源 allow 不會自動排除其他信任來源。IPv6 必須同時檢查 after6.rules 與執行中的 ip6tables chains。

多網路容器的發布埠不一定轉送至你選擇的網路。WebUFW 只放行所選網路的目的 IP，不會替 Docker 改路由或自動開放其他網路。可用 `sudo iptables -t nat -S DOCKER` 與 `sudo ip6tables -t nat -S DOCKER` 核對 DNAT 目的，再選擇對應網路；Docker 的 gateway priority 也會影響選擇。預覽會對多網路容器顯示提醒。

## 執行與停止

```bash
sudo webufw run
sudo systemctl start webufw  # 已安裝服務但目前未執行時
sudo systemctl status webufw
sudo journalctl -u webufw -n 100 --no-pager
sudo systemctl stop webufw
```

前景與 systemd 模式不能同時執行。停止時會先處理尚未確認的變更；已確認規則保持原狀。根程序失敗時 systemd 會重新啟動並讀取回復紀錄。網頁子程序失敗會讓根程序結束並由 systemd 重啟整組程序。

## 網頁存取

預設監聽 `127.0.0.1:8088`。設定頁可改成 `0.0.0.0:8088`、`[::]:8088` 或指定的本機 IP:連接埠；儲存後需 `sudo systemctl restart webufw`。區網使用者以 `http://<主機 IP>:8088` 存取。首次設定碼只能由本機連線提交。區網連線目前為明文 HTTP，請自行限制可連入的網段；HTTPS 尚未提供。

## 衝突與人工回復

遇到外部 CLI 變更、寫入期間強制終止、或回復指令失敗，WebUFW 不會整份覆蓋設定：

1. 使用主控台／SSH 檢查 `sudo ufw status numbered`、`sudo ufw show added` 與 `/var/lib/webufw/pending.json`。
2. 依紀錄確認必要規則及順序；只修正本次受影響規則。
3. 確認當前狀態正確後，停止 WebUFW。
4. 執行 `sudo webufw resolve --acknowledge-current-state`，將 pending.json 保留為 resolved-時間.json。
5. 重新啟動 WebUFW。此命令不修改防火牆。

請勿在尚未核對時直接刪除 pending.json。

## 更新

重新執行最新 Release 的管線安裝指令；安裝器會下載並校驗新 binary，然後重啟服務。更新不會移除 UFW 規則、既有密碼或所選腳本。已驗證 HSBearBig 版本可供 WebUFW 管理 Docker 規則；升級成未知 SHA256 後會先退回唯讀，待重新驗證。
