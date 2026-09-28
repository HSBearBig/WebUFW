# 部署與維護

## 安裝

WebUFW 可以先獨立啟動；UFW 和 Docker 都是功能選項，不是管理介面的啟動條件。WebUFW 不透過容器部署。

```bash
curl -fsSL https://raw.githubusercontent.com/HSBearBig/WebUFW/main/install.sh | bash
```

GitHub Raw 的 `install.sh` 先向 GitHub Releases API 解析最新正式版，再下載對應架構的 binary 與 `SHA256SUMS`。校驗後由腳本建立服務帳號並安裝 systemd 服務；一般使用者只在安裝階段呼叫 sudo，也可由 root 直接執行。系統不再安裝 WebUFW 指令入口。

首次啟動會產生 `admin` 的隨機初始密碼，只有 bcrypt 雜湊寫入設定檔。明文初始密碼記錄在服務日誌，安裝器會等待 HTTP 監聽成功並顯示當次啟動日誌。在 `http://127.0.0.1:8088` 直接登入後，可於設定頁修改密碼。關閉終端機後可以查閱：

```bash
sudo systemctl status webufw --no-pager -l
sudo journalctl -u webufw --no-pager --grep='WebUFW 初始密碼'
```

`status` 只顯示最近日誌，初始密碼不在其中時請用 `journalctl`。重啟或升級不會再次產生或列印密碼；日誌中的初始密碼在修改密碼後就不能登入。初始密碼只保留於主機的 journal 保存期間。

腳本只安裝 WebUFW，不安裝 UFW、Docker 或 ufw-docker；依賴狀態在網頁設定頁查看。預設啟用並啟動 systemd 服務；重新安裝會重啟服務。若只要安裝檔案，使用 `bash -s -- --no-start`，之後以 `sudo systemctl enable --now webufw` 首次啟動並產生密碼。

相關路徑（WebUFW 私有的 ufw-docker 與 source.json 只在使用者於網頁確認後建立）：

| 路徑 | 用途 |
|---|---|
| `/usr/local/libexec/webufw/webufw` | systemd 使用的服務執行檔 |
| `/var/lib/webufw/ufw-docker` | 僅在網頁確認後才安裝的選用腳本 |
| `/var/lib/webufw/source.json` | 所選來源、commit、SHA256 與 URL |
| `/etc/webufw/config.json` | 首次啟動自動建立；root:root / 0600，監聽設定及 bcrypt 雜湊 |
| `/var/lib/webufw/` | root:root / 0700，待確認操作及輪替紀錄 |
| `/run/webufw/agent.sock` | root:webufw / 0660，限制本機 Agent RPC |
| `/etc/systemd/system/webufw.service` | root Agent 啟動一般使用者 web 子程序 |

既有設定與密碼不會因重新安裝而重設，升級會移除舊版 `/usr/local/bin/webufw`。安裝器不下載 ufw-docker 腳本、不另行開放防火牆管理埠，也不啟用 UFW。

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
sudo systemctl start webufw  # 已安裝服務但目前未執行時
sudo systemctl status webufw
sudo journalctl -u webufw -n 100 --no-pager
sudo systemctl stop webufw
```

服務由 systemd 管理。停止時會先處理尚未確認的變更；已確認規則保持原狀。根程序失敗時 systemd 會重新啟動並讀取回復紀錄。網頁子程序失敗會讓根程序結束並由 systemd 重啟整組程序。

## 網頁存取

預設監聽 `127.0.0.1:8088`，僅能從主機本身存取。若從其他電腦首次登入，可先透過 SSH tunnel：

```bash
ssh -L 8088:127.0.0.1:8088 使用者@主機IP
```

保持 SSH 連線，在自己的瀏覽器開啟 `http://127.0.0.1:8088`。設定頁可改成 `0.0.0.0:8088`、`[::]:8088` 或指定的本機 IP:連接埠，也可直接編輯 `/etc/webufw/config.json` 的 `listen` 欄位並保留 `password_hash`。儲存後需執行 `sudo systemctl restart webufw`；設定頁會提示目前生效位址及是否需要重啟。

區網使用者以 `http://<主機 IP>:8088` 存取，`0.0.0.0` 是監聽設定而非瀏覽器目的位址。修改監聽位址不會開放 UFW、防火牆設備或雲端安全群組。區網連線目前為明文 HTTP，請限制管理來源；HTTPS 尚未提供。

若重啟後仍無法連線，先在主機上檢查：

```bash
sudo systemctl status webufw --no-pager -l
sudo ss -ltnp 'sport = :8088'
curl --noproxy '*' -v http://127.0.0.1:8088/
curl --noproxy '*' -v http://主機IP:8088/
sudo ufw status verbose
```

- 如果仍只監聽 `127.0.0.1`，檢查設定檔與重啟是否成功。
- 如果監聽 `0.0.0.0:8088`、`*:8088` 或 `[::]:8088`，而主機 IP 請求得到 HTTP 200，管理頁已可經該位址回應；另一台電腦仍逾時時，請檢查 UFW 入站規則、上游防火牆與路由。
- UFW 已啟用且沒有允許管理來源時，可在網頁建立主機入站規則，或依實際來源執行以下範例，再從另一台電腦測試。不要將範例網段直接套用至不同網路。

```bash
# 範例：僅允許管理網段 192.168.1.0/24 存取 TCP 8088
sudo ufw allow from 192.168.1.0/24 to any port 8088 proto tcp
```

## 衝突與人工回復

遇到外部 CLI 變更、寫入期間強制終止、或回復指令失敗，WebUFW 不會整份覆蓋設定：

1. 使用主控台／SSH 檢查 `sudo ufw status numbered`、`sudo ufw show added` 與 `/var/lib/webufw/pending.json`。
2. 依紀錄確認必要規則及順序；只修正本次受影響規則。
3. 確認當前狀態正確後，停止 WebUFW。
4. 將 `/var/lib/webufw/pending.json` 移至同目錄下未使用的 `resolved-時間.json` 檔名，保留核對紀錄。
5. 重新啟動 WebUFW。封存紀錄不修改防火牆。

```bash
sudo systemctl stop webufw
sudo mv -n /var/lib/webufw/pending.json "/var/lib/webufw/resolved-$(date -u +%Y%m%dT%H%M%S).json"
sudo systemctl start webufw
```

請勿在尚未核對時直接刪除 pending.json。

## 更新

重新執行最新 Release 的管線安裝指令；安裝器會下載並校驗新 binary，然後重啟服務。更新不會移除 UFW 規則、既有密碼或所選腳本。已驗證 HSBearBig 版本可供 WebUFW 管理 Docker 規則；升級成未知 SHA256 後會先退回唯讀，待重新驗證。
