# Build the console-free Windows GUI exe.
New-Item -ItemType Directory -Force -Path build
go build -trimpath -ldflags "-H windowsgui" -o build\copilot-bridge.exe .
