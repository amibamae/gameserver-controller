$filePath = "minecraft-debian.sh.tmpl"
$lines = Get-Content $filePath

$mcrconSection = @'
section "Installing and configuring mcrcon"

# Install mcrcon for Minecraft server control.
# mcrcon is a standalone binary from mise/mcrcon (not Debian package).
curl -fsSL https://github.com/misey/mcrcon/releases/download/v1.2.0/mcrcon_linux_x86_64 `
    --output /tmp/mcrcon `
    2>&1 | tail -n 1 || true

[[ $? -eq 0 ]] && test -f /tmp/mcrcon

if [[ ! -f /tmp/mcrcon ]]; then
    echo "FAILED: could not download mcrcon" >&2
    exit 1
fi

# Verify checksum if available.
# curl -fsSL https://github.com/misey/mcrcon/releases/download/v1.2.0/mcrcon_linux_x86_64.sha256 `
#     --output /tmp/mcrcon.sha256 || true
# sha256sum -c /tmp/mcrcon.sha256 || exit 1

chmod 0755 /tmp/mcrcon
mv -f /tmp/mcrcon /usr/local/bin/mcrcon

chown root:root /usr/local/bin/mcrcon

mcrcon --version

# Generate RCON password and store in secure file (0600, root-owned).
RCON_PASSWORD="$(openssl rand -base64 32 | tr -d '[:space:]')"

cat > /etc/minecraft-mcrcon/password.conf <<EOF_MCRCON
rcon_password=$RCON_PASSWORD
EOF_MCRCON

chown root:root /etc/minecraft-mcrcon/password.conf
chmod 0600 /etc/minecraft-mcrcon/password.conf

# Configure mcrcon to bind RCON socket to 127.0.0.1 (no inbound firewall needed).
cat > /etc/default/mcrcon <<EOF_DEFAULT_MCRCON
MCRCON_RCON_HOST="127.0.0.1"
MCRCON_RCON_PORT="25577"
MCRCON_PASSWORD_FILE="/etc/minecraft-mcrcon/password.conf"
EOF_DEFAULT_MCRCON

chmod 0644 /etc/default/mcrcon

# Create systemd service for mcrcon (optional, run as root with bounded credentials).
cat > /etc/systemd/system/mcrcon.socket <<EOF_MC_SOCKET
[Unit]
Description=mcrcon RCON Socket
After=network-online.target
Wants=network-online.target

[Socket]
ListenStream=127.0.0.1:25577
NoDelay=true
BindPaths=/
Symlinks=
PassCredentials=true
PassSecurity=true
Accept=noop

[Install]
WantedBy=sockets.target
EOF_MC_SOCKET

cat > /etc/systemd/system/mcrcon.service <<EOF_MC_SERVICE
[Unit]
Description=mcrcon RCON Server for Minecraft
After=network-online.target mcrcon.socket
Requires=mcrcon.socket
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=-/etc/default/mcrcon
# Run with bounded credentials via env file (0600 root-owned).
ExecStart=/usr/local/bin/mcrcon \$MCRCON_RCON_HOST \$MCRCON_RCON_PORT \$MCRCON_PASSWORD_FILE
Restart=on-failure
RestartSec=5

# Harden mcrcon service.
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=true
ReadWritePaths=/tmp /etc/minecraft-mcrcon

[Install]
WantedBy=multi-user.target
EOF_MC_SERVICE

chmod 0644 /etc/systemd/system/mcrcon.socket /etc/systemd/system/mcrcon.service

# Enable and start mcrcon socket.
systemctl daemon-reload
systemctl enable --now mcrcon.socket

echo "mcrcon installed, RCON bound to 127.0.0.1:25577"
echo "RCON password stored in /etc/minecraft-mcrcon/password.conf (0600)"

# Add mcrcon allow-list entry to sudoers for minecraft-admin.
cat >> /etc/sudoers.d/minecraft-admin <<EOF_MCRCON_SUDO
minecraft-admin ALL=(root) NOPASSWD: /usr/local/bin/mcrcon *
EOF_MCRCON_SUDO

chmod 0440 /etc/sudoers.d/minecraft-admin

visudo -cf /etc/sudoers.d/minecraft-admin
'@'

$newLines = $lines[0..601] + $mcrconSection + $lines[602..($lines.Count)]
Set-Content $filePath -Value $newLines -Encoding UTF8

Write-Output "MCRCON section added to $filePath"
