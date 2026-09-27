# 發布 WebUFW

安裝腳本從 GitHub Raw 取得，但執行檔只使用已發布的 GitHub Release，不會從 `main` 建置。將 `install.sh` 提交至 `main` 前，Raw 網址會回傳 404；第一個 Release 發布前，安裝器也找不到可下載的執行檔。

## 自動發版

1. 將欲發布的程式碼（包含 `.github/workflows/release.yml`、`install.sh`）提交並推送至 GitHub。
2. 在該提交建立 `vX.Y.Z` tag，例如 `v0.1.0`，再推送 tag：

   ```bash
   git tag v0.1.0
   git push origin v0.1.0
   ```

3. GitHub Actions 的 Release workflow 會驗證 tag 格式，執行安裝器測試、Go vet 與 Go 測試，交叉編譯 Linux amd64／arm64 執行檔，並核對 SHA256 與執行檔版本。全部通過後才建立同名的正式 GitHub Release，附上自動產生的發版說明。
4. 檢查 Release 的三個 assets：`webufw-linux-amd64`、`webufw-linux-arm64`、`SHA256SUMS`。`install.sh` 由 GitHub Raw 提供，不需上傳至 Release；GitHub 自動附上的 Source code 壓縮檔不能取代這三個資產。
5. 在可丟棄的 Ubuntu 24.04 VM 驗證管線安裝及服務啟動；需要 Docker 規則時，再測試 UFW/Docker 整合與真實封包。

Workflow 只接受 `vX.Y.Z` 正式版 tag，且只在推送 tag 時發布；重跑已存在的 Release 不會覆蓋原有資產。建置工作只有讀取權限，發布工作才取得 `contents: write`。不需要個人存取權杖；使用 GitHub Actions 提供的 `GITHUB_TOKEN`。

## 本機檢查或手動備援

在對應的原始碼版本執行 `./scripts/package-release.sh vX.Y.Z`（需要 Go 1.26 以上），再到 `dist/vX.Y.Z` 執行 `sha256sum -c SHA256SUMS`。此腳本只產生資產，不會上傳 GitHub。若是暫時性失敗且尚未建立 Release，可在 GitHub Actions 重跑該工作流程。若需修改程式碼或 workflow，請從修正後的提交建立新版本 tag；也可人工為既有 tag 建立 Release 並上傳上述三個檔案。

安裝器預設向 `https://api.github.com/repos/HSBearBig/WebUFW/releases/latest` 取得最新正式版的 tag，再從該 tag 下載資產。GitHub 的 `latest` 不含 prerelease；指定版本可用 `--version vX.Y.Z`。若要固定腳本本身，請從 `https://raw.githubusercontent.com/HSBearBig/WebUFW/vX.Y.Z/install.sh` 取得，並搭配相同版本的 `--version vX.Y.Z`。

`SHA256SUMS` 用於發現下載損壞或資產不一致；它與執行檔來自同一 Release，不能單獨取代對 GitHub 發布者的信任。
