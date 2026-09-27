# Builds SteamApolloExporter.exe (no cgo / C compiler needed).
# Regenerates the resource file (icon + "run as administrator" manifest) first.
$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot

go run github.com/tc-hib/go-winres@v0.3.3 simply `
    --icon winres/icon.png --manifest gui --admin --arch amd64 `
    --product-name "Steam Apollo Exporter" --file-description "Export Steam games to Apollo"
if ($LASTEXITCODE) { exit $LASTEXITCODE }

go build -trimpath -ldflags "-H windowsgui -s -w" -o SteamApolloExporter.exe .
if ($LASTEXITCODE) { exit $LASTEXITCODE }
Write-Host "Built $PSScriptRoot\SteamApolloExporter.exe"
