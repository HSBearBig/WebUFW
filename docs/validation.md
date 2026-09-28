# 驗證紀錄

驗證日期：2026-09-21～2026-09-23；VM 測試資料於 2026-09-25 移至獨立封存。版本：0.1.0-dev。這是可執行的第一版，尚未作為正式發行版標記。下列 VM 結果是在「安裝器固定安裝內附腳本」的舊部署流程下取得；目前改為先偵測系統既有腳本，也可由使用者選擇來源後下載；已核對註解格式的 HSBearBig 版本改由受鎖保護的 UFW 指令寫入。這個新寫入流程尚未在 VM 重跑。

## 測試環境

- Ubuntu 24.04.5 LTS 獨立 QEMU VM，2 vCPU / 1,536 MiB，TCG 軟體 CPU 模擬。
- 一般 rootful Docker Engine 29.1.3，bridge / NAT，iptables 相容後端。
- UFW 0.36.2 原生 CLI、IPv4 與 IPv6，兩個 Docker bridge 網路。
- Go 1.27.1 建置，CGO_ENABLED=0。單一 Linux amd64 執行檔約 10 MiB。
- VM 內兩個來源 namespace，分別作為允許與禁止來源；使用自行建置的靜態 TCP HTTP / UDP echo 容器發送真實封包。
- 真實規則寫入與安裝均在上述 VM 內進行。

## 自動化與介面

舊版執行 `go test -race ./...` 的 18 個 Go 測試、`go vet ./...`、9 個 Bash 腳本回歸案例與 Bash 語法檢查均通過。來源選擇版本曾通過 Go race 測試、vet 與 JavaScript 語法檢查；當時新增測試涵蓋首次網頁設定、來源選項、明確安裝、備份、內容變更拒絕與未驗證來源唯讀。舊版腳本回歸結果是歷史紀錄，目前程式不攜帶也不修補該腳本。來源選擇流程的防火牆真實封包與 VM 整合仍待驗證。

Go 測試包含規則解析、唯讀保留、IP/埠驗證、雙棧配對、完整內容識別、來源縮限、容器同步、版本衝突、部分指令失敗、逾時與重啟回復、in-flight 衝突、登入限速、工作階段、CSRF、Origin/Host 檢查及密碼變更後失效，以及登入／改密碼並行時的工作階段失效。另驗證 Docker API 欄位映射、不支援環境保留唯讀，以及顯示快取無法授權過期寫入。

瀏覽器已操作登入、規則預覽、套用與回復、Docker 目的 IP 唯讀欄位，以及淺色／深色切換與重新整理後保留偏好。預設跟隨系統，也可在設定中明確指定外觀。資源由本機提供，沒有 CDN。

## VM 結果

19 組真實 VM 案例全部通過：10 組整合、5 組失敗／生命週期與 4 組 Docker API 回歸。原始結果與 VM 專用測試程式另存於交付檔案 `WebUFW-VM-validation.tar.gz`；它們不是 WebUFW 的執行依賴。

| 類別 | 已驗證行為 |
|---|---|
| 原生架構 | root Agent / 一般使用者 web、socket 權限、前景 SIGTERM 與 systemd |
| 主機規則 | 新增、CLI 查詢、依完整內容刪除、恢復原位置 |
| CLI 相容 | 外部修改後版本衝突、原生 UFW 鎖序列化 API / CLI 寫入 |
| 真實封包 | TCP / UDP 允許來源可通、其他來源被擋；同埠不同容器互不混淆 |
| 來源縮限 | 既有不限來源規則會放行額外來源；縮限後阻擋，保留其他指定來源 |
| 多網路 / IPv6 | 選定網路的目的 IP、雙棧來源配對、IPv6 封包 |
| CLI 腳本 | 無指定埠時列出 UDP、重載保留、來源精確刪除 |
| 容器 IP | 改變後標記待同步、同步刪除舊目的、更新後封包可通 |
| 復原 | 60 秒未確認、正常重啟、SIGKILL 後 systemd 重啟；瀏覽器不必保持連線 |
| 衝突保護 | 待確認期間外部 CLI 修改會停止自動回復，保留外部規則 |
| 舊安裝更新 | 舊流程不默默覆蓋不同腳本、明確取代時完整備份、保留設定與密碼；新來源選擇流程未在 VM 重跑 |
| 服務停止 | 已確認規則仍有效，TCP 封包持續可通 |
| 讀取效能調整 | 20 容器／200 規則、變更提示能偵測 CLI 修改、顯示快取不能略過寫入前的核心狀態檢查 |

另外以真實瀏覽器登入 VM，在 200 條規則與 20 個既有容器下，CLI 新增後主機規則摘要自動從 195 更新為 196，沒有手動重新整理；測試後已刪除該條規則。另確認完整 VM 重新開機後，啟用的 systemd 服務會自動啟動 root Agent 與一般使用者 web。這是功能檢查；UI 工具的採樣間隔無法精確量出首次繪製時間。瀏覽器原始紀錄存於上述封存。

VM 測試先修正了 UFW 最後一條同 IP 版本規則的回復位置，以及 systemd 清除 RuntimeDirectory 後的衝突封存路徑處理。多網路測試也明確固定發布埠使用的 gateway，避免把不同 DNAT 目的誤當作規則失效。

## 資源與更新速度

200 條規則、20 個容器、單一管理者；RSS 為 Agent 與 web 程序合計。每種情境採樣約 36 秒，是短時間基準，並非長時間負載驗證。

| 情境 | RSS 最大採樣值 | 單核心平均 CPU |
|---|---:|---:|
| 無瀏覽器查詢，服務閒置 | 23.76 MiB | 0.194% |
| 瀏覽器頁面每 3 秒檢查變更 | 26.60 MiB | 2.190% |

兩種情境的 RSS 都低於 64 MiB；無查詢閒置 CPU 低於 1%。**持續開啟頁面的 CPU 在此 TCG VM 尚未達到 1% 目標**，不能將無查詢結果視為整體效能驗收完成。原生硬體的 CPU、長時間 RSS 與頁面更新時間仍待量測。

本版以固定本機 Docker API 讀取容器，並將輕量變更提示與完整狀態分開。最後一次約 37 秒輪詢期間只進行 2 次完整核心狀態重查，分別耗時 2.612 與 2.740 秒。外部 CLI 修改後，變更提示加完整狀態讀取的回歸量測為 1.549 秒；這個數字不含等待下一次瀏覽器輪詢與畫面繪製，不能當作 5 秒端到端保證。

另以獨立量測程序記錄短暫子程序 RSS：`ufw status` 15.25 MiB，20 容器的 `docker inspect` 29.88 MiB。這些數值不計入上表的兩個常駐程序 RSS，亦不是所有寫入命令的最壞峰值；閒置讀取已不啟動 Docker CLI。

舊流程最後建置的 Linux amd64 執行檔 SHA256（不是新來源選擇版本）：

```text
38bd32872f5a0b97303c230913b416e48de290d4238e83242e426f62a976173f
```

整合／失敗案例在效能調整前執行；讀取路徑調整後重新執行 4 組 API 回歸與 Go race 測試，最後一次變更提示排序修正後再次量測資源。最後另修正重新登入後仍顯示過期錯誤提示的前端問題，經 JavaScript 語法與瀏覽器檢查；後端未變更。這些紀錄不是聲稱每一個測試都在同一份二進位檔上重跑。

## 重現測試

VM 專用程式已從正式專案移出，保留在交付檔案 `WebUFW-VM-validation.tar.gz`。這些測試會改變防火牆、Docker 網路與容器，只可用於可丟棄的 VM。測試有 root、hostname 與環境變數三重檢查，`vm_setup.sh` 另要求 `/var/tmp/webufw-vm-ready` 標記。

1. 準備 hostname 為 `webufw-test` 的 Ubuntu 24.04 VM，安裝 UFW 與一般 Docker Engine。VM 使用者為 `ubuntu`。
2. 從獨立封存取得 `tests/fixtures/echo` 與 `tests/vm_*`。建置 `cmd/webufw` 與 echo；將靜態 echo binary 命名為 `server`、打包為 `test-image.tar`。
3. 將 WebUFW binary、tar 與封存中的 VM 測試程式放到 VM 的 `/home/ubuntu/`。
4. 此封存使用舊版安裝與密碼流程，請搭配對應歷史版本重現；新版以服務日誌中的初始密碼登入。建立上述就緒標記。
5. 依序在 VM 執行：

```bash
sudo env WEBUFW_DISPOSABLE_VM=1 bash /home/ubuntu/vm_setup.sh
sudo env WEBUFW_DISPOSABLE_VM=1 python3 /home/ubuntu/vm_integration.py
sudo env WEBUFW_DISPOSABLE_VM=1 python3 /home/ubuntu/vm_faults.py
sudo env WEBUFW_DISPOSABLE_VM=1 python3 /home/ubuntu/vm_resources.py
sudo env WEBUFW_DISPOSABLE_VM=1 python3 /home/ubuntu/vm_api_smoke.py
sudo env WEBUFW_DISPOSABLE_VM=1 python3 /home/ubuntu/vm_resources.py --measure-only
```

來源 namespace 為 `198.18.0.2` / `198.18.1.2`，IPv6 為 `2001:db8:aa::2` / `2001:db8:bb::2`。測試 VM SSH 使用本機轉送埠，不需要公開測試服務。

資源測試在 VM 停止 WebUFW 後，以原生 UFW 產生的範例規則展開至 200 條，再重新載入；這個產生器只供建立測試資料，不是產品的寫入方式。容器共 20 個。分別量測沒有瀏覽器及每 3 秒檢查變更、變更時才取得完整狀態，合計 root/web 兩個程序的 RSS 與單核心 CPU 比例；短暫 UFW status 與 Docker inspect 子程序另外量測。

## 驗證範圍

多網路容器以明確的 Docker gateway priority 指定發布埠的 DNAT 網路。規則只保護所選目的 IP，不會改寫 Docker 路由；其他網路的既有放行、信任網段與 conntrack 仍需一併檢查。

CLI 同步在功能上以即時重新讀取 UFW 驗證；頁面更新檢查間隔為 3 秒。TCG 模擬的指令與狀態查詢延遲不代表原生機器，**5 秒內反映的端到端時間目標仍需在原生硬體驗證**。CPU 數值也只代表此 VM 環境。

尚未驗證其他 Linux 發行版、其他 UFW 版本或長時間負載。Swarm、rootless、Docker Desktop、Docker 原生 nftables 及特殊 bridge gateway 模式的寫入被拒絕。in-flight 崩潰或外部 CLI 衝突會保留日誌並要求人工核對；不宣稱能自動復原所有情況。

## 2026-09-28：服務安裝與初始密碼

移除使用者可用的 WebUFW 子指令與首次設定碼流程。安裝腳本直接部署 systemd 服務，首次啟動產生隨機密碼並記錄在 journal，升級沿用既有設定。

- 安裝器測試在暫存目錄中使用模擬下載、systemctl、journalctl 與服務帳號，實際執行安裝檔案流程；涵蓋 curl 管線輸入、SHA256 拒絕、舊入口移除、設定與 pending 紀錄保留、初始密碼顯示、升級與啟動失敗。真實主機沒有重新安裝或修改防火牆。
- Go 測試涵蓋初始密碼登入、0600 設定檔與只儲存雜湊、重啟及修改密碼後的持續有效性、無效設定不覆寫，以及監聽設定與生效位址的區分。
- 真實 TCP 測試以 `0.0.0.0` 綁定暫用埠，從本機的非 loopback IPv4 位址讀取頁面並登入。這驗證程式的 socket 與 HTTP 路徑，不代表另一台電腦穿越防火牆的連線已驗證。
- `go test -race ./...`、`go vet ./...`、Bash / JavaScript 語法檢查通過，Linux amd64 / arm64 發布檔成功建置。

另唯讀檢查目前主機的既有服務：監聽所有介面的 8088，本機請求 loopback 與主機區網 IP 均回 HTTP 200。UFW 規則查詢需要 sudo 密碼，尚未確認跨機器連線被阻擋的位置。新版完整的 root Agent / systemd 安裝生命週期仍需在可丟棄 VM 中驗證。
