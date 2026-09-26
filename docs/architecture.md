# 架構與 API

## 程序與權限

`webufw run` 是 root Agent，啟動同一 binary 的 `_web` 子程序並指定非 root UID/GID，清空補充群組。未執行選用的 systemd 安裝時使用系統 `nobody`，安裝後使用 `webufw`。網頁程序沒有 UFW、Docker socket 或設定檔的直接存取權。缺少 UFW、Docker 或 ufw-docker 時，管理介面仍可啟動，首次密碼透過本機頁面與終端機一次性設定碼建立。

Agent 的 Unix socket 以檔案權限及 SO_PEERCRED 檢查來源，僅接受 webufw UID 或 root。內部 RPC 只接受固定操作名稱；沒有命令字串或任意 shell 端點。設定、認證與日誌操作亦透過 Agent。

Docker 狀態由 root Agent 透過本機 `/var/run/docker.sock` 讀取固定的 `/info`、`/containers/json?all=1` 與 `/networks` API，使用 Go 標準 HTTP client、5 秒逾時與 2 MiB 回應上限。HTTP 程序沒有 Docker socket，亦沒有一般用途的 Docker API 代理。規則寫入只使用使用者在網頁確認安裝的 WebUFW 已驗證腳本。

網頁與 Agent 的分離縮小 HTTP 程序可直接存取的範圍；若網頁程序遭控制，攻擊者仍可能呼叫 Agent 已允許的管理操作。這不是防禦已控制管理員或 root 的安全邊界。

## 規則來源

Agent 讀取 UFW 的 user.rules/user6.rules `### tuple ###` metadata，保留完整註解。複雜 tuple、應用 profile、來源埠、介面與不一致 Docker 註解均唯讀。模型使用完整內容雜湊識別，不將畫面列號當作刪除主鍵。

實際主機寫入使用參數陣列執行 UFW。WebUFW 不內嵌 ufw-docker 腳本或修補檔；設定頁可選擇 HSBearBig 或 chaifeng 的最新 commit，下載後先核對 SHA256 才安裝到 `/var/lib/webufw/ufw-docker`，不覆蓋 `/usr/local/bin/ufw-docker`。新下載的來源未經 WebUFW 寫入相容驗證，只供獨立 CLI 使用，網頁 Docker 規則唯讀。為保護既有安裝與待確認變更回復，舊版已驗證腳本仍以既有 SHA256 辨識並可執行原寫入流程。資料庫中不另存一套規則；pending.json 僅存短期操作日誌及回復所需內容。

## CLI 同步與鎖定

UFW 使用 `/run/ufw.lock` 的 POSIX record lock。Agent 在同一把鎖內完成版本核對、指令序列及操作結果記錄。根程序持鎖；受限 `_ufw` helper 驗證繼承的檔案描述符及鎖持有 PID，才以已安裝的 UFW/Python entrypoint 執行命令，避免重複取得父程序的鎖。

這個相容層針對 Ubuntu 24.04 UFW 0.36.2；升級 UFW 時需重新執行 VM 測試。遵守原生 UFW 鎖的 CLI 寫入會序列化。直接編輯檔案、原始 iptables 命令及 Docker 本身不受這把鎖約束，故另外比對前後狀態。

頁面每 3 秒透過 `/changes` 檢查檔案 metadata、Docker 容器與待確認狀態，變更時才取得完整 Snapshot。Hint 不是規則版本，也不能用於授權寫入。完整唯讀查詢會重新讀取 UFW 規則檔、腳本與 Docker API；UFW 核心啟用狀態及 DOCKER-USER 鏈一致性檢查最多重用 30 秒。啟用設定、整合設定或 Docker socket 改變會使結果失效。**預覽、套用、確認及回復永遠即時檢查核心狀態**，不使用這份顯示用快取；直接操作 iptables 後，顯示可能稍後更新，但不能藉此跳過寫入前的核對。

預覽有效 2 分鐘；套用前重新檢查 UFW 設定內容、Docker 容器網路狀態與腳本版本。衝突回 HTTP 409。

## 回復

危險操作成功後提供 60 秒確認。計時器在 root Agent，與瀏覽器連線無關。正常中斷及重新啟動時以 journal 比對狀態，只刪除本次新增內容、以原位置插入本次刪除內容、回復本次變更的 UFW 啟用狀態。寫入紀錄以 fsync + atomic rename 落盤。

外部變更或不確定的 in-flight 結果會阻止自動回復，需要人工核對。回復亦可能因系統命令失敗而無法完成；UI 不宣稱一定可以挽救所有網路配置。

## HTTP API

全部路徑位於 `/api/v1`。登入以外需有效工作階段。所有 POST 需要精確 Origin 與 JSON Content-Type；登入以外再驗證 X-CSRF-Token。

| 方法與路徑 | 功能 |
|---|---|
| GET、POST `/setup` | 首次設定狀態；以終端機一次性設定碼建立密碼（只允許本機監聽） |
| POST `/login` | username/password 登入，回傳 csrf |
| GET `/session` | 取得目前工作階段的 csrf |
| POST `/logout` | 結束工作階段 |
| GET `/changes` | 更新提示與是否需要重新檢查核心狀態 |
| GET `/status`、`/rules`、`/containers` | 目前 Snapshot，含 revision、規則、容器、環境及 pending |
| POST `/preview` | Change → 預覽 ID、added、removed、commands、warnings |
| POST `/apply` | `{ "id": "preview-id" }` |
| POST `/confirm`、`/rollback` | `{ "id": "pending-id" }` |
| GET `/logs` | 最近 100 筆 firewall / audit |
| GET `/settings` | 本機監聽位址與管理者名稱，不含密碼雜湊 |
| GET `/sources` | 可選來源、已安裝版本及相容狀態 |
| POST `/source.prepare`、`/source.install` | 選來源與預覽固定版本；核對 SHA256 後安裝到私有資料目錄 |
| POST `/settings.update` | 僅限本機 loopback 的 listen；重啟生效 |
| POST `/password` | current/password；使所有工作階段失效 |

Change.kind 為 `host.add`、`host.edit`、`host.delete`、`ufw.toggle`、`docker.add`、`docker.narrow`、`docker.delete` 或 `docker.sync`。

```json
{
  "kind": "docker.add",
  "revision": "目前 Snapshot 的 revision",
  "docker": {
    "container": "nginx",
    "network": "frontend",
    "port": "80",
    "protocol": "tcp",
    "source": "203.0.113.10"
  }
}
```

刪除、編輯及同步使用 `rule_id`。目的 IP 不由 Docker API 寫入請求提供。host 包含 action/direction/protocol/source/destination/port/comment；指定雙棧任意來源與目的時產生對應 IPv4/IPv6 規則。
